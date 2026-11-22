package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultNpmRegistry  = "https://registry.npmmirror.com"
	nasNetworkProxyPort = 7890
	nasNetworkProxyAddr = "127.0.0.1:7890"
)

var proxyEnvNames = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "npm_config_proxy", "npm_config_https_proxy", "npm_config_noproxy", "NO_PROXY", "no_proxy", "NODE_USE_ENV_PROXY"}

type Config struct {
	ServerPort      int    `json:"server_port"`
	ProxyPort       int    `json:"proxy_port"`
	HeapMemoryLimit int    `json:"heap_memory_limit,omitempty"`
	NetworkProxy    string `json:"network_proxy"`
	ProxyRuntime    bool   `json:"proxy_runtime"`
	NpmRegistry     string `json:"npm_registry"`
	UpdateChannel   string `json:"update_channel"`
	AccessMode      string `json:"access_mode"`
	ReverseProxyURL string `json:"reverse_proxy_url"`
	AccessPassword  string `json:"access_password"`
	DataLibraryPath string `json:"data_library_path"`
	Version         string `json:"version,omitempty"`
	BuildTime       string `json:"build_time,omitempty"`
	LastRunState    string `json:"last_run_state,omitempty"`
}

type ConfigResponse struct {
	Config
	Revision string `json:"revision"`
}

func configRevision(cfg Config) string {
	cfg.Version, cfg.BuildTime, cfg.LastRunState, cfg.DataLibraryPath = "", "", "", ""
	data, _ := json.Marshal(cfg)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func configResponse() ConfigResponse {
	cfg := GetConfig()
	return ConfigResponse{Config: cfg, Revision: configRevision(cfg)}
}

func (c Config) GetServerPort() int {
	if c.ServerPort <= 0 {
		return 3000
	}
	return c.ServerPort
}

func (c Config) GetProxyPort() int {
	if c.ProxyPort <= 0 {
		return 3001
	}
	return c.ProxyPort
}

func (c Config) allowsRemoteProxy() bool {
	return strings.TrimSpace(c.AccessPassword) != ""
}

func (c Config) proxyListenAddress() string {
	host := "127.0.0.1"
	if c.allowsRemoteProxy() {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(c.GetProxyPort()))
}

func (c Config) GetNpmRegistry() string {
	if value := strings.TrimSpace(c.NpmRegistry); value != "" {
		return value
	}
	return DefaultNpmRegistry
}

func (c Config) GetUpdateChannel() string {
	if c.UpdateChannel == "" {
		return "latest"
	}
	return c.UpdateChannel
}

var (
	globalConfig   Config
	configMu       sync.RWMutex
	configFilePath string
)

func InitConfig() {
	configFilePath = filepath.Join(globalPkgVar, "config.json")
	cfg := Config{ServerPort: 3000, ProxyPort: 3001, AccessMode: "fngateway", NpmRegistry: DefaultNpmRegistry, UpdateChannel: "latest"}
	if data, err := os.ReadFile(configFilePath); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			LogFatal("[配置] 配置格式无效: %s", err)
		}
	} else if !os.IsNotExist(err) {
		LogFatal("[配置] 无法读取配置: %s", err)
	}
	if err := validateConfig(cfg); err != nil {
		LogFatal("[配置] 配置校验失败: %s", err)
	}
	cfg.DataLibraryPath = globalPkgVar
	configMu.Lock()
	globalConfig = cfg
	configMu.Unlock()
}

// InitAppEnv 将运行数据与缓存隔离在本应用目录。
func InitAppEnv() {
	paths := map[string]string{
		"HOME":                 globalHomeDir,
		"XDG_CONFIG_HOME":      filepath.Join(globalPkgVar, "config"),
		"XDG_DATA_HOME":        filepath.Join(globalPkgVar, "data"),
		"XDG_STATE_HOME":       filepath.Join(globalPkgVar, "state"),
		"XDG_CACHE_HOME":       filepath.Join(globalPkgVar, "cache"),
		"OPENCHAMBER_DATA_DIR": globalOpenChamberHome,
		"npm_config_cache":     globalNpmCache,
	}
	for key, path := range paths {
		if err := os.MkdirAll(path, 0700); err != nil {
			LogFatal("[环境] 创建目录失败 %s: %s", path, err)
		}
		if err := os.Setenv(key, path); err != nil {
			LogFatal("[环境] 设置 %s 失败: %s", key, err)
		}
	}
	_ = os.Setenv("PATH", filepath.Join(runtimeDir, "node_modules", ".bin")+":"+nodeBinDir+":"+os.Getenv("PATH"))
	_ = os.Setenv("CI", "true")
	_ = os.Setenv("OPENCODE_BINARY", opencodeBin())
	ApplyNpmRegistryEnv()
}

func ApplyNpmRegistryEnv() {
	_ = os.Setenv("npm_config_registry", GetConfig().GetNpmRegistry())
}

func (c Config) usesOfficialRegistry() bool {
	u, err := url.Parse(c.GetNpmRegistry())
	return err == nil && strings.EqualFold(u.Hostname(), "registry.npmjs.org")
}

func (c Config) resolveNetworkProxy(nasProxyAvailable func() bool) string {
	if proxy := strings.TrimSpace(c.NetworkProxy); proxy != "" {
		return proxy
	}
	if c.GetServerPort() == nasNetworkProxyPort || c.GetProxyPort() == nasNetworkProxyPort {
		return ""
	}
	if nasProxyAvailable() {
		return "http://" + nasNetworkProxyAddr
	}
	return ""
}

func (c Config) networkProxyURL() string {
	return c.resolveNetworkProxy(func() bool {
		conn, err := net.DialTimeout("tcp4", nasNetworkProxyAddr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
}

func (c Config) outboundTransport(allowProxy bool) (*http.Transport, error) {
	transport := &http.Transport{}
	if allowProxy {
		if proxy := c.networkProxyURL(); proxy != "" {
			proxyURL, err := url.Parse(proxy)
			if err != nil {
				return nil, fmt.Errorf("网络代理地址无效")
			}
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return transport, nil
}

func (c Config) proxyBypass(proxy string) string {
	hosts := "localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,.local,registry.npmmirror.com,npmmirror.com"
	if !c.usesOfficialRegistry() {
		if u, err := url.Parse(c.GetNpmRegistry()); err == nil && u.Hostname() != "" {
			hosts += "," + u.Hostname()
		}
	}
	if u, err := url.Parse(proxy); err == nil && u.Hostname() != "" && u.Hostname() != "127.0.0.1" {
		hosts += "," + u.Hostname()
	}
	return hosts
}

func withoutProxyEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		isProxy := false
		for _, name := range proxyEnvNames {
			isProxy = isProxy || strings.EqualFold(key, name)
		}
		if !isProxy {
			env = append(env, entry)
		}
	}
	return env
}

func networkProxyEnv(cfg Config, isEnabled bool) []string {
	proxy := ""
	if isEnabled {
		proxy = cfg.networkProxyURL()
	}
	return proxyEnv(cfg, proxy)
}

func proxyEnv(cfg Config, proxy string) []string {
	bypass := cfg.proxyBypass(proxy)
	env := []string{"NO_PROXY=" + bypass, "no_proxy=" + bypass, "npm_config_noproxy=" + bypass}
	if proxy == "" {
		return env
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "npm_config_proxy", "npm_config_https_proxy"} {
		env = append(env, key+"="+proxy)
	}
	if u, err := url.Parse(proxy); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		env = append(env, "NODE_USE_ENV_PROXY=1")
	}
	return env
}

// runtimeEnv 仅向应用子进程应用运行时网络代理。
func runtimeEnv() []string {
	cfg := GetConfig()
	env := append(withoutProxyEnv(), networkProxyEnv(cfg, cfg.ProxyRuntime)...)
	if cfg.HeapMemoryLimit > 0 {
		env = append(env, fmt.Sprintf("NODE_OPTIONS=--max-old-space-size=%d", cfg.HeapMemoryLimit*1024))
	}
	return env
}

func validateConfig(cfg Config) error {
	if channel := cfg.GetUpdateChannel(); channel != "latest" && channel != "canary" {
		return fmt.Errorf("更新渠道必须为 latest 或 canary")
	}
	if cfg.ServerPort < 1 || cfg.ServerPort > 65535 || cfg.ProxyPort < 1 || cfg.ProxyPort > 65535 {
		return fmt.Errorf("端口号必须在 1 ~ 65535 之间")
	}
	if cfg.ServerPort == cfg.ProxyPort {
		return fmt.Errorf("内部监听端口与反向代理端口不能相同")
	}
	if cfg.HeapMemoryLimit < 0 || cfg.HeapMemoryLimit > 128 {
		return fmt.Errorf("堆内存上限必须在 0 ~ 128 GB 之间")
	}
	if cfg.AccessMode != "fngateway" && cfg.AccessMode != "port" && cfg.AccessMode != "custom" {
		return fmt.Errorf("打开方式无效")
	}
	for _, entry := range []struct{ value, label string }{{cfg.GetNpmRegistry(), "NPM 依赖源"}, {cfg.ReverseProxyURL, "外部访问地址"}} {
		if entry.value == "" {
			continue
		}
		u, err := url.Parse(entry.value)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("%s必须是有效的 HTTP 或 HTTPS 地址", entry.label)
		}
	}
	if proxy := strings.TrimSpace(cfg.NetworkProxy); proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" ||
			(u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
			return fmt.Errorf("网络代理地址必须是有效的 HTTP、HTTPS 或 SOCKS5 地址")
		}
		if cfg.ProxyRuntime && (u.Scheme == "socks5" || u.Scheme == "socks5h") {
			return fmt.Errorf("SOCKS5 可用于 npm 官方源部署；服务进程代理需要 HTTP 或 HTTPS 地址，请使用代理软件的 HTTP 或混合端口")
		}
	}
	if cfg.AccessMode == "custom" && cfg.ReverseProxyURL == "" {
		return fmt.Errorf("请填写自定义外部访问地址")
	}
	return nil
}

func GetConfig() Config       { configMu.RLock(); defer configMu.RUnlock(); return globalConfig }
func GetBuildTime() string    { return GetConfig().BuildTime }
func GetVersion() string      { return GetConfig().Version }
func GetLastRunState() string { return GetConfig().LastRunState }

func persistConfigLocked(cfg Config) error {
	return writeJSONFile(configFilePath, cfg)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncSnapshotDirectory(filepath.Dir(path))
}

func updateConfigValue(update func(*Config)) {
	configMu.Lock()
	defer configMu.Unlock()
	update(&globalConfig)
	if err := persistConfigLocked(globalConfig); err != nil {
		LogWarning("[配置] 保存失败: %s", err)
	}
}

func SetBuildTime(t time.Time) {
	updateConfigValue(func(c *Config) { c.BuildTime = t.Format("2006-01-02 15:04") })
}
func SetVersion(v string)      { updateConfigValue(func(c *Config) { c.Version = v }) }
func SetLastRunState(s string) { updateConfigValue(func(c *Config) { c.LastRunState = s }) }

func SaveConfig(cfg Config) error {
	configMu.Lock()
	defer configMu.Unlock()
	cfg.LastRunState = globalConfig.LastRunState
	cfg.Version, cfg.BuildTime = globalConfig.Version, globalConfig.BuildTime
	cfg.DataLibraryPath = globalPkgVar
	if err := persistConfigLocked(cfg); err != nil {
		return err
	}
	globalConfig = cfg
	return nil
}
