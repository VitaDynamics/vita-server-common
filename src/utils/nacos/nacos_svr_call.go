package nacos

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
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
	traceIDKey traceIDContextKey = "trace_id"
)

const (
	traceIDField  = "trace_id"
	traceIDHeader = "x-trace-id"

	// gRPC server default enforcement: MinTime=5m, PermitWithoutStream=false.
	// Avoid idle pings and keep interval conservative to prevent ENHANCE_YOUR_CALM / too_many_pings.
	grpcKeepaliveTime    = 30 * time.Second
	grpcKeepaliveTimeout = 10 * time.Second
)

func defaultGrpcKeepaliveParams() keepalive.ClientParameters {
	return keepalive.ClientParameters{
		Time:                grpcKeepaliveTime,
		Timeout:             grpcKeepaliveTimeout,
		PermitWithoutStream: false,
	}
}

// GrpcCallParam holds all parameters needed for a single gRPC call via Nacos discovery.
type GrpcCallParam struct {
	FullMethod  string      //gRPC 服务的完整方法名，格式为 "/包名.服务名/方法名"
	Request     interface{} //方法入参
	Reply       interface{} //方法出参
	CallOptions []grpc.CallOption
	Retry       bool //是否启用重试（连接失败时重试一次）
	RetryCount  int  //重试次数，默认为1次
}

// SelectGrpcAddr picks one healthy instance address via a reusable naming client.
func SelectGrpcAddr(namingClient naming_client.INamingClient, serviceName, groupName, clusterName string) (string, error) {
	addrs, err := selectGrpcAddrsFromNacos(namingClient, serviceName, groupName, clusterName)
	if err != nil {
		return "", err
	}
	return addrs[0], nil
}

// selectGrpcAddrsFromNacos returns all healthy instance addresses via a reusable naming client.
func selectGrpcAddrsFromNacos(namingClient naming_client.INamingClient, serviceName, groupName, clusterName string) ([]string, error) {
	if namingClient == nil {
		return nil, fmt.Errorf("nacos naming client is nil")
	}
	if groupName == "" {
		groupName = DefaultNacosGroup
	}
	clusters := []string{}
	if clusterName != "" {
		clusters = []string{clusterName}
	}

	instances, err := namingClient.SelectInstances(vo.SelectInstancesParam{
		ServiceName: serviceName,
		GroupName:   groupName,
		Clusters:    clusters,
		HealthyOnly: true,
	})
	if err != nil {
		return nil, fmt.Errorf("nacos discover service=%s group=%s failed: %w", serviceName, groupName, err)
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("nacos discover service=%s group=%s returned no healthy instances", serviceName, groupName)
	}

	addrs := make([]string, 0, len(instances))
	for _, instance := range instances {
		addrs = append(addrs, fmt.Sprintf("%s:%d", instance.Ip, instance.Port))
	}

	return addrs, nil
}

// ---------------------------------------------------------------------------
// NacosGrpcClient — 连接服务用的方式提供grpc client，自动维护连接的健康状态并重连
// ---------------------------------------------------------------------------

// NacosGrpcClient maintains a reusable gRPC connection to a Nacos-registered service.
// It re-resolves and reconnects automatically when the connection becomes unhealthy.
type NacosGrpcClient struct {
	mu           sync.Mutex
	conn         *grpc.ClientConn
	namingClient naming_client.INamingClient
	nacosConf    *PkgNacosConfig
	namespaceID  string
	serviceName  string
	groupName    string
	clusterName  string
}

// NacosGrpcClientConfig holds configuration for creating a NacosGrpcClient.
type NacosGrpcClientConfig struct {
	NacosConf    *PkgNacosConfig
	NamespaceID  string
	ServiceName  string
	GroupName    string                      // optional, defaults to DefaultNacosGroup
	ClusterName  string                      // optional
	NamingClient naming_client.INamingClient // optional: inject for tests; if nil, obtained via GetNacosNamingClient
}

// NewNacosGrpcClient creates a client and eagerly establishes the first connection.
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

	var namingClient naming_client.INamingClient
	if cfg.NamingClient != nil {
		namingClient = cfg.NamingClient
	} else {
		var err error
		namingClient, err = GetNacosNamingClient(cfg.NacosConf, cfg.NamespaceID)
		if err != nil {
			return nil, err
		}
	}

	client := &NacosGrpcClient{
		namingClient: namingClient,
		nacosConf:    cfg.NacosConf,
		namespaceID:  cfg.NamespaceID,
		serviceName:  cfg.ServiceName,
		groupName:    cfg.GroupName,
		clusterName:  cfg.ClusterName,
	}

	conn, err := client.dialConn(context.Background())
	if err != nil {
		return nil, err
	}
	client.conn = conn
	return client, nil
}

// resolveMaxAttempts returns total invoke attempts based on Retry and RetryCount.
// RetryCount is the number of retries after the first failure; defaults to 1 when Retry is true.
func ResolveMaxAttempts(param GrpcCallParam) int {
	if !param.Retry {
		return 1
	}
	retryCount := param.RetryCount
	if retryCount <= 0 {
		retryCount = 1
	}
	return 1 + retryCount
}

// Invoke calls the given gRPC full method through the pooled connection.
// The connection is automatically refreshed if it is no longer ready.
func (p *NacosGrpcClient) Invoke(ctx context.Context, param GrpcCallParam) error {
	start := time.Now()
	ctx, traceID := injectTraceID(ctx)
	ctx = injectTraceMetadata(ctx, traceID)
	logCtx := buildLogCtxWithTrace(ctx, logrus.Fields{
		"service": p.serviceName,
		"method":  param.FullMethod,
	})

	maxAttempts := ResolveMaxAttempts(param)
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		conn, err := p.getConn(ctx)
		if err != nil {
			lastErr = err
			logCtx["attempt"] = attempt + 1
			if attempt < maxAttempts-1 {
				logrus.WithFields(logCtx).Warnf("get pooled grpc connection failed (attempt %d/%d), will retry: %v", attempt+1, maxAttempts, err)
				continue
			}
			logrus.WithFields(logCtx).Errorf("get pooled grpc connection failed after %d attempts: %v", maxAttempts, err)
			return err
		}

		if err := conn.Invoke(ctx, param.FullMethod, param.Request, param.Reply, param.CallOptions...); err != nil {
			lastErr = err
			logCtx["elapsed_ms"] = time.Since(start).Milliseconds()
			logCtx["attempt"] = attempt + 1

			if attempt < maxAttempts-1 {
				logrus.WithFields(logCtx).Warnf("grpc invoke failed (attempt %d/%d), will retry: %v", attempt+1, maxAttempts, err)
				p.invalidateConn()
				continue
			}
			logrus.WithFields(logCtx).Errorf("grpc invoke failed after %d attempts: %v", maxAttempts, err)
			return err
		}

		logCtx["elapsed_ms"] = time.Since(start).Milliseconds()
		if attempt > 0 {
			logCtx["attempt"] = attempt + 1
		}
		logrus.WithFields(logCtx).Info("grpc invoke succeeded")
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

func (p *NacosGrpcClient) invalidateConn() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
}

func (p *NacosGrpcClient) getConn(ctx context.Context) (*grpc.ClientConn, error) {
	if conn := p.pickHealthyConn(); conn != nil {
		return conn, nil
	}

	newConn, err := p.dialConn(ctx)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn != nil {
		state := p.conn.GetState()
		if state == connectivity.Ready || state == connectivity.Idle || state == connectivity.Connecting {
			_ = newConn.Close()
			return p.conn, nil
		}
		_ = p.conn.Close()
	}
	p.conn = newConn
	return p.conn, nil
}

func (p *NacosGrpcClient) pickHealthyConn() *grpc.ClientConn {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn == nil {
		return nil
	}

	state := p.conn.GetState()
	if state == connectivity.Ready || state == connectivity.Idle || state == connectivity.Connecting {
		return p.conn
	}

	if state == connectivity.Shutdown || state == connectivity.TransientFailure {
		logrus.WithFields(logrus.Fields{
			"service": p.serviceName,
			"state":   state.String(),
		}).Warn("connection is in bad state, reconnecting")
	}
	_ = p.conn.Close()
	p.conn = nil
	return nil
}

func (p *NacosGrpcClient) dialConn(ctx context.Context) (*grpc.ClientConn, error) {
	addrs, err := selectGrpcAddrsFromNacos(p.namingClient, p.serviceName, p.groupName, p.clusterName)
	logCtx := buildLogCtxWithTrace(ctx, logrus.Fields{
		"service": p.serviceName,
	})
	if err != nil {
		logrus.WithFields(logCtx).Errorf("resolve grpc address failed: %v", err)
		return nil, err
	}

	var lastErr error
	var lastAddr string
	for _, addr := range addrs {
		lastAddr = addr
		attemptLogCtx := buildLogCtxWithTrace(ctx, logrus.Fields{
			"service": p.serviceName,
			"addr":    addr,
		})

		conn, err := p.dialAddr(ctx, addr, attemptLogCtx)
		if err == nil {
			return conn, nil
		}

		lastErr = err
		logrus.WithFields(attemptLogCtx).Warnf("grpc instance connect failed, trying next instance: %v", err)
	}

	return nil, fmt.Errorf("all grpc instances connect failed service=%s instances=%d last_addr=%s: %w", p.serviceName, len(addrs), lastAddr, lastErr)
}

func (p *NacosGrpcClient) dialAddr(ctx context.Context, addr string, logCtx logrus.Fields) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(defaultGrpcKeepaliveParams()),
	)
	if err != nil {
		logrus.WithFields(logCtx).Errorf("create grpc client failed: %v", err)
		return nil, fmt.Errorf("grpc connect to %s (service=%s) failed: %w", addr, p.serviceName, err)
	}

	conn.Connect()
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			logrus.WithFields(logCtx).Info("grpc connection ready")
			return conn, nil
		}
		if state == connectivity.Shutdown {
			_ = conn.Close()
			return nil, fmt.Errorf("grpc connection shutdown before ready addr=%s service=%s", addr, p.serviceName)
		}
		if !conn.WaitForStateChange(ctx, state) {
			_ = conn.Close()
			return nil, fmt.Errorf("grpc connect timeout addr=%s service=%s: %w", addr, p.serviceName, ctx.Err())
		}
	}
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
