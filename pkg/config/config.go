// Package config 提供 YAML 配置管理与热加载
//
// 设计原则：
// - 基于 fsnotify 实现文件变更监听
// - 原子替换配置指针，读取无锁
// - 支持路由规则、插件配置、服务发现
package config

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 支持 YAML 字符串格式的 time.Duration（如 "5s", "30s"）
type Duration struct {
	time.Duration
}

// UnmarshalYAML 实现 yaml.Unmarshaler 接口
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = dur
	return nil
}

// MarshalYAML 实现 yaml.Marshaler 接口
func (d Duration) MarshalYAML() (interface{}, error) {
	return d.String(), nil
}

// Config 网关配置
type Config struct {
	// Server 服务配置
	Server ServerConfig `yaml:"server"`
	// Routes 路由规则列表
	Routes []RouteConfig `yaml:"routes"`
	// Plugins 插件配置
	Plugins PluginConfig `yaml:"plugins"`
}

// ServerConfig 服务配置
type ServerConfig struct {
	// Host 监听地址
	Host string `yaml:"host"`
	// Port 监听端口
	Port int `yaml:"port"`
	// ReadTimeout 读取超时（如 "10s", "5m"）
	ReadTimeout Duration `yaml:"read_timeout"`
	// WriteTimeout 写入超时（如 "30s", "1m"）
	WriteTimeout Duration `yaml:"write_timeout"`
	// ShutdownTimeout 优雅关闭超时（如 "5s"）
	ShutdownTimeout Duration `yaml:"shutdown_timeout"`
}

// RouteConfig 路由规则配置
type RouteConfig struct {
	// Method HTTP 方法
	Method string `yaml:"method"`
	// Path 路径
	Path string `yaml:"path"`
	// Backend 后端服务地址
	Backend string `yaml:"backend"`
	// Weight 权重
	Weight int `yaml:"weight"`
	// Plugins 该路由启用的插件列表
	Plugins []string `yaml:"plugins"`
}

// PluginConfig 插件配置
type PluginConfig struct {
	// Auth 鉴权插件配置
	Auth AuthConfig `yaml:"auth"`
	// RateLimit 限流插件配置
	RateLimit RateLimitConfig `yaml:"rate_limit"`
}

// AuthConfig 鉴权配置
type AuthConfig struct {
	// Secret JWT 密钥
	Secret string `yaml:"secret"`
	// Enabled 是否启用
	Enabled bool `yaml:"enabled"`
}

// RateLimitConfig 限流配置
type RateLimitConfig struct {
	// Rate 每秒请求数
	Rate int64 `yaml:"rate"`
	// Capacity 桶容量
	Capacity int64 `yaml:"capacity"`
	// Enabled 是否启用
	Enabled bool `yaml:"enabled"`
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host:            "0.0.0.0",
			Port:            8080,
			ReadTimeout:     Duration{Duration: 10 * time.Second},
			WriteTimeout:    Duration{Duration: 30 * time.Second},
			ShutdownTimeout: Duration{Duration: 5 * time.Second},
		},
		Routes: []RouteConfig{
			{
				Method:  "GET",
				Path:    "/health",
				Backend: "http://127.0.0.1:8080",
				Weight:  1,
			},
		},
		Plugins: PluginConfig{
			Auth: AuthConfig{
				Secret:  "default-secret",
				Enabled: false,
			},
			RateLimit: RateLimitConfig{
				Rate:     1000,
				Capacity: 100,
				Enabled:  false,
			},
		},
	}
}

// LoadFromFile 从 YAML 文件加载配置
func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file failed: %w", err)
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file failed: %w", err)
	}

	return cfg, nil
}

// SaveToFile 保存配置到 YAML 文件
func SaveToFile(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config failed: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config file failed: %w", err)
	}

	return nil
}

// Watcher 配置热加载监听器
type Watcher struct {
	// configPath 配置文件路径
	configPath string
	// current 当前配置（原子指针，无锁读取）
	current atomic.Value
	// onChange 配置变更回调
	onChange func(*Config)
	// stopCh 停止信号
	stopCh chan struct{}
	// stopped 是否已停止（防止重复关闭 channel）
	stopped bool
}

// NewWatcher 创建配置监听器
func NewWatcher(configPath string, onChange func(*Config)) (*Watcher, error) {
	cfg, err := LoadFromFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("load initial config failed: %w", err)
	}

	w := &Watcher{
		configPath: configPath,
		onChange:   onChange,
		stopCh:     make(chan struct{}),
	}
	w.current.Store(cfg)

	return w, nil
}

// Get 获取当前配置（无锁读取）
func (w *Watcher) Get() *Config {
	return w.current.Load().(*Config)
}

// Start 启动配置监听（轮询模式，无需 fsnotify 依赖）
func (w *Watcher) Start() {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		var lastModTime time.Time

		for {
			select {
			case <-ticker.C:
				info, err := os.Stat(w.configPath)
				if err != nil {
					continue
				}

				// 检查文件修改时间
				if info.ModTime().After(lastModTime) {
					lastModTime = info.ModTime()

					newCfg, err := LoadFromFile(w.configPath)
					if err != nil {
						continue
					}

					w.current.Store(newCfg)
					if w.onChange != nil {
						w.onChange(newCfg)
					}
				}

			case <-w.stopCh:
				return
			}
		}
	}()
}

// Stop 停止配置监听（可安全多次调用）
func (w *Watcher) Stop() {
	if !w.stopped {
		w.stopped = true
		close(w.stopCh)
	}
}
