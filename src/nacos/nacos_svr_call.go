package nacos

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

type traceIDContextKey string

const (
	traceIDKey    traceIDContextKey = "trace_id"
	traceIDField                    = "trace_id"
	traceIDHeader                   = "x-trace-id"
)

// GrpcCallParam holds all parameters needed for a single gRPC call via Nacos discovery.
type GrpcCallParam struct {
	FullMethod  string      //gRPC 服务的完整方法名，格式为 "/包名.服务名/方法名"
	Request     interface{} //方法入参
	Reply       interface{} //方法出参
	CallOptions []grpc.CallOption
}

// resolveGrpcAddrFromNacos creates a temporary naming client and returns one healthy instance address.
func resolveGrpcAddrFromNacos(nacosConf *PkgNacosConfig, namespaceID, serviceName, groupName, clusterName string) (string, error) {
	namingClient, err := newNacosNamingClient(nacosConf, namespaceID)
	if err != nil {
		return "", err
	}
	defer namingClient.CloseClient() // Fix: close nacos client to prevent resource leak

	if groupName == "" {
		groupName = DefaultNacosGroup
	}
	clusters := []string{}
	if clusterName != "" {
		clusters = []string{clusterName}
	}

	instance, err := namingClient.SelectOneHealthyInstance(vo.SelectOneHealthInstanceParam{
		ServiceName: serviceName,
		GroupName:   groupName,
		Clusters:    clusters,
	})
	if err != nil {
		return "", fmt.Errorf("nacos discover service=%s group=%s failed: %w", serviceName, groupName, err)
	}

	return fmt.Sprintf("%s:%d", instance.Ip, instance.Port), nil
}

// ---------------------------------------------------------------------------
// NacosGrpcClient —  NacosGrpcClient — 连接服务用的方式提供grpc client，自动维护连接的健康状态并重连
// ---------------------------------------------------------------------------

// NacosGrpcClient maintains a reusable gRPC connection to a Nacos-registered service.
// It re-resolves and reconnects automatically when the connection becomes unhealthy.
type NacosGrpcClient struct {
	mu          sync.Mutex
	conn        *grpc.ClientConn
	nacosConf   *PkgNacosConfig
	namespaceID string
	serviceName string
	groupName   string
	clusterName string
}

// NacosGrpcClientConfig holds configuration for creating a NacosGrpcClient.
type NacosGrpcClientConfig struct {
	NacosConf   *PkgNacosConfig
	NamespaceID string
	ServiceName string
	GroupName   string // optional, defaults to DefaultNacosGroup
	ClusterName string // optional
}

// NewNacosGrpcClient creates a pool and eagerly establishes the first connection.
func NewNacosGrpcClient(cfg NacosGrpcClientConfig) (*NacosGrpcClient, error) {
	if cfg.NacosConf == nil {
		return nil, fmt.Errorf("nacos config is nil")
	}
	if cfg.ServiceName == "" {
		return nil, fmt.Errorf("service name is required")
	}
	if cfg.GroupName == "" {
		cfg.GroupName = DefaultNacosGroup
	}

	pool := &NacosGrpcClient{
		nacosConf:   cfg.NacosConf,
		namespaceID: cfg.NamespaceID,
		serviceName: cfg.ServiceName,
		groupName:   cfg.GroupName,
		clusterName: cfg.ClusterName,
	}

	if err := pool.connect(context.Background()); err != nil {
		return nil, err
	}
	return pool, nil
}

// Invoke calls the given gRPC full method through the pooled connection.
// The connection is automatically refreshed if it is no longer ready.
// Fix: Add retry logic to handle race condition where connection fails after retrieval.
func (p *NacosGrpcClient) Invoke(ctx context.Context, param GrpcCallParam) error {
	start := time.Now()
	ctx, traceID := injectTraceID(ctx)
	ctx = injectTraceMetadata(ctx, traceID)
	logCtx := buildLogCtxWithTrace(ctx, logrus.Fields{
		"service": p.serviceName,
		"method":  param.FullMethod,
	})

	// Fix: Retry up to 2 times to handle race condition where connection becomes unhealthy
	maxRetries := 2
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		conn, err := p.getConn(ctx)
		if err != nil {
			logrus.WithFields(logCtx).Errorf("get pooled grpc connection failed (attempt %d): %v", attempt+1, err)
			lastErr = err
			continue
		}

		if err := conn.Invoke(ctx, param.FullMethod, param.Request, param.Reply, param.CallOptions...); err != nil {
			lastErr = err
			logCtx["elapsed_ms"] = time.Since(start).Milliseconds()
			logCtx["attempt"] = attempt + 1
			logrus.WithFields(logCtx).Warnf("grpc invoke failed, will retry: %v", err)

			// If this is not the last attempt, mark connection as bad and retry
			if attempt < maxRetries-1 {
				p.mu.Lock()
				_ = p.conn.Close()
				p.conn = nil
				p.mu.Unlock()
				continue
			}
			logrus.WithFields(logCtx).Errorf("grpc invoke via pool failed after %d attempts: %v", maxRetries, err)
			return err
		}

		logCtx["elapsed_ms"] = time.Since(start).Milliseconds()
		if attempt > 0 {
			logCtx["attempt"] = attempt + 1
		}
		logrus.WithFields(logCtx).Info("grpc invoke via pool succeeded")
		return nil
	}

	return lastErr
}

// Close closes the underlying gRPC connection.
func (p *NacosGrpcClient) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
}

func (p *NacosGrpcClient) getConn(ctx context.Context) (*grpc.ClientConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn != nil {
		state := p.conn.GetState()
		// Fix: Check for READY, IDLE, and CONNECTING states explicitly.
		// CONNECTING is acceptable - let Invoke handle it.
		if state == connectivity.Ready || state == connectivity.Idle || state == connectivity.Connecting {
			return p.conn, nil
		}
		// Only reconnect if connection is definitely dead
		if state == connectivity.Shutdown || state == connectivity.TransientFailure {
			logrus.WithFields(logrus.Fields{
				"service": p.serviceName,
				"state":   state.String(),
			}).Warn("connection is in bad state, reconnecting")
		}
		_ = p.conn.Close()
		p.conn = nil
	}

	if err := p.connect(ctx); err != nil {
		return nil, err
	}
	return p.conn, nil
}

func (p *NacosGrpcClient) connect(ctx context.Context) error {
	// Fix: Add timeout to prevent indefinite blocking if Nacos is unreachable
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	addr, err := resolveGrpcAddrFromNacos(p.nacosConf, p.namespaceID, p.serviceName, p.groupName, p.clusterName)
	logCtx := buildLogCtxWithTrace(ctx, logrus.Fields{
		"service": p.serviceName,
	})
	if err != nil {
		logrus.WithFields(logCtx).Errorf("resolve grpc address failed: %v", err)
		return err
	}
	logCtx["addr"] = addr

	// Use grpc.NewClient with keepalive to detect stale connections
	// gRPC uses lazy connection by default, so we don't wait for Ready state
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second, //十秒一次心跳检测
			Timeout:             3 * time.Second,  //检测时等待3秒
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		logrus.WithFields(logCtx).Errorf("create grpc client failed: %v", err)
		return fmt.Errorf("grpc connect to %s (service=%s) failed: %w", addr, p.serviceName, err)
	}

	p.conn = conn
	logrus.WithFields(logCtx).Info("grpc pool connection initialized (lazy connection)")
	return nil
}

func injectTraceID(ctx context.Context) (context.Context, string) {
	if ctx == nil {
		ctx = context.Background()
	}

	if traceID, ok := ctx.Value(traceIDKey).(string); ok && traceID != "" {
		return ctx, traceID
	}
	if traceID, ok := ctx.Value(traceIDField).(string); ok && traceID != "" {
		ctx = context.WithValue(ctx, traceIDKey, traceID)
		return ctx, traceID
	}

	traceID := uuid.NewString()
	ctx = context.WithValue(ctx, traceIDKey, traceID)
	return ctx, traceID
}

func injectTraceMetadata(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		return ctx
	}

	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok || md == nil {
		md = metadata.New(nil)
	} else {
		md = md.Copy()
	}
	md.Set(traceIDField, traceID)
	return metadata.NewOutgoingContext(ctx, md)
}

func buildLogCtxWithTrace(ctx context.Context, base logrus.Fields) logrus.Fields {
	traceID, _ := ctx.Value(traceIDKey).(string)
	if traceID == "" {
		traceID, _ = ctx.Value(traceIDField).(string)
	}
	logCtx := logrus.Fields{}
	for k, v := range base {
		logCtx[k] = v
	}
	if traceID != "" {
		logCtx[traceIDField] = traceID
	}
	return logCtx
}
