// Package context 提供零拷贝（Zero-Allocation）网关上下文管理
//
// 设计原则：
// - 使用 sync.Pool 管理 GatewayContext 对象池
// - 请求完成后调用 Reset() 清空并释放回池
// - 内部 map 预分配容量，避免动态扩容
package context

import (
	"net/http"
	"sync"
)

// HandlerFunc 插件处理函数
type HandlerFunc func(ctx *GatewayContext)

// GatewayContext 网关上下文
// 通过 sync.Pool 管理，实现零拷贝复用
type GatewayContext struct {
	// Request HTTP 请求
	Request *http.Request
	// Response HTTP 响应写入器
	Response http.ResponseWriter
	// UserID 认证后的用户 ID
	UserID string
	// Route 匹配到的路由（引用 router.Route，避免循环导入用 interface）
	Route interface{}
	// startTime 请求开始时间
	startTime int64
	// data 预分配的上下文数据存储
	data map[string]interface{}
	// index 当前插件执行索引
	index int
	// handlers 插件处理函数链
	handlers []HandlerFunc
	// aborted 是否已中止
	aborted bool
}

// preAllocDataSize 预分配 data map 的容量
const preAllocDataSize = 8

// Reset 重置上下文，释放回 Pool
func (ctx *GatewayContext) Reset() {
	ctx.Request = nil
	ctx.Response = nil
	ctx.UserID = ""
	ctx.Route = nil
	ctx.startTime = 0
	ctx.index = 0
	ctx.aborted = false

	// 清空 handlers 但保留底层数组
	ctx.handlers = ctx.handlers[:0]

	// 清空 data 但保留预分配内存
	for k := range ctx.data {
		delete(ctx.data, k)
	}
}

// Set 设置上下文数据
func (ctx *GatewayContext) Set(key string, value interface{}) {
	if ctx.data == nil {
		ctx.data = make(map[string]interface{}, preAllocDataSize)
	}
	ctx.data[key] = value
}

// Get 获取上下文数据
func (ctx *GatewayContext) Get(key string) (interface{}, bool) {
	if ctx.data == nil {
		return nil, false
	}
	val, ok := ctx.data[key]
	return val, ok
}

// Next 调用下一个插件处理函数
func (ctx *GatewayContext) Next() {
	if ctx.aborted {
		return
	}
	ctx.index++
	if ctx.index < len(ctx.handlers) {
		ctx.handlers[ctx.index](ctx)
	}
}

// Abort 中止插件链执行
func (ctx *GatewayContext) Abort() {
	ctx.aborted = true
}

// IsAborted 检查是否已中止
func (ctx *GatewayContext) IsAborted() bool {
	return ctx.aborted
}

// Handlers 获取插件处理函数链（供 Pipeline 使用）
func (ctx *GatewayContext) Handlers() []HandlerFunc {
	return ctx.handlers
}

// SetHandlers 设置插件处理函数链（供 Pipeline 使用）
func (ctx *GatewayContext) SetHandlers(handlers []HandlerFunc) {
	ctx.handlers = handlers
}

// SetIndex 设置插件执行索引（供 Pipeline 使用）
func (ctx *GatewayContext) SetIndex(index int) {
	ctx.index = index
}

// contextPool 网关上下文对象池
var contextPool = &sync.Pool{
	New: func() interface{} {
		return &GatewayContext{
			data: make(map[string]interface{}, preAllocDataSize),
		}
	},
}

// GetContext 从池中获取 GatewayContext
func GetContext() *GatewayContext {
	return contextPool.Get().(*GatewayContext)
}

// PutContext 将 GatewayContext 释放回池中
func PutContext(ctx *GatewayContext) {
	ctx.Reset()
	contextPool.Put(ctx)
}
