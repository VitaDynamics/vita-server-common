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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

	// connectReadyTimeout 等待 channel 就绪的上限，覆盖退出 idle、重新解析与重连。
	connectReadyTimeout = 10 * time.Second
	// defaultGrpcIdleTimeout channel 空闲多久后降级为 idle。
	// 退出 idle 时 grpc 会重建 resolver，从而重新拉取实例列表，低频调用方因此不会连到已下线的旧实例。
	defaultGrpcIdleTimeout = 30 * time.Minute
	// unavailableRetries 针对 Unavailable 的额外重试次数。实例重建后地址会变，
	// 必须强制回 Nacos 重新解析再打一次，否则调用方只会看到指向旧实例的 connection refused。
	unavailableRetries = 1

	// 多实例场景下按 round_robin 分发，并自动跳过没有就绪的实例。
	nacosGrpcServiceConfig = `{"loadBalancingConfig":[{"round_robin":{}}]}`
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
// 地址由内置的 nacos resolver 持续维护：实例上下线、Pod 重建后 channel 会自行换到新地址，
// 不会像直接 dial "ip:port" 那样把某一个实例地址钉死在连接上。
type NacosGrpcClient struct {
	mu              sync.Mutex
	conn            *grpc.ClientConn
	resolverBuilder *nacosResolverBuilder
	namingClient    naming_client.INamingClient
	nacosConf       *PkgNacosConfig
	namespaceID     string
	serviceName     string
	groupName       string
	clusterName     string
	idleTimeout     time.Duration
}

// NacosGrpcClientConfig holds configuration for creating a NacosGrpcClient.
type NacosGrpcClientConfig struct {
	NacosConf    *PkgNacosConfig
	NamespaceID  string
	ServiceName  string
	GroupName    string                      // optional, defaults to DefaultNacosGroup
	ClusterName  string                      // optional
	NamingClient naming_client.INamingClient // optional: inject for tests; if nil, obtained via GetNacosNamingClient
	IdleTimeout  time.Duration               // optional, defaults to defaultGrpcIdleTimeout
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
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultGrpcIdleTimeout
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
		idleTimeout:  cfg.IdleTimeout,
	}

	conn, builder, err := client.dialConn(context.Background())
	if err != nil {
		return nil, err
	}
	client.conn = conn
	client.resolverBuilder = builder
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

// Invoke calls the given gRPC full method through the shared connection.
// 调用前会确保 channel 处于 Ready；遇到 Unavailable 会强制回 Nacos 重新解析实例后再试一次。
func (p *NacosGrpcClient) Invoke(ctx context.Context, param GrpcCallParam) error {
	start := time.Now()
	ctx, traceID := injectTraceID(ctx)
	ctx = injectTraceMetadata(ctx, traceID)
	logCtx := buildLogCtxWithTrace(ctx, logrus.Fields{
		"service": p.serviceName,
		"method":  param.FullMethod,
	})

	maxAttempts := ResolveMaxAttempts(param)
	retriesLeftOnUnavailable := unavailableRetries
	var lastErr error

	for attempt := 1; ; attempt++ {
		lastErr = p.invokeOnce(ctx, param)
		logCtx["attempt"] = attempt
		logCtx["elapsed_ms"] = time.Since(start).Milliseconds()

		if lastErr == nil {
			logrus.WithFields(logCtx).Info("grpc invoke succeeded")
			return nil
		}

		retry := attempt < maxAttempts
		if !retry && retriesLeftOnUnavailable > 0 && isUnavailable(lastErr) {
			retriesLeftOnUnavailable--
			retry = true
		}
		if !retry {
			logrus.WithFields(logCtx).Errorf("grpc invoke failed after %d attempts: %v", attempt, lastErr)
			return lastErr
		}

		logrus.WithFields(logCtx).Warnf("grpc invoke failed (attempt %d), re-resolving instances and retrying: %v", attempt, lastErr)
		p.refreshInstances()
	}
}

func (p *NacosGrpcClient) invokeOnce(ctx context.Context, param GrpcCallParam) error {
	conn, err := p.getConn(ctx)
	if err != nil {
		return err
	}
	if err := p.ensureReady(ctx, conn); err != nil {
		return err
	}
	return conn.Invoke(ctx, param.FullMethod, param.Request, param.Reply, param.CallOptions...)
}

func isUnavailable(err error) bool {
	return status.Code(err) == codes.Unavailable
}

// refreshInstances 让 resolver 立刻回 Nacos 重新解析实例列表。
func (p *NacosGrpcClient) refreshInstances() {
	p.mu.Lock()
	builder := p.resolverBuilder
	p.mu.Unlock()
	if builder != nil {
		builder.resolveNow()
	}
}

// Close closes the underlying gRPC connection.
// 关闭 channel 会连带关闭 resolver，并注销 Nacos 订阅。
func (p *NacosGrpcClient) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
	p.resolverBuilder = nil
}

// getConn 复用同一个 channel。地址变化由 resolver 负责，只有 channel 已被关闭时才重建。
func (p *NacosGrpcClient) getConn(ctx context.Context) (*grpc.ClientConn, error) {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn != nil && conn.GetState() != connectivity.Shutdown {
		return conn, nil
	}

	newConn, newBuilder, err := p.dialConn(ctx)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil && p.conn.GetState() != connectivity.Shutdown {
		_ = newConn.Close()
		return p.conn, nil
	}
	if p.conn != nil {
		_ = p.conn.Close()
	}
	p.conn = newConn
	p.resolverBuilder = newBuilder
	return p.conn, nil
}

// ensureReady 在调用前把 channel 推到 Ready。
// 低频调用（如每天一次的定时任务）时 channel 早已因空闲降级到 IDLE，
// 这里显式触发重新解析与重连，避免直接在旧地址上发起调用。
func (p *NacosGrpcClient) ensureReady(ctx context.Context, conn *grpc.ClientConn) error {
	if conn.GetState() == connectivity.Ready {
		return nil
	}
	p.refreshInstances()
	// 退出 idle 会重建 resolver，并在重建时同步拉一次最新实例列表。
	conn.Connect()
	return p.waitReady(ctx, conn)
}

func (p *NacosGrpcClient) waitReady(ctx context.Context, conn *grpc.ClientConn) error {
	waitCtx, cancel := context.WithTimeout(ctx, connectReadyTimeout)
	defer cancel()

	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			return nil
		}
		if state == connectivity.Shutdown {
			return fmt.Errorf("grpc connection shutdown before ready service=%s", p.serviceName)
		}
		if !conn.WaitForStateChange(waitCtx, state) {
			return fmt.Errorf("grpc connect timeout service=%s state=%s: %w", p.serviceName, state.String(), waitCtx.Err())
		}
	}
}

// dialConn 创建由 nacos resolver 驱动的 channel。
// 建连前先确认服务当前有健康实例，让调用方在服务整体不可用时能立即拿到错误并进入自己的重试逻辑。
func (p *NacosGrpcClient) dialConn(ctx context.Context) (*grpc.ClientConn, *nacosResolverBuilder, error) {
	logCtx := buildLogCtxWithTrace(ctx, logrus.Fields{
		"service": p.serviceName,
	})

	if _, err := selectGrpcAddrsFromNacos(p.namingClient, p.serviceName, p.groupName, p.clusterName); err != nil {
		logrus.WithFields(logCtx).Errorf("resolve grpc address failed: %v", err)
		return nil, nil, err
	}

	builder := newNacosResolverBuilder(p.namingClient, p.serviceName, p.groupName, p.clusterName)
	conn, err := grpc.NewClient(builder.target(),
		grpc.WithResolvers(builder),
		grpc.WithDefaultServiceConfig(nacosGrpcServiceConfig),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(defaultGrpcKeepaliveParams()),
		grpc.WithIdleTimeout(p.idleTimeout),
	)
	if err != nil {
		logrus.WithFields(logCtx).Errorf("create grpc client failed: %v", err)
		return nil, nil, fmt.Errorf("grpc create client for service=%s failed: %w", p.serviceName, err)
	}

	conn.Connect()
	if err := p.waitReady(ctx, conn); err != nil {
		_ = conn.Close()
		logrus.WithFields(logCtx).Errorf("grpc connection not ready: %v", err)
		return nil, nil, err
	}

	logrus.WithFields(logCtx).Info("grpc connection ready")
	return conn, builder, nil
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
