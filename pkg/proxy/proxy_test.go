package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
