package router

import (
	"fmt"
	"sync"
	"testing"
)

// ==================== 🔴 TDD Step 1: 基础功能测试 ====================

// TestNewRouter 测试创建路由器
func TestNewRouter(t *testing.T) {
	r := NewRouter()
	if r == nil {
		t.Fatal("NewRouter() should not return nil")
	}
}

// TestAddRoute 测试添加路由
func TestAddRoute(t *testing.T) {
	r := NewRouter()

	route := &Route{
		Method:  "GET",
		Path:    "/api/v1/users",
		Backend: "http://user-service:8080",
		Weight:  1,
	}

	err := r.AddRoute(route)
	if err != nil {
		t.Fatalf("AddRoute() failed: %v", err)
	}
}

// TestMatch_Exact 测试精确匹配
func TestMatch_Exact(t *testing.T) {
	r := NewRouter()

	// 添加路由
	r.AddRoute(&Route{
		Method:  "GET",
		Path:    "/api/v1/users",
		Backend: "http://user-service:8080",
	})

	r.AddRoute(&Route{
		Method:  "POST",
		Path:    "/api/v1/users",
		Backend: "http://user-service:8080",
	})

	// 测试精确匹配
	tests := []struct {
		method  string
		path    string
		wantOK  bool
		wantURL string
	}{
		{"GET", "/api/v1/users", true, "http://user-service:8080"},
		{"POST", "/api/v1/users", true, "http://user-service:8080"},
		{"GET", "/api/v1/notfound", false, ""},
		{"DELETE", "/api/v1/users", false, ""},
	}

	for _, tt := range tests {
		got, ok := r.Match(tt.method, tt.path)
		if ok != tt.wantOK {
			t.Errorf("Match(%s, %s) ok = %v, want %v", tt.method, tt.path, ok, tt.wantOK)
		}
		if ok && got.Backend != tt.wantURL {
			t.Errorf("Match(%s, %s) Backend = %s, want %s", tt.method, tt.path, got.Backend, tt.wantURL)
		}
	}
}

// TestRemoveRoute 测试移除路由
func TestRemoveRoute(t *testing.T) {
	r := NewRouter()

	r.AddRoute(&Route{
		Method:  "GET",
		Path:    "/api/v1/users",
		Backend: "http://user-service:8080",
	})

	// 确认存在
	if _, ok := r.Match("GET", "/api/v1/users"); !ok {
		t.Fatal("route should exist before removal")
	}

	// 移除
	err := r.RemoveRoute("GET", "/api/v1/users")
	if err != nil {
		t.Fatalf("RemoveRoute() failed: %v", err)
	}

	// 确认已移除
	if _, ok := r.Match("GET", "/api/v1/users"); ok {
		t.Error("route should not exist after removal")
	}
}

// TestRemoveRoute_NotFound 测试移除不存在的路由
func TestRemoveRoute_NotFound(t *testing.T) {
	r := NewRouter()
	err := r.RemoveRoute("GET", "/not/exist")
	if err == nil {
		t.Error("RemoveRoute() should return error for non-existent route")
	}
}

// ==================== 🔴 TDD Step 2: 边界条件测试 ====================

// TestAddRoute_NilRoute 测试添加 nil 路由
func TestAddRoute_NilRoute(t *testing.T) {
	r := NewRouter()
	err := r.AddRoute(nil)
	if err == nil {
		t.Error("AddRoute(nil) should return error")
	}
}

// TestAddRoute_EmptyMethod 测试添加空 Method 的路由
func TestAddRoute_EmptyMethod(t *testing.T) {
	r := NewRouter()
	err := r.AddRoute(&Route{
		Method:  "",
		Path:    "/test",
		Backend: "http://test",
	})
	if err == nil {
		t.Error("AddRoute() with empty method should return error")
	}
}

// TestAddRoute_EmptyPath 测试添加空 Path 的路由
func TestAddRoute_EmptyPath(t *testing.T) {
	r := NewRouter()
	err := r.AddRoute(&Route{
		Method:  "GET",
		Path:    "",
		Backend: "http://test",
	})
	if err == nil {
		t.Error("AddRoute() with empty path should return error")
	}
}

// TestAddRoute_EmptyBackend 测试添加空 Backend 的路由
func TestAddRoute_EmptyBackend(t *testing.T) {
	r := NewRouter()
	err := r.AddRoute(&Route{
		Method:  "GET",
		Path:    "/test",
		Backend: "",
	})
	if err == nil {
		t.Error("AddRoute() with empty backend should return error")
	}
}

// ==================== 🔴 TDD Step 3: 并发安全测试 ====================

// TestConcurrentReadWrite 测试并发读写安全
func TestConcurrentReadWrite(t *testing.T) {
	r := NewRouter()

	// 先添加一些路由
	for i := 0; i < 10; i++ {
		path := fmt.Sprintf("/api/v%d/test", i)
		r.AddRoute(&Route{
			Method:  "GET",
			Path:    path,
			Backend: "http://test-service:8080",
		})
	}

	var wg sync.WaitGroup

	// 并发读取
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			path := fmt.Sprintf("/api/v%d/test", id%10)
			if _, ok := r.Match("GET", path); !ok {
				t.Errorf("concurrent read failed for path: %s", path)
			}
		}(i)
	}

	// 并发写入
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			route := &Route{
				Method:  "POST",
				Path:    fmt.Sprintf("/api/v%d/new", id),
				Backend: "http://new-service:8080",
			}
			r.AddRoute(route)
		}(i)
	}

	wg.Wait()

	// 验证写入没有被并发读取影响
	snapshot := r.Snapshot()
	if len(snapshot) < 10 {
		t.Errorf("expected at least 10 routes, got %d", len(snapshot))
	}
}

// ==================== 🔴 TDD Step 4: Snapshot 测试 ====================

// TestSnapshot 测试路由快照
func TestSnapshot(t *testing.T) {
	r := NewRouter()

	routes := []*Route{
		{Method: "GET", Path: "/api/v1/users", Backend: "http://users:8080"},
		{Method: "POST", Path: "/api/v1/users", Backend: "http://users:8080"},
		{Method: "GET", Path: "/api/v1/orders", Backend: "http://orders:8080"},
	}

	for _, route := range routes {
		r.AddRoute(route)
	}

	snapshot := r.Snapshot()
	if len(snapshot) != len(routes) {
		t.Errorf("Snapshot() len = %d, want %d", len(snapshot), len(routes))
	}
}

// ==================== 🔴 TDD Step 5: Benchmark 测试 ====================

// BenchmarkMatch 基准测试路由匹配性能
func BenchmarkMatch(b *testing.B) {
	r := NewRouter()

	// 添加 1000 条路由
	for i := 0; i < 1000; i++ {
		path := fmt.Sprintf("/api/v%d/%d/test/path", i/100, i%100)
		r.AddRoute(&Route{
			Method:  "GET",
			Path:    path,
			Backend: "http://test-service:8080",
		})
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.Match("GET", "/api/v1/42/test/path")
		}
	})
}

// BenchmarkAddRoute 基准测试添加路由性能
func BenchmarkAddRoute(b *testing.B) {
	r := NewRouter()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := fmt.Sprintf("/api/v%d/test", i)
		r.AddRoute(&Route{
			Method:  "GET",
			Path:    path,
			Backend: "http://test-service:8080",
		})
	}
}
