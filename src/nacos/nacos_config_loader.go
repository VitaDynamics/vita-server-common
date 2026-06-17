package nacos

import (
	"fmt"
	"sync"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

var (
	nacosClientMu      sync.RWMutex
	nacosClientConf    *PkgNacosConfig
	nacosConfigClients = make(map[string]config_client.IConfigClient)
)

// InitNacosConfigClient initializes the global nacos config client manager.
// It eagerly creates the default-namespace client and enables lazy reuse for other namespaces.
func InitNacosConfigClient(nacosConf *PkgNacosConfig, namespace string) config_client.IConfigClient {
	if err := validateNacosConfig(nacosConf); err != nil {
		return nil
	}

	client, err := newNacosConfigClient(nacosConf, namespace)
	if err != nil {
		panic(fmt.Sprintf("failed to create default nacos client for namespace '%s': %v", namespace, err))
	}
	return client
}

// GetNacosConfigClient returns a reusable global nacos config client for the given namespace.
func GetNacosConfigClient(namespaceID string) (config_client.IConfigClient, error) {
	nacosClientMu.RLock()
	nacosConf := nacosClientConf
	client, ok := nacosConfigClients[namespaceID]
	nacosClientMu.RUnlock()

	if ok {
		return client, nil
	}

	if nacosConf == nil || !nacosConf.Enabled {
		return nil, fmt.Errorf("nacos client is not initialized")
	}

	created, err := newNacosConfigClient(nacosConf, namespaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to create nacos client: %w", err)
	}

	nacosClientMu.Lock()
	if existing, exists := nacosConfigClients[namespaceID]; exists {
		nacosClientMu.Unlock()
		return existing, nil
	}
	nacosConfigClients[namespaceID] = created
	nacosClientMu.Unlock()

	return created, nil
}

type ConfigParam struct {
	DataId string
	Group  string
}

// 根据传入的client，dataId和group从Nacos获取配置内容，返回原始字符串形式的配置内容。如果Nacos未启用或获取失败，则返回错误。
// Returns the config content as raw string. Returns error if Nacos is disabled or fetch fails.
func GetNacosConfig(client config_client.IConfigClient, param ConfigParam) (string, error) {
	if client == nil {
		return "", fmt.Errorf("nacos client is not initialized")
	}
	if param.Group == "" {
		param.Group = "DEFAULT_GROUP"
	}

	content, err := client.GetConfig(vo.ConfigParam{
		DataId: param.DataId,
		Group:  param.Group,
	})
	if err != nil {
		return "", fmt.Errorf("failed to get config (group=%s dataId=%s): %w", param.Group, param.DataId, err)
	}

	if content == "" {
		return "", fmt.Errorf("config is empty (group=%s dataId=%s)", param.Group, param.DataId)
	}

	return content, nil
}

// WatchNacosConfig registers a listener on a single Nacos config item.
// namespaceID: Nacos namespace ID (leave empty for public namespace)
// group: Nacos config group (leave empty to use DEFAULT_GROUP)
// dataID: Nacos config data ID
// onChange: callback function called when config changes, receives the new content
//
// Returns error if Nacos is disabled or listener registration fails.
// The listener runs in the background until the application stops.
func WatchNacosConfig(client config_client.IConfigClient, namespaceID, group, dataID string,
	onChange func(content string)) error {

	if client == nil {
		return fmt.Errorf("nacos client is not initialized")
	}
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	return client.ListenConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  group,
		OnChange: func(namespace, group, dataId, data string) {
			if onChange != nil {
				onChange(data)
			}
		},
	})
}

func validateNacosConfig(nacosConf *PkgNacosConfig) error {
	if nacosConf == nil || !nacosConf.Enabled {
		return fmt.Errorf("nacos is not enabled")
	}

	if len(nacosConf.Servers) == 0 {
		return fmt.Errorf("nacos servers not configured")
	}

	return nil
}

func newNacosConfigClient(nacosConf *PkgNacosConfig, namespaceID string) (config_client.IConfigClient, error) {
	// Convert servers to SDK format
	serverConfigs := make([]constant.ServerConfig, 0, len(nacosConf.Servers))
	for _, srv := range nacosConf.Servers {
		serverConfigs = append(serverConfigs, *constant.NewServerConfig(srv.Addr, srv.Port))
	}

	if len(serverConfigs) == 0 {
		return nil, fmt.Errorf("no nacos servers configured")
	}

	cc := constant.ClientConfig{
		NamespaceId:         namespaceID,
		Username:            nacosConf.Username,
		Password:            nacosConf.Password,
		LogDir:              nacosConf.LogDir,
		CacheDir:            nacosConf.CacheDir,
		LogLevel:            nacosConf.LogLevel,
		NotLoadCacheAtStart: true, //服务启动时不从本地加载缓存的配置，避免旧配置覆盖新配置
	}

	client, err := clients.NewConfigClient(vo.NacosClientParam{
		ClientConfig:  &cc,
		ServerConfigs: serverConfigs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create nacos config client: %w", err)
	}

	return client, nil
}
