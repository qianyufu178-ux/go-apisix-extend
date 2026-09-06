package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ==================== 🔴 TDD: 基础功能测试 ====================

// TestDefaultConfig 测试默认配置
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig() should not return nil")
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("default port = %d, want 8080", cfg.Server.Port)
	}
	if len(cfg.Routes) != 1 {
		t.Errorf("default routes count = %d, want 1", len(cfg.Routes))
	}
}

// TestLoadFromFile 测试从文件加载配置
func TestLoadFromFile(t *testing.T) {
	// 创建临时配置文件
	content := `
server:
  host: "127.0.0.1"
  port: 9090
  read_timeout: 5s
  write_timeout: 10s

routes:
  - method: "GET"
    path: "/api/v1/users"
    backend: "http://user-service:8080"
    weight: 1
    plugins: ["auth", "rate_limit"]

plugins:
  auth:
    secret: "my-secret-key"
    enabled: true
  rate_limit:
    rate: 500
    capacity: 50
    enabled: true
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write temp config failed: %v", err)
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatalf("LoadFromFile() failed: %v", err)
	}

	// 验证配置
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("host = %s, want 127.0.0.1", cfg.Server.Host)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("port = %d, want 9090", cfg.Server.Port)
	}
	if cfg.Server.ReadTimeout.Duration != 5*time.Second {
		t.Errorf("read_timeout = %v, want 5s", cfg.Server.ReadTimeout)
	}

	if len(cfg.Routes) != 1 {
		t.Fatalf("routes count = %d, want 1", len(cfg.Routes))
	}
	if cfg.Routes[0].Method != "GET" {
		t.Errorf("route method = %s, want GET", cfg.Routes[0].Method)
	}
	if cfg.Routes[0].Backend != "http://user-service:8080" {
		t.Errorf("route backend = %s, want http://user-service:8080", cfg.Routes[0].Backend)
	}

	if !cfg.Plugins.Auth.Enabled {
		t.Error("auth should be enabled")
	}
	if cfg.Plugins.Auth.Secret != "my-secret-key" {
		t.Errorf("auth secret = %s, want my-secret-key", cfg.Plugins.Auth.Secret)
	}
	if !cfg.Plugins.RateLimit.Enabled {
		t.Error("rate_limit should be enabled")
	}
	if cfg.Plugins.RateLimit.Rate != 500 {
		t.Errorf("rate_limit rate = %d, want 500", cfg.Plugins.RateLimit.Rate)
	}
}

// TestLoadFromFile_NotFound 测试加载不存在的文件
func TestLoadFromFile_NotFound(t *testing.T) {
	_, err := LoadFromFile("/path/to/nonexistent/config.yaml")
	if err == nil {
		t.Error("LoadFromFile() should return error for non-existent file")
	}
}

// TestSaveToFile 测试保存配置到文件
func TestSaveToFile(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Server.Port = 9090

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	if err := SaveToFile(configPath, cfg); err != nil {
		t.Fatalf("SaveToFile() failed: %v", err)
	}

	// 重新加载验证
	loaded, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatalf("reload config failed: %v", err)
	}

	if loaded.Server.Port != 9090 {
		t.Errorf("port = %d, want 9090", loaded.Server.Port)
	}
}

// ==================== 🔴 TDD: Watcher 测试 ====================

// TestNewWatcher 测试创建配置监听器
func TestNewWatcher(t *testing.T) {
	content := `
server:
  host: "0.0.0.0"
  port: 8080
routes:
  - method: "GET"
    path: "/health"
    backend: "http://localhost:8080"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write temp config failed: %v", err)
	}

	w, err := NewWatcher(configPath, func(cfg *Config) {
		// onChange callback
	})
	if err != nil {
		t.Fatalf("NewWatcher() failed: %v", err)
	}
	defer w.Stop()

	// 验证初始配置
	cfg := w.Get()
	if cfg == nil {
		t.Fatal("Get() should not return nil")
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("port = %d, want 8080", cfg.Server.Port)
	}
}

// TestWatcher_Get 测试无锁读取配置
func TestWatcher_Get(t *testing.T) {
	content := `server:
  host: "0.0.0.0"
  port: 8080
routes:
  - method: "GET"
    path: "/health"
    backend: "http://localhost:8080"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write temp config failed: %v", err)
	}

	w, err := NewWatcher(configPath, nil)
	if err != nil {
		t.Fatalf("NewWatcher() failed: %v", err)
	}
	defer w.Stop()

	// 多次读取验证无锁安全
	for i := 0; i < 100; i++ {
		cfg := w.Get()
		if cfg == nil {
			t.Fatal("Get() should not return nil")
		}
	}
}

// TestWatcher_StartStop 测试启动停止
func TestWatcher_StartStop(t *testing.T) {
	content := `server:
  host: "0.0.0.0"
  port: 8080
routes:
  - method: "GET"
    path: "/health"
    backend: "http://localhost:8080"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write temp config failed: %v", err)
	}

	w, err := NewWatcher(configPath, nil)
	if err != nil {
		t.Fatalf("NewWatcher() failed: %v", err)
	}

	w.Start()
	w.Stop()
	// 不 panic 即通过
}
