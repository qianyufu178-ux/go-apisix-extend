// Package proxy 提供反向代理核心扩展
//
// 设计原则：
// - 基于 httputil.ReverseProxy
// - 自定义 Director、ModifyResponse、ErrorHandler
// - 整合 Router + Pipeline + Context 所有模块
package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	gwctx "github.com/yfyadmin/go-apisix-extend/pkg/context"
	"github.com/yfyadmin/go-apisix-extend/pkg/plugin"
	"github.com/yfyadmin/go-apisix-extend/pkg/router"
)

// GatewayProxy 网关代理
// 整合路由、插件流水线、上下文池和反向代理
type GatewayProxy struct {
	router       router.Router
	pipeline     plugin.Pipeline
	reverseProxy *httputil.ReverseProxy
	timeout      time.Duration
}

// ProxyOption 网关代理配置选项
type ProxyOption func(*GatewayProxy)

// WithTimeout 设置代理超时时间
func WithTimeout(timeout time.Duration) ProxyOption {
	return func(p *GatewayProxy) {
		p.timeout = timeout
	}
}

// NewGatewayProxy 创建网关代理
func NewGatewayProxy(r router.Router, p plugin.Pipeline, opts ...ProxyOption) *GatewayProxy {
	gp := &GatewayProxy{
		router:   r,
		pipeline: p,
		timeout:  30 * time.Second,
	}

	for _, opt := range opts {
		opt(gp)
	}

	// 创建反向代理，使用自定义 Director
	gp.reverseProxy = &httputil.ReverseProxy{
		Director:       gp.director,
		ModifyResponse: gp.modifyResponse,
		ErrorHandler:   gp.errorHandler,
	}

	return gp
}

// ServeHTTP 实现 http.Handler 接口
// 完整的请求处理流程：
// 1. 从池中获取 Context
// 2. 路由匹配
// 3. 执行插件流水线
// 4. 反向代理转发
// 5. 释放 Context 回池
func (p *GatewayProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. 从池中获取 Context
	ctx := gwctx.GetContext()
	defer gwctx.PutContext(ctx)

	ctx.Request = r
	ctx.Response = w

	// 2. 路由匹配
	route, ok := p.router.Match(r.Method, r.URL.Path)
	if !ok {
		http.Error(w, `{"error":"route not found","code":404}`, http.StatusNotFound)
		return
	}
	ctx.Route = route

	// 3. 执行插件流水线
	p.pipeline.Execute(ctx)

	// 如果插件中止了请求，不进行转发
	if ctx.IsAborted() {
		return
	}

	// 4. 将路由信息注入请求 context，供 director 使用
	// 注意：WithContext 返回新请求，ReverseProxy 内部会使用它
	reqWithRoute := r.WithContext(context.WithValue(r.Context(), routeKey, route))

	// 5. 反向代理转发
	p.reverseProxy.ServeHTTP(w, reqWithRoute)
}

// director 自定义 Director 函数
// 注入网关重写后的 HTTP Header
func (p *GatewayProxy) director(req *http.Request) {
	// 从请求上下文中获取路由信息
	// 由于 ReverseProxy 内部会创建新请求，我们需要从原始请求中获取
	if route, ok := req.Context().Value(routeKey).(*router.Route); ok {
		if targetURL, err := url.Parse(route.Backend); err == nil {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.URL.Path = targetURL.Path
		}
	}

	// 注入自定义 Header
	if req.Header.Get("X-Real-IP") == "" {
		req.Header.Set("X-Real-IP", req.RemoteAddr)
	}
	req.Header.Set("X-Forwarded-For", req.RemoteAddr)
	req.Header.Set("X-Forwarded-Host", req.Host)
}

// modifyResponse 自定义 ModifyResponse 回调
// 允许后置插件对后端响应进行修改
func (p *GatewayProxy) modifyResponse(resp *http.Response) error {
	// 预留：后置插件处理响应
	// 例如：统一添加响应头
	resp.Header.Set("X-Gateway", "go-apisix-extend")
	return nil
}

// errorHandler 自定义错误处理
// 捕获后端服务错误，统一返回 JSON 格式
func (p *GatewayProxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	statusCode := http.StatusBadGateway

	// 根据错误类型判断状态码
	if urlErr, ok := err.(*url.Error); ok && urlErr.Timeout() {
		statusCode = http.StatusGatewayTimeout
	}

	w.WriteHeader(statusCode)

	errResp := map[string]interface{}{
		"error":   "upstream_error",
		"message": "后端服务不可用，请稍后重试",
		"code":    statusCode,
	}
	json.NewEncoder(w).Encode(errResp)
}

// contextKey 用于存储请求上下文中的键
type contextKey string

const routeKey contextKey = "route"
