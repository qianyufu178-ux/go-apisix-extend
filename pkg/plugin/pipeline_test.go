package plugin

import (
	"net/http"
	"testing"

	"github.com/yfyadmin/go-apisix-extend/pkg/context"
)

// ==================== 🔴 TDD: Pipeline 基础测试 ====================

// TestNewPipeline 测试创建 Pipeline
func TestNewPipeline(t *testing.T) {
	p := NewPipeline()
	if p == nil {
		t.Fatal("NewPipeline() should not return nil")
	}
}

// TestUse 测试注册插件
func TestUse(t *testing.T) {
	p := NewPipeline()
	handler := func(ctx *context.GatewayContext) {
		ctx.Next()
	}
	p.Use(handler)
	// 没有 panic 即通过
}

// TestExecute 测试执行插件链
func TestExecute(t *testing.T) {
	p := NewPipeline()
	ctx := context.GetContext()
	defer context.PutContext(ctx)

	var order []string

	p.Use(
		func(c *context.GatewayContext) {
			order = append(order, "plugin1")
			c.Next()
		},
		func(c *context.GatewayContext) {
			order = append(order, "plugin2")
			c.Next()
		},
	)

	p.Execute(ctx)

	expected := []string{"plugin1", "plugin2"}
	if len(order) != len(expected) {
		t.Errorf("order = %v, want %v", order, expected)
	}
	for i, v := range expected {
		if i < len(order) && order[i] != v {
			t.Errorf("order[%d] = %s, want %s", i, order[i], v)
		}
	}
}

// TestExecute_Abort 测试执行中被中止
func TestExecute_Abort(t *testing.T) {
	p := NewPipeline()
	ctx := context.GetContext()
	defer context.PutContext(ctx)

	var order []string

	p.Use(
		func(c *context.GatewayContext) {
			order = append(order, "auth")
			c.Abort() // 鉴权失败
		},
		func(c *context.GatewayContext) {
			order = append(order, "rate_limit") // 不应执行
			c.Next()
		},
	)

	p.Execute(ctx)

	if len(order) != 1 || order[0] != "auth" {
		t.Errorf("order = %v, want [auth]", order)
	}
}

// ==================== 🔴 TDD: Auth 插件测试 ====================

// TestAuthPlugin_Pass 测试鉴权通过
func TestAuthPlugin_Pass(t *testing.T) {
	ctx := context.GetContext()
	defer context.PutContext(ctx)

	// 模拟带 token 的请求
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer mysecrettoken")
	ctx.Request = req

	auth := NewAuthPlugin("secret")
	p := NewPipeline()
	p.Use(auth.Auth())

	var passed bool
	p.Use(func(c *context.GatewayContext) {
		passed = true
	})

	p.Execute(ctx)

	if !passed {
		t.Error("request with token should pass auth")
	}
	if ctx.UserID != "Bearer m" {
		t.Errorf("UserID = %s, want 'Bearer m'", ctx.UserID)
	}
}

// TestAuthPlugin_Block 测试鉴权拦截
func TestAuthPlugin_Block(t *testing.T) {
	ctx := context.GetContext()
	defer context.PutContext(ctx)

	// 模拟无 token 的请求
	req, _ := http.NewRequest("GET", "/test", nil)
	ctx.Request = req

	auth := NewAuthPlugin("secret")
	p := NewPipeline()
	p.Use(auth.Auth())

	var passed bool
	p.Use(func(c *context.GatewayContext) {
		passed = true
	})

	p.Execute(ctx)

	if passed {
		t.Error("request without token should be blocked")
	}
}

// ==================== 🔴 TDD: RateLimit 插件测试 ====================

// TestRateLimit_Allow 测试限流通过
func TestRateLimit_Allow(t *testing.T) {
	ctx := context.GetContext()
	defer context.PutContext(ctx)

	rl := NewRateLimitPlugin(1000, 10) // 每秒1000，桶容量10
	p := NewPipeline()
	p.Use(rl.RateLimit())

	var passed bool
	p.Use(func(c *context.GatewayContext) {
		passed = true
	})

	p.Execute(ctx)

	if !passed {
		t.Error("first request should be allowed")
	}
}

// TestRateLimit_Block 测试限流拦截
func TestRateLimit_Block(t *testing.T) {
	ctx := context.GetContext()
	defer context.PutContext(ctx)

	rl := NewRateLimitPlugin(1000, 1) // 每秒1000，桶容量1
	p := NewPipeline()

	// 消耗掉唯一的令牌
	ctx2 := context.GetContext()
	p.Use(rl.RateLimit())
	p.Execute(ctx2)
	context.PutContext(ctx2)

	// 再次请求应该被限流
	var blocked bool
	p2 := NewPipeline()
	p2.Use(rl.RateLimit())
	p2.Use(func(c *context.GatewayContext) {
		blocked = true
	})
	p2.Execute(ctx)

	if blocked {
		t.Error("request exceeding rate limit should be blocked")
	}
}

// ==================== 🔴 TDD: Metrics 插件测试 ====================

// TestMetricsPlugin 测试监控插件
func TestMetricsPlugin(t *testing.T) {
	metrics := NewMetricsPlugin()
	p := NewPipeline()
	p.Use(metrics.Metrics())

	ctx := context.GetContext()
	p.Execute(ctx)
	context.PutContext(ctx)

	stats := metrics.Stats()
	if stats["total_requests"].(int64) != 1 {
		t.Errorf("total_requests = %d, want 1", stats["total_requests"])
	}
	if stats["active_requests"].(int64) != 0 {
		t.Errorf("active_requests = %d, want 0", stats["active_requests"])
	}
}

// ==================== 🔴 TDD: 完整插件链测试 ====================

// TestFullPipeline 测试完整插件链：Auth → RateLimit → Metrics
func TestFullPipeline(t *testing.T) {
	ctx := context.GetContext()
	defer context.PutContext(ctx)

	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "valid-token-here")
	ctx.Request = req

	auth := NewAuthPlugin("secret")
	rl := NewRateLimitPlugin(1000, 100)
	metrics := NewMetricsPlugin()

	p := NewPipeline()
	p.Use(auth.Auth())
	p.Use(rl.RateLimit())
	p.Use(metrics.Metrics())

	var reached bool
	p.Use(func(c *context.GatewayContext) {
		reached = true
	})

	p.Execute(ctx)

	if !reached {
		t.Error("full pipeline should complete successfully")
	}
	if ctx.UserID == "" {
		t.Error("UserID should be set after auth")
	}
}
