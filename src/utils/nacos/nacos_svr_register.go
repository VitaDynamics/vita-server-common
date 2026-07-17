package nacos

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

var (
	namingClientMu   sync.RWMutex
	namingClientConf *PkgNacosConfig
	namingClients    = make(map[string]naming_client.INamingClient)
)

const (
	DefaultNacosGroup   = "DEFAULT_GROUP"
	DefaultNacosCluster = "DEFAULT"
)

type RegisterServiceInstance struct {
	ServiceName string
	IP          string
	Port        uint64
	Metadata    map[string]string
	GroupName   string
	ClusterName string
	Weight      float64
	Enable      bool
	Healthy     bool
	Ephemeral   bool
}

type RegisterServicesParam struct {
	NacosConf   *PkgNacosConfig
	NamespaceID string
	Instances   []RegisterServiceInstance
}

// RegisterServicesToNacos provides a generic registration capability for multiple instances.
// It returns a cleanup function that deregisters all successfully registered instances.
func RegisterServicesToNacos(param RegisterServicesParam) (func(), error) {
	if err := validateNacosConfig(param.NacosConf); err != nil {
		return nil, err
	}
	if len(param.Instances) == 0 {
		return nil, fmt.Errorf("nacos register instances is empty")
	}

	namingClient, err := GetNacosNamingClient(param.NacosConf, param.NamespaceID)
	if err != nil {
		return nil, err
	}

	cleanupFns := make([]func(), 0, len(param.Instances))
	for _, inst := range param.Instances {
		if strings.TrimSpace(inst.ServiceName) == "" {
			cleanupRegistered(cleanupFns)
			return nil, fmt.Errorf("service name is required")
		}
		if inst.Port == 0 {
			cleanupRegistered(cleanupFns)
			return nil, fmt.Errorf("service port is required")
		}

		groupName := inst.GroupName
		if groupName == "" {
			groupName = DefaultNacosGroup
		}
		clusterName := inst.ClusterName
		if clusterName == "" {
			clusterName = DefaultNacosCluster
		}
		weight := inst.Weight
		if weight <= 0 {
			weight = 1
		}

		registerParam := vo.RegisterInstanceParam{
			Ip:          inst.IP,
			Port:        inst.Port,
			Weight:      weight,
			Enable:      inst.Enable,
			Healthy:     inst.Healthy,
			Metadata:    inst.Metadata,
			ClusterName: clusterName,
			ServiceName: inst.ServiceName,
			GroupName:   groupName,
			Ephemeral:   inst.Ephemeral,
		}

		ok, registerErr := namingClient.RegisterInstance(registerParam)
		if registerErr != nil {
			cleanupRegistered(cleanupFns)
			return nil, fmt.Errorf("register instance failed service=%s ip=%s port=%d: %w", inst.ServiceName, inst.IP, inst.Port, registerErr)
		}
		if !ok {
			cleanupRegistered(cleanupFns)
			return nil, fmt.Errorf("register instance failed service=%s ip=%s port=%d: sdk returned false", inst.ServiceName, inst.IP, inst.Port)
		}

		cleanupFns = append(cleanupFns, func() {
			deregisterParam := vo.DeregisterInstanceParam{
				Ip:          inst.IP,
				Port:        inst.Port,
				Cluster:     clusterName,
				ServiceName: inst.ServiceName,
				GroupName:   groupName,
				Ephemeral:   inst.Ephemeral,
			}
			_, _ = namingClient.DeregisterInstance(deregisterParam)
		})
	}

	return func() {
		cleanupRegistered(cleanupFns)
	}, nil
}

func cleanupRegistered(cleanupFns []func()) {
	for i := len(cleanupFns) - 1; i >= 0; i-- {
		cleanupFns[i]()
	}
}

// GetNacosNamingClient returns a reusable naming client for the given namespace.
// Clients are cached for the process lifetime and shared by service registration and gRPC discovery.
func GetNacosNamingClient(nacosConf *PkgNacosConfig, namespaceID string) (naming_client.INamingClient, error) {
	if err := validateNacosConfig(nacosConf); err != nil {
		return nil, err
	}

	namingClientMu.RLock()
	client, ok := namingClients[namespaceID]
	namingClientMu.RUnlock()
	if ok {
		return client, nil
	}

	created, err := newNacosNamingClient(nacosConf, namespaceID)
	if err != nil {
		return nil, err
	}

	namingClientMu.Lock()
	defer namingClientMu.Unlock()
	if existing, exists := namingClients[namespaceID]; exists {
		created.CloseClient()
		return existing, nil
	}
	namingClientConf = nacosConf
	namingClients[namespaceID] = created
	return created, nil
}

func newNacosNamingClient(nacosConf *PkgNacosConfig, namespaceID string) (naming_client.INamingClient, error) {
	serverConfigs := make([]constant.ServerConfig, 0, len(nacosConf.Servers))
	for _, srv := range nacosConf.Servers {
		serverConfigs = append(serverConfigs, *constant.NewServerConfig(srv.Addr, srv.Port))
	}

	if len(serverConfigs) == 0 {
		return nil, fmt.Errorf("no nacos servers configured")
	}

	clientConfig := constant.ClientConfig{
		NamespaceId:         namespaceID,
		Username:            nacosConf.Username,
		Password:            nacosConf.Password,
		LogDir:              nacosConf.LogDir,
		CacheDir:            nacosConf.CacheDir,
		LogLevel:            nacosConf.LogLevel,
		NotLoadCacheAtStart: true,
	}

	client, err := clients.NewNamingClient(vo.NacosClientParam{
		ClientConfig:  &clientConfig,
		ServerConfigs: serverConfigs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create nacos naming client: %w", err)
	}

	return client, nil
}

// ResolveRegisterIP resolves the IP address to register with Nacos.
// Priority: explicit IP, CIDR match, interface name, default route, then first usable IPv4.
func ResolveRegisterIP() (string, error) {
	if ip := strings.TrimSpace(os.Getenv("NACOS_REGISTER_IP")); ip != "" {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return "", fmt.Errorf("invalid NACOS_REGISTER_IP: %s", ip)
		}
		return ip, nil
	}

	if cidr := strings.TrimSpace(os.Getenv("NACOS_REGISTER_CIDR")); cidr != "" {
		ip, err := resolveRegisterIPByCIDR(cidr)
		if err != nil {
			return "", err
		}
		return ip, nil
	}

	if ifaceName := strings.TrimSpace(os.Getenv("NACOS_REGISTER_INTERFACE")); ifaceName != "" {
		ip, err := resolveRegisterIPByInterface(ifaceName)
		if err != nil {
			return "", err
		}
		return ip, nil
	}

	if ip, err := resolveDefaultRouteIP(); err == nil {
		return ip, nil
	}

	return resolveFirstNonLoopbackIPv4()
}

// resolveRegisterIPByCIDR returns the first local IPv4 address contained in the given CIDR.
func resolveRegisterIPByCIDR(cidr string) (string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("invalid NACOS_REGISTER_CIDR: %s", cidr)
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		ip, ok := addrToUsableIPv4(addr)
		if !ok {
			continue
		}
		if ipNet.Contains(ip) {
			return ip.String(), nil
		}
	}

	return "", fmt.Errorf("no local ipv4 address found in NACOS_REGISTER_CIDR: %s", cidr)
}

// resolveRegisterIPByInterface returns the first usable IPv4 address on the named interface.
func resolveRegisterIPByInterface(ifaceName string) (string, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return "", fmt.Errorf("NACOS_REGISTER_INTERFACE %s not found: %w", ifaceName, err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		ip, ok := addrToUsableIPv4(addr)
		if ok {
			return ip.String(), nil
		}
	}

	return "", fmt.Errorf("no usable ipv4 address found on NACOS_REGISTER_INTERFACE: %s", ifaceName)
}

// resolveDefaultRouteIP returns the local IPv4 selected by the system default route.
func resolveDefaultRouteIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if localAddr, ok := conn.LocalAddr().(*net.UDPAddr); ok && localAddr.IP != nil {
		if ipv4 := localAddr.IP.To4(); ipv4 != nil && !ipv4.IsLoopback() && !ipv4.IsLinkLocalUnicast() {
			return ipv4.String(), nil
		}
	}
	return "", fmt.Errorf("default route did not return a usable ipv4 address")
}

// resolveFirstNonLoopbackIPv4 returns the first usable local IPv4 as a final fallback.
func resolveFirstNonLoopbackIPv4() (string, error) {
	addrs, ifErr := net.InterfaceAddrs()
	if ifErr != nil {
		return "", ifErr
	}
	for _, addr := range addrs {
		ip, ok := addrToUsableIPv4(addr)
		if ok {
			return ip.String(), nil
		}
	}

	return "", fmt.Errorf("no non-loopback ipv4 address found")
}

// addrToUsableIPv4 extracts a non-loopback, non-link-local IPv4 address.
func addrToUsableIPv4(addr net.Addr) (net.IP, bool) {
	ipNet, ok := addr.(*net.IPNet)
	if !ok || ipNet.IP == nil {
		return nil, false
	}
	ipv4 := ipNet.IP.To4()
	if ipv4 == nil || ipv4.IsLoopback() || ipv4.IsLinkLocalUnicast() {
		return nil, false
	}
	return ipv4, true
}
