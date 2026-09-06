package plugin

import (
	"sync/atomic"
	"time"

	"github.com/yfyadmin/go-apisix-extend/pkg/context"
)

// MetricsPlugin 监控指标采集插件
type MetricsPlugin struct {
	totalRequests  int64 // 总请求数
	activeRequests int64 // 活跃请求数
	totalLatency   int64 // 总延迟（纳秒）
}

// NewMetricsPlugin 创建监控插件
func NewMetricsPlugin() *MetricsPlugin {
	return &MetricsPlugin{}
}

// Metrics 返回监控插件处理函数
func (p *MetricsPlugin) Metrics() context.HandlerFunc {
	return func(ctx *context.GatewayContext) {
		start := time.Now()
		atomic.AddInt64(&p.activeRequests, 1)
		atomic.AddInt64(&p.totalRequests, 1)

		ctx.Next()

		latency := time.Since(start).Nanoseconds()
		atomic.AddInt64(&p.totalLatency, latency)
		atomic.AddInt64(&p.activeRequests, -1)
	}
}

// Stats 返回当前统计信息
func (p *MetricsPlugin) Stats() map[string]interface{} {
	total := atomic.LoadInt64(&p.totalRequests)
	latency := atomic.LoadInt64(&p.totalLatency)
	active := atomic.LoadInt64(&p.activeRequests)

	stats := map[string]interface{}{
		"total_requests":  total,
		"active_requests": active,
	}

	if total > 0 {
		avgLatency := time.Duration(latency / total)
		stats["avg_latency"] = avgLatency.String()
	}

	return stats
}
