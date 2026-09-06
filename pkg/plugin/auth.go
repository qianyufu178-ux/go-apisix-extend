package plugin

import (
	"github.com/yfyadmin/go-apisix-extend/pkg/context"
)

// AuthPlugin 鉴权插件
// 拦截未授权请求，验证 JWT Token
type AuthPlugin struct {
	// 预留：JWT secret key 配置
	secret string
}

// NewAuthPlugin 创建鉴权插件
func NewAuthPlugin(secret string) *AuthPlugin {
	return &AuthPlugin{secret: secret}
}

// Auth 返回鉴权插件处理函数
func (p *AuthPlugin) Auth() context.HandlerFunc {
	return func(ctx *context.GatewayContext) {
		// 从 Header 获取 token
		token := ctx.Request.Header.Get("Authorization")
		if token == "" {
			ctx.Abort()
			return
		}

		// 预留：JWT 验证逻辑
		// 简单示例：将 token 前8位作为 UserID
		if len(token) >= 8 {
			ctx.UserID = token[:8]
		}

		ctx.Next()
	}
}
