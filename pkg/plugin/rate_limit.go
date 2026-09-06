package plugin

import (
	"sync/atomic"
	"time"

	"github.com/yfyadmin/go-apisix-extend/pkg/context"
)

// RateLimitPlugin 令牌桶限流插件
type RateLimitPlugin struct {
	rate     int64         // 每秒令牌数
	capacity int64         // 桶容量
	tokens   int64         // 当前令牌数
	lastTime int64         // 上次补充时间（unix nano）
}

// NewRateLimitPlugin 创建限流插件
// rate: 每秒允许的请求数
// capacity: 桶容量（突发流量上限）
func NewRateLimitPlugin(rate, capacity int64) *RateLimitPlugin {
	return &RateLimitPlugin{
		rate:     rate,
		capacity: capacity,
		tokens:   capacity, // 初始满桶
		lastTime: time.Now().UnixNano(),
	}
}

// RateLimit 返回限流插件处理函数
func (p *RateLimitPlugin) RateLimit() context.HandlerFunc {
	return func(ctx *context.GatewayContext) {
		if !p.allow() {
			ctx.Abort()
			return
		}
		ctx.Next()
	}
}

// allow 检查是否允许请求通过（原子操作，无锁）
func (p *RateLimitPlugin) allow() bool {
	// 补充令牌（CAS 循环确保只有一个 goroutine 执行补充）
	for {
		lastTime := atomic.LoadInt64(&p.lastTime)
		now := time.Now().UnixNano()
		elapsed := now - lastTime

		if elapsed <= 0 {
			// 时间未流逝，跳过补充
			break
		}

		// 尝试更新 lastTime（只有成功者才执行补充）
		if atomic.CompareAndSwapInt64(&p.lastTime, lastTime, now) {
			// 计算应补充的令牌数
			tokens := atomic.LoadInt64(&p.tokens)
			newTokens := tokens + (elapsed * p.rate / int64(time.Second))
			if newTokens > p.capacity {
				newTokens = p.capacity
			}
			atomic.StoreInt64(&p.tokens, newTokens)
		}
		break
	}

	// 消耗令牌
	for {
		tokens := atomic.LoadInt64(&p.tokens)
		if tokens <= 0 {
			return false
		}
		if atomic.CompareAndSwapInt64(&p.tokens, tokens, tokens-1) {
			return true
		}
	}
}
