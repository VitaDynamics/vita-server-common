package nacos

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/resolver"
)

const (
	// nacosResolverScheme 只通过 grpc.WithResolvers 注入到单个 channel，不注册进 grpc 全局 resolver 表。
	nacosResolverScheme = "nacos"
	// nacosResolverSyncInterval 订阅推送之外的兜底轮询间隔：Nacos 推送通道静默断开时仍能刷新地址。
	nacosResolverSyncInterval = 30 * time.Second
	// nacosResolverMinInterval ResolveNow 的最小间隔：grpc 在连接失败时会连续触发，这里合并成一次查询。
	nacosResolverMinInterval = time.Second
)

// nacosResolverBuilder 把一个 Nacos 服务解析为 grpc 地址列表。
// 服务标识复用客户端持有的 nacosService，target 里只带服务名便于排查。
type nacosResolverBuilder struct {
	svc *nacosService

	mu      sync.Mutex
	current *nacosResolver
}

func newNacosResolverBuilder(svc *nacosService) *nacosResolverBuilder {
	return &nacosResolverBuilder{svc: svc}
}

func (b *nacosResolverBuilder) Scheme() string {
	return nacosResolverScheme
}

// target 返回该 builder 对应的 grpc target。
func (b *nacosResolverBuilder) target() string {
	return fmt.Sprintf("%s:///%s", nacosResolverScheme, url.PathEscape(b.svc.serviceName))
}

// Build 每次 channel 退出 idle 都会被调用，这里同步做一次解析，让 channel 立刻拿到当前实例列表。
func (b *nacosResolverBuilder) Build(_ resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	r := &nacosResolver{
		builder: b,
		cc:      cc,
		trigger: make(chan struct{}, 1),
		stopCh:  make(chan struct{}),
	}

	r.resolve()

	// 订阅失败不阻塞建连：兜底轮询与 ResolveNow 仍能拿到最新地址。
	r.subParam = r.newSubscribeParam()
	if err := b.svc.namingClient.Subscribe(r.subParam); err != nil {
		logrus.WithFields(r.logFields()).Warnf("subscribe nacos service failed, fallback to periodic sync: %v", err)
		r.subParam = nil
	}

	b.mu.Lock()
	b.current = r
	b.mu.Unlock()

	go r.watch()
	return r, nil
}

// resolveNow 主动触发一次解析。channel 处于 idle 时 grpc 已关闭 resolver，
// 此时无需处理：下次退出 idle 会重新 Build，并在 Build 内同步解析一次。
func (b *nacosResolverBuilder) resolveNow() {
	b.mu.Lock()
	current := b.current
	b.mu.Unlock()
	if current != nil {
		current.notify()
	}
}

type nacosResolver struct {
	builder  *nacosResolverBuilder
	cc       resolver.ClientConn
	subParam *vo.SubscribeParam
	trigger  chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once

	mu        sync.Mutex
	lastAddrs []string
}

func (r *nacosResolver) ResolveNow(resolver.ResolveNowOptions) {
	r.notify()
}

func (r *nacosResolver) Close() {
	r.stopOnce.Do(func() {
		close(r.stopCh)

		r.builder.mu.Lock()
		if r.builder.current == r {
			r.builder.current = nil
		}
		r.builder.mu.Unlock()

		// Unsubscribe 依据 SubscribeParam 中回调字段的地址定位订阅，必须传回注册时的同一个 param。
		if r.subParam != nil {
			if err := r.builder.svc.namingClient.Unsubscribe(r.subParam); err != nil {
				logrus.WithFields(r.logFields()).Warnf("unsubscribe nacos service failed: %v", err)
			}
		}
	})
}

func (r *nacosResolver) notify() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

func (r *nacosResolver) watch() {
	ticker := time.NewTicker(nacosResolverSyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.resolve()
		case <-r.trigger:
			// 等一个最小间隔再查，把连接失败期间的密集 ResolveNow 合并成一次。
			select {
			case <-time.After(nacosResolverMinInterval):
				r.resolve()
			case <-r.stopCh:
				return
			}
		}
	}
}

// resolve 查询 Nacos 健康实例并推送给 grpc；解析失败只上报错误，保留上一次的地址列表。
func (r *nacosResolver) resolve() {
	addrs, err := selectGrpcAddrsFromNacos(r.builder.svc)
	if err != nil {
		r.cc.ReportError(err)
		logrus.WithFields(r.logFields()).Warnf("resolve nacos service failed: %v", err)
		return
	}

	r.mu.Lock()
	changed := !sameAddrs(r.lastAddrs, addrs)
	r.lastAddrs = addrs
	r.mu.Unlock()

	state := resolver.State{Addresses: make([]resolver.Address, 0, len(addrs))}
	for _, addr := range addrs {
		state.Addresses = append(state.Addresses, resolver.Address{Addr: addr})
	}
	if err := r.cc.UpdateState(state); err != nil {
		logrus.WithFields(r.logFields()).Warnf("update grpc resolver state failed: %v", err)
		return
	}
	if changed {
		logrus.WithFields(r.logFields()).Infof("nacos instances updated: %s", strings.Join(addrs, ","))
	}
}

func (r *nacosResolver) newSubscribeParam() *vo.SubscribeParam {
	return &vo.SubscribeParam{
		ServiceName: r.builder.svc.serviceName,
		GroupName:   r.builder.svc.groupName,
		Clusters:    r.builder.svc.clusters(),
		SubscribeCallback: func(_ []model.Instance, err error) {
			if err != nil {
				logrus.WithFields(r.logFields()).Warnf("nacos subscribe callback error: %v", err)
				return
			}
			r.notify()
		},
	}
}

func (r *nacosResolver) logFields() logrus.Fields {
	return r.builder.svc.logFields()
}

func sameAddrs(old, new []string) bool {
	if len(old) != len(new) {
		return false
	}
	oldSorted := append([]string(nil), old...)
	newSorted := append([]string(nil), new...)
	sort.Strings(oldSorted)
	sort.Strings(newSorted)
	for i := range oldSorted {
		if oldSorted[i] != newSorted[i] {
			return false
		}
	}
	return true
}
