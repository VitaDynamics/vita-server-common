package test

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/VitaDynamics/vita-server-common/src/utils/nacos"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const healthCheckMethod = "/grpc.health.v1.Health/Check"

// fakeNamingClient 只实现 NacosGrpcClient 真正用到的 SelectInstances / Subscribe / Unsubscribe，
// 用于模拟实例地址在运行期发生变化（Pod 重建换 IP）。
type fakeNamingClient struct {
	mu        sync.Mutex
	addrs     []string
	callbacks []*vo.SubscribeParam
	unsubbed  int
}

func newFakeNamingClient(addrs ...string) *fakeNamingClient {
	return &fakeNamingClient{addrs: addrs}
}

// setAddrs 替换实例列表，并按 Nacos 推送的方式通知订阅者。
func (f *fakeNamingClient) setAddrs(addrs ...string) {
	f.mu.Lock()
	f.addrs = addrs
	callbacks := append([]*vo.SubscribeParam(nil), f.callbacks...)
	f.mu.Unlock()

	for _, param := range callbacks {
		param.SubscribeCallback(nil, nil)
	}
}

func (f *fakeNamingClient) unsubscribeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unsubbed
}

func (f *fakeNamingClient) SelectInstances(vo.SelectInstancesParam) ([]model.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	instances := make([]model.Instance, 0, len(f.addrs))
	for _, addr := range f.addrs {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var portNum uint64
		if _, err := fmt.Sscanf(port, "%d", &portNum); err != nil {
			return nil, err
		}
		instances = append(instances, model.Instance{
			Ip:      host,
			Port:    portNum,
			Weight:  1,
			Healthy: true,
			Enable:  true,
		})
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("no healthy instances")
	}
	return instances, nil
}

func (f *fakeNamingClient) Subscribe(param *vo.SubscribeParam) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callbacks = append(f.callbacks, param)
	return nil
}

func (f *fakeNamingClient) Unsubscribe(param *vo.SubscribeParam) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unsubbed++
	for i, existing := range f.callbacks {
		if existing == param {
			f.callbacks = append(f.callbacks[:i], f.callbacks[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeNamingClient) RegisterInstance(vo.RegisterInstanceParam) (bool, error) {
	return true, nil
}

func (f *fakeNamingClient) BatchRegisterInstance(vo.BatchRegisterInstanceParam) (bool, error) {
	return true, nil
}

func (f *fakeNamingClient) DeregisterInstance(vo.DeregisterInstanceParam) (bool, error) {
	return true, nil
}

func (f *fakeNamingClient) UpdateInstance(vo.UpdateInstanceParam) (bool, error) {
	return true, nil
}

func (f *fakeNamingClient) GetService(vo.GetServiceParam) (model.Service, error) {
	return model.Service{}, nil
}

func (f *fakeNamingClient) SelectAllInstances(vo.SelectAllInstancesParam) ([]model.Instance, error) {
	return f.SelectInstances(vo.SelectInstancesParam{})
}

func (f *fakeNamingClient) SelectOneHealthyInstance(vo.SelectOneHealthInstanceParam) (*model.Instance, error) {
	instances, err := f.SelectInstances(vo.SelectInstancesParam{})
	if err != nil {
		return nil, err
	}
	return &instances[0], nil
}

func (f *fakeNamingClient) GetAllServicesInfo(vo.GetAllServiceInfoParam) (model.ServiceList, error) {
	return model.ServiceList{}, nil
}

func (f *fakeNamingClient) ServerHealthy() bool { return true }

func (f *fakeNamingClient) CloseClient() {}

// startHealthServer 启动一个 grpc 服务，用 Health.Check 的返回状态区分是哪个实例在应答。
func startHealthServer(t *testing.T, status healthpb.HealthCheckResponse_ServingStatus) (addr string, stop func()) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}

	server := grpc.NewServer()
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", status)
	healthpb.RegisterHealthServer(server, healthServer)

	go func() {
		_ = server.Serve(listener)
	}()

	return listener.Addr().String(), server.Stop
}

func newTestGrpcClient(t *testing.T, namingClient *fakeNamingClient, idleTimeout time.Duration) *nacos.NacosGrpcClient {
	t.Helper()

	client, err := nacos.NewNacosGrpcClient(nacos.NacosGrpcClientConfig{
		NacosConf: &nacos.PkgNacosConfig{
			Enabled: true,
			Servers: []nacos.PkgNacosServerConfig{{Addr: "127.0.0.1", Port: 8848}},
		},
		ServiceName:  "vita-app-rpc",
		NamingClient: namingClient,
		IdleTimeout:  idleTimeout,
	})
	if err != nil {
		t.Fatalf("NewNacosGrpcClient failed: %v", err)
	}
	return client
}

func checkServingStatus(ctx context.Context, client *nacos.NacosGrpcClient) (healthpb.HealthCheckResponse_ServingStatus, error) {
	reply := &healthpb.HealthCheckResponse{}
	err := client.Invoke(ctx, nacos.GrpcCallParam{
		FullMethod: healthCheckMethod,
		Request:    &healthpb.HealthCheckRequest{},
		Reply:      reply,
	})
	return reply.GetStatus(), err
}

// TestInvokeFollowsInstanceAddressChange 覆盖线上故障场景：实例重建换了地址后，
// 复用的 channel 必须跟着切到新地址，而不是一直连旧地址报 connection refused。
func TestInvokeFollowsInstanceAddressChange(t *testing.T) {
	oldAddr, stopOld := startHealthServer(t, healthpb.HealthCheckResponse_SERVING)
	namingClient := newFakeNamingClient(oldAddr)

	client := newTestGrpcClient(t, namingClient, time.Hour)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status, err := checkServingStatus(ctx, client)
	if err != nil {
		t.Fatalf("invoke against original instance failed: %v", err)
	}
	if status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("got status %v from original instance, want SERVING", status)
	}

	// 模拟 Pod 重建：旧实例下线，新实例换了端口，Nacos 中只剩新地址。
	newAddr, stopNew := startHealthServer(t, healthpb.HealthCheckResponse_NOT_SERVING)
	defer stopNew()
	stopOld()
	namingClient.setAddrs(newAddr)

	status, err = checkServingStatus(ctx, client)
	if err != nil {
		t.Fatalf("invoke after instance address change failed: %v", err)
	}
	if status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("got status %v after address change, want NOT_SERVING from the new instance", status)
	}
}

// TestInvokeSkipsDeadInstance 多实例时，已经下线的实例不应该让调用失败。
func TestInvokeSkipsDeadInstance(t *testing.T) {
	deadAddr, stopDead := startHealthServer(t, healthpb.HealthCheckResponse_SERVING)
	liveAddr, stopLive := startHealthServer(t, healthpb.HealthCheckResponse_NOT_SERVING)
	defer stopLive()

	namingClient := newFakeNamingClient(deadAddr, liveAddr)
	client := newTestGrpcClient(t, namingClient, time.Hour)
	defer client.Close()

	stopDead()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status, err := checkServingStatus(ctx, client)
	if err != nil {
		t.Fatalf("invoke with one dead instance failed: %v", err)
	}
	if status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("got status %v, want NOT_SERVING from the surviving instance", status)
	}
}

// TestInvokeAfterIdleUsesFreshInstances 复现线上故障：定时任务每天只调一次，
// 期间 channel 早已降级为 idle 且服务实例已经重建。退出 idle 时必须重新解析实例，
// 否则会一直连旧 Pod 地址并报 connection refused。
func TestInvokeAfterIdleUsesFreshInstances(t *testing.T) {
	oldAddr, stopOld := startHealthServer(t, healthpb.HealthCheckResponse_SERVING)
	namingClient := newFakeNamingClient(oldAddr)

	idleTimeout := 200 * time.Millisecond
	client := newTestGrpcClient(t, namingClient, idleTimeout)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := checkServingStatus(ctx, client); err != nil {
		t.Fatalf("invoke against original instance failed: %v", err)
	}

	// 等 channel 降级为 idle，此时 grpc 已关闭 resolver。
	time.Sleep(3 * idleTimeout)

	// 旧实例彻底消失（端口不再监听，等价于 connection refused），Nacos 中换成新地址。
	newAddr, stopNew := startHealthServer(t, healthpb.HealthCheckResponse_NOT_SERVING)
	defer stopNew()
	stopOld()
	namingClient.setAddrs(newAddr)

	status, err := checkServingStatus(ctx, client)
	if err != nil {
		t.Fatalf("invoke after idle failed: %v", err)
	}
	if status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("got status %v after idle, want NOT_SERVING from the new instance", status)
	}
}

// TestCloseUnsubscribes 关闭 client 必须注销 Nacos 订阅，避免连接反复重建时泄漏订阅。
func TestCloseUnsubscribes(t *testing.T) {
	addr, stop := startHealthServer(t, healthpb.HealthCheckResponse_SERVING)
	defer stop()

	namingClient := newFakeNamingClient(addr)
	client := newTestGrpcClient(t, namingClient, time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := checkServingStatus(ctx, client); err != nil {
		t.Fatalf("invoke failed: %v", err)
	}

	client.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if namingClient.unsubscribeCount() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected Unsubscribe to be called on Close")
}
