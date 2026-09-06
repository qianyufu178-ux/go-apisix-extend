package context

import (
	"net/http"
	"sync"
	"testing"
)

// ==================== 🔴 TDD: 基础功能测试 ====================

// TestGetContext 测试从池中获取上下文
func TestGetContext(t *testing.T) {
	ctx := GetContext()
	if ctx == nil {
		t.Fatal("GetContext() should not return nil")
	}
	PutContext(ctx)
}

// TestPutContext 测试释放上下文回池
func TestPutContext(t *testing.T) {
	ctx := GetContext()
	ctx.Set("key", "value")
	PutContext(ctx)

	// 再次获取，验证已被 Reset
	ctx2 := GetContext()
	if _, ok := ctx2.Get("key"); ok {
		t.Error("context should be reset after PutContext")
	}
	PutContext(ctx2)
}

// TestSetGet 测试设置和获取上下文数据
func TestSetGet(t *testing.T) {
	ctx := GetContext()
	defer PutContext(ctx)

	ctx.Set("user_id", "12345")
	ctx.Set("role", "admin")

	if val, ok := ctx.Get("user_id"); !ok || val.(string) != "12345" {
		t.Errorf("Get('user_id') = %v, %v, want '12345', true", val, ok)
	}

	if val, ok := ctx.Get("role"); !ok || val.(string) != "admin" {
		t.Errorf("Get('role') = %v, %v, want 'admin', true", val, ok)
	}

	if _, ok := ctx.Get("nonexistent"); ok {
		t.Error("Get('nonexistent') should return false")
	}
}

// TestReset 测试 Reset 方法
func TestReset(t *testing.T) {
	ctx := GetContext()

	// 设置一些数据
	ctx.Request = &http.Request{}
	ctx.UserID = "test_user"
	ctx.Set("key1", "value1")
	ctx.Set("key2", "value2")

	// 执行 Reset
	ctx.Reset()

	// 验证所有字段被清空
	if ctx.Request != nil {
		t.Error("Request should be nil after Reset")
	}
	if ctx.UserID != "" {
		t.Error("UserID should be empty after Reset")
	}
	if _, ok := ctx.Get("key1"); ok {
		t.Error("data should be cleared after Reset")
	}
	if _, ok := ctx.Get("key2"); ok {
		t.Error("data should be cleared after Reset")
	}
	if ctx.IsAborted() {
		t.Error("aborted should be false after Reset")
	}

	PutContext(ctx)
}

// ==================== 🔴 TDD: Next/Abort 测试 ====================

// TestNext 测试 Next 调用链
func TestNext(t *testing.T) {
	ctx := GetContext()
	defer PutContext(ctx)

	var order []string

	handlers := []HandlerFunc{
		func(c *GatewayContext) {
			order = append(order, "handler1")
			c.Next()
		},
		func(c *GatewayContext) {
			order = append(order, "handler2")
			c.Next()
		},
		func(c *GatewayContext) {
			order = append(order, "handler3")
		},
	}

	ctx.SetHandlers(handlers)
	ctx.index = -1
	ctx.Next()

	expected := []string{"handler1", "handler2", "handler3"}
	if len(order) != len(expected) {
		t.Errorf("order length = %d, want %d", len(order), len(expected))
	}
	for i, v := range expected {
		if i < len(order) && order[i] != v {
			t.Errorf("order[%d] = %s, want %s", i, order[i], v)
		}
	}
}

// TestAbort 测试 Abort 中断调用链
func TestAbort(t *testing.T) {
	ctx := GetContext()
	defer PutContext(ctx)

	var order []string

	handlers := []HandlerFunc{
		func(c *GatewayContext) {
			order = append(order, "handler1")
			c.Abort()
			c.Next() // 应该被忽略
		},
		func(c *GatewayContext) {
			order = append(order, "handler2")
			c.Next()
		},
	}

	ctx.SetHandlers(handlers)
	ctx.index = -1
	ctx.Next()

	if len(order) != 1 || order[0] != "handler1" {
		t.Errorf("order = %v, want [handler1]", order)
	}
}

// TestNext_NoHandlers 测试没有 handler 时 Next 不 panic
func TestNext_NoHandlers(t *testing.T) {
	ctx := GetContext()
	defer PutContext(ctx)

	ctx.index = -1
	ctx.Next() // 不应该 panic
}

// ==================== 🔴 TDD: 并发安全测试 ====================

// TestConcurrentPoolAccess 测试并发池访问
func TestConcurrentPoolAccess(t *testing.T) {
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ctx := GetContext()
			ctx.Set("goroutine", id)
			ctx.Set("data", "test")
			PutContext(ctx)
		}(i)
	}

	wg.Wait()
}

// ==================== 🔴 TDD: Benchmark 测试 ====================

// BenchmarkGetPutContext 基准测试 Get/Put 性能
func BenchmarkGetPutContext(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ctx := GetContext()
		ctx.Set("key", "value")
		PutContext(ctx)
	}
}

// BenchmarkPoolReuse 基准测试池复用（验证零分配）
func BenchmarkPoolReuse(b *testing.B) {
	b.ReportAllocs()
	ctxs := make([]*GatewayContext, b.N)
	for i := 0; i < b.N; i++ {
		ctxs[i] = GetContext()
		ctxs[i].Set("key", "value")
	}
	for i := 0; i < b.N; i++ {
		PutContext(ctxs[i])
	}
}
