// Package plugin 提供链式插件引擎（Plugin Pipeline）
//
// 设计原则：
// - 洋葱模型执行链
// - 支持 ctx.Next() 调用后续插件
// - 支持 ctx.Abort() 中断调用链
// - 内置插件：Auth → RateLimit → Metrics
package plugin

import (
	"github.com/yfyadmin/go-apisix-extend/pkg/context"
)

// Pipeline 插件流水线接口
type Pipeline interface {
	// Use 注册插件处理函数
	Use(handlers ...context.HandlerFunc)
	// Execute 执行插件链
	Execute(ctx *context.GatewayContext)
}

// pipeline 插件流水线实现
type pipeline struct {
	handlers []context.HandlerFunc
}

// NewPipeline 创建插件流水线
func NewPipeline() Pipeline {
	return &pipeline{
		handlers: make([]context.HandlerFunc, 0, 8),
	}
}

// Use 注册插件处理函数
func (p *pipeline) Use(handlers ...context.HandlerFunc) {
	p.handlers = append(p.handlers, handlers...)
}

// Execute 执行插件链
func (p *pipeline) Execute(ctx *context.GatewayContext) {
	if len(p.handlers) == 0 {
		return
	}

	// 设置 handlers 到上下文
	ctx.SetHandlers(p.handlers)
	ctx.SetIndex(-1)

	// 启动插件链
	ctx.Next()
}
