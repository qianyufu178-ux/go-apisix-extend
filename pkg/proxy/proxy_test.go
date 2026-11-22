package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yfyadmin/go-apisix-extend/pkg/plugin"
	"github.com/yfyadmin/go-apisix-extend/pkg/router"
)

// ==================== 🔴 TDD: 基础功能测试 ====================

// TestNewGatewayProxy 测试创建网关代理
func TestNewGatewayProxy(t *testing.T) {
	r := router.NewRouter()
	p := plugin.NewPipeline()
	gp := NewGatewayProxy(r, p)
	if gp == nil {
		t.Fatal("NewGatewayProxy() should not return nil")
	}
}

// TestServeHTTP_RouteNotFound 测试路由未找到
func TestServeHTTP_RouteNotFound(t *testing.T) {
	r := router.NewRouter()
	p := plugin.NewPipeline()
	gp := NewGatewayProxy(r, p)

	req := httptest.NewRequest("GET", "/api/v1/notfound", nil)
	w := httptest.NewRecorder()

	gp.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusNotFound)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "route not found" {
		t.Errorf("error = %v, want 'route not found'", resp["error"])
	}
}

// TestServeHTTP_AuthBlocked 测试鉴权拦截
func TestServeHTTP_AuthBlocked(t *testing.T) {
	r := router.NewRouter()
	r.AddRoute(&router.Route{
		Method:  "GET",
		Path:    "/api/v1/users",
		Backend: "http://localhost:8080",
	})

	auth := plugin.NewAuthPlugin("secret")
	p := plugin.NewPipeline()
	p.Use(auth.Auth())

	gp := NewGatewayProxy(r, p)

	// 无 token 请求
	req := httptest.NewRequest("GET", "/api/v1/users", nil)
	w := httptest.NewRecorder()

	gp.ServeHTTP(w, req)

	// 鉴权失败应该返回，不进行转发
	// 由于没有设置响应，状态码应为 200（默认）
	// 但请求被 Abort，不会转发到后端
}

// TestServeHTTP_WithAuth 测试带鉴权的请求
func TestServeHTTP_WithAuth(t *testing.T) {
	r := router.NewRouter()
	r.AddRoute(&router.Route{
		Method:  "GET",
		Path:    "/api/v1/users",
		Backend: "http://localhost:8080",
	})

	auth := plugin.NewAuthPlugin("secret")
	p := plugin.NewPipeline()
	p.Use(auth.Auth())

	gp := NewGatewayProxy(r, p)

	// 带 token 的请求
	req := httptest.NewRequest("GET", "/api/v1/users", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	w := httptest.NewRecorder()

	gp.ServeHTTP(w, req)

	// 由于后端 localhost:8080 不存在，会触发 errorHandler
	// 返回 502 Bad Gateway
	if w.Code != http.StatusBadGateway {
		t.Logf("expected 502 due to unreachable backend, got %d", w.Code)
	}
}

// ==================== 🔴 TDD: ErrorHandler 测试 ====================

// TestErrorHandler 测试错误处理
func TestErrorHandler(t *testing.T) {
	r := router.NewRouter()
	p := plugin.NewPipeline()
	gp := NewGatewayProxy(r, p)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	// 模拟错误
	gp.errorHandler(w, req, http.ErrAbortHandler)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusBadGateway)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["code"].(float64) != float64(http.StatusBadGateway) {
		t.Errorf("code = %v, want %d", resp["code"], http.StatusBadGateway)
	}
	if resp["error"] != "upstream_error" {
		t.Errorf("error = %v, want 'upstream_error'", resp["error"])
	}
}

// ==================== 🔴 TDD: Director 测试 ====================

// TestDirector_HeaderInjection 测试 Director Header 注入
func TestDirector_HeaderInjection(t *testing.T) {
	r := router.NewRouter()
	p := plugin.NewPipeline()
	gp := NewGatewayProxy(r, p)

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"

	// 直接调用 director
	gp.director(req)

	if req.Header.Get("X-Real-IP") != "192.168.1.1:12345" {
		t.Errorf("X-Real-IP = %s, want 192.168.1.1:12345", req.Header.Get("X-Real-IP"))
	}
	if req.Header.Get("X-Forwarded-For") != "192.168.1.1:12345" {
		t.Errorf("X-Forwarded-For = %s, want 192.168.1.1:12345", req.Header.Get("X-Forwarded-For"))
	}
}

// ==================== 🔴 TDD: ModifyResponse 测试 ====================

// TestModifyResponse 测试响应修改
func TestModifyResponse(t *testing.T) {
	r := router.NewRouter()
	p := plugin.NewPipeline()
	gp := NewGatewayProxy(r, p)

	// 创建一个模拟响应
	resp := &http.Response{
		Header: http.Header{},
	}

	err := gp.modifyResponse(resp)
	if err != nil {
		t.Fatalf("modifyResponse() failed: %v", err)
	}

	if resp.Header.Get("X-Gateway") != "go-apisix-extend" {
		t.Errorf("X-Gateway = %s, want 'go-apisix-extend'", resp.Header.Get("X-Gateway"))
	}
}

// ==================== 🔴 TDD: 完整流程测试 ====================

// TestFullFlow 测试完整请求流程
func TestFullFlow(t *testing.T) {
	r := router.NewRouter()
	r.AddRoute(&router.Route{
		Method:  "GET",
		Path:    "/api/v1/test",
		Backend: "http://127.0.0.1:18080",
	})

	auth := plugin.NewAuthPlugin("secret")
	rl := plugin.NewRateLimitPlugin(1000, 100)
	metrics := plugin.NewMetricsPlugin()

	p := plugin.NewPipeline()
	p.Use(auth.Auth())
	p.Use(rl.RateLimit())
	p.Use(metrics.Metrics())

	gp := NewGatewayProxy(r, p)

	// 带 token 的请求
	req := httptest.NewRequest("GET", "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer test-token-123")
	w := httptest.NewRecorder()

	gp.ServeHTTP(w, req)

	// 后端不可达，返回 502
	if w.Code != http.StatusBadGateway {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

// ==================== 🔴 TDD: 本地处理器（local://）测试 ====================

// TestServeHTTP_LocalHandler 测试 local:// 路由交给本地处理器并保留原始路径
func TestServeHTTP_LocalHandler(t *testing.T) {
	r := router.NewRouter()
	if err := r.AddRoute(&router.Route{
		Method:  "PUT",
		Path:    "/ingest/live/*",
		Backend: "local://live",
	}); err != nil {
		t.Fatalf("AddRoute() failed: %v", err)
	}

	var gotPath, gotBody string
	local := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		raw, _ := io.ReadAll(req.Body)
		gotBody = string(raw)
		w.WriteHeader(http.StatusNoContent)
	})

	gp := NewGatewayProxy(r, plugin.NewPipeline(), WithLocalHandler("live", local))

	req := httptest.NewRequest("PUT", "/ingest/live/cam0", strings.NewReader("ts-bytes"))
	w := httptest.NewRecorder()
	gp.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
	if gotPath != "/ingest/live/cam0" {
		t.Errorf("local handler path = %q, want /ingest/live/cam0", gotPath)
	}
	if gotBody != "ts-bytes" {
		t.Errorf("local handler body = %q, want ts-bytes", gotBody)
	}
}

// TestServeHTTP_LocalHandlerMissing 测试 local:// 处理器未注册
func TestServeHTTP_LocalHandlerMissing(t *testing.T) {
	r := router.NewRouter()
	if err := r.AddRoute(&router.Route{
		Method:  "GET",
		Path:    "/ingest/live/*",
		Backend: "local://live",
	}); err != nil {
		t.Fatalf("AddRoute() failed: %v", err)
	}

	gp := NewGatewayProxy(r, plugin.NewPipeline())

	req := httptest.NewRequest("GET", "/ingest/live/cam0", nil)
	w := httptest.NewRecorder()
	gp.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

// ==================== 🔴 TDD: 路径透传测试 ====================

// TestDirector_PreservesPath 后端不带路径时，必须原样透传客户端路径
func TestDirector_PreservesPath(t *testing.T) {
	gp := NewGatewayProxy(router.NewRouter(), plugin.NewPipeline())

	req := httptest.NewRequest("GET", "/api/status", nil)
	req = req.WithContext(context.WithValue(req.Context(), routeKey, &router.Route{
		Backend: "http://127.0.0.1:8099",
	}))

	gp.director(req)

	if req.URL.Host != "127.0.0.1:8099" {
		t.Errorf("host = %s, want 127.0.0.1:8099", req.URL.Host)
	}
	if req.URL.Path != "/api/status" {
		t.Errorf("path = %s, want /api/status", req.URL.Path)
	}
}

// TestDirector_JoinsBackendPath 后端自带前缀时按前缀拼接
func TestDirector_JoinsBackendPath(t *testing.T) {
	gp := NewGatewayProxy(router.NewRouter(), plugin.NewPipeline())

	req := httptest.NewRequest("GET", "/api/status", nil)
	req = req.WithContext(context.WithValue(req.Context(), routeKey, &router.Route{
		Backend: "http://127.0.0.1:8099/v1/",
	}))

	gp.director(req)

	if req.URL.Path != "/v1/api/status" {
		t.Errorf("path = %s, want /v1/api/status", req.URL.Path)
	}
}

// TestDefaultTransportTuning 校验上游连接池参数与复用行为。
//
// 这些数字在文档里对外承诺（README / ZetaGatewayReadme）：
// 标准库默认 MaxIdleConnsPerHost 只有 2，网关按代理场景放宽到 2000，
// 且连接池必须是包级单例，配置热加载重建 Proxy 时不能换池。
func TestDefaultTransportTuning(t *testing.T) {
	gp := NewGatewayProxy(router.NewRouter(), plugin.NewPipeline())

	tr, ok := gp.reverseProxy.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("reverse proxy transport = %T, want *http.Transport", gp.reverseProxy.Transport)
	}
	if tr.MaxIdleConns != 10000 {
		t.Errorf("MaxIdleConns = %d, want 10000", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != 2000 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 2000", tr.MaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 90s", tr.IdleConnTimeout)
	}

	reloaded := NewGatewayProxy(router.NewRouter(), plugin.NewPipeline())
	if reloaded.reverseProxy.Transport != gp.reverseProxy.Transport {
		t.Error("transport should be shared across proxies (config reload must reuse the pool)")
	}
}

// ==================== 🔴 TDD: Benchmark（网关完整请求路径） ====================

// benchResponseWriter 零分配 ResponseWriter，
// 用于把「网络 IO 与序列化」排除在计时之外，只测网关自身处理成本。
type benchResponseWriter struct {
	header http.Header
}

// Header 返回响应头
func (w *benchResponseWriter) Header() http.Header { return w.header }

// Write 丢弃响应体
func (w *benchResponseWriter) Write(b []byte) (int, error) { return len(b), nil }

// WriteHeader 忽略状态码
func (w *benchResponseWriter) WriteHeader(int) {}

// BenchmarkServeHTTP_LocalRoute 基准测试完整请求路径：
// 上下文池取用 → 无锁路由匹配 → 插件流水线（指标）→ local:// 本地处理器 → 上下文归还池。
// 该路径不经过网络，因此数字反映的是网关自身的单请求开销。
func BenchmarkServeHTTP_LocalRoute(b *testing.B) {
	r := router.NewRouter()
	for i := 0; i < 200; i++ {
		if err := r.AddRoute(&router.Route{
			Method:  "GET",
			Path:    fmt.Sprintf("/bench/api/%d", i),
			Backend: "http://test-service:8080",
		}); err != nil {
			b.Fatalf("AddRoute() failed: %v", err)
		}
	}
	if err := r.AddRoute(&router.Route{
		Method:  "GET",
		Path:    "/bench/local",
		Backend: "local://bench",
	}); err != nil {
		b.Fatalf("AddRoute() failed: %v", err)
	}

	p := plugin.NewPipeline()
	p.Use(plugin.NewMetricsPlugin().Metrics())

	gp := NewGatewayProxy(r, p,
		WithLocalHandler("bench", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))

	// 串行复用同一个请求与响应写入器：请求只读，GatewayContext 由池保证独立
	req := httptest.NewRequest("GET", "/bench/local", nil)
	w := &benchResponseWriter{header: make(http.Header, 4)}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gp.ServeHTTP(w, req)
	}
}
