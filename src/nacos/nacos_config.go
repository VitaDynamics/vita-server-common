package nacos

// nacos配置中心需要的配置
type PkgNacosConfig struct {
	Enabled  bool                   `yaml:"enabled"`
	Servers  []PkgNacosServerConfig `yaml:"servers"`
	Username string                 `yaml:"username"`
	Password string                 `yaml:"password"`
	LogDir   string                 `yaml:"log_dir"`
	CacheDir string                 `yaml:"cache_dir"`
	LogLevel string                 `yaml:"log_level"`
}

// NacosServerConfig represents a single Nacos server endpoint in a cluster.
type PkgNacosServerConfig struct {
	Addr string `yaml:"addr"`
	Port uint64 `yaml:"port"`
}
