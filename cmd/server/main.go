你// go-apisix-extend 网关服务入口
//
// 启动方式：
//   go run cmd/server/main.go -config config.yaml
//
// 支持优雅关闭（Ctrl+C）和配置热加载
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/yfyadmin/go-apisix-extend/pkg/config"
	"github.com/yfyadmin/go-apisix-extend/pkg/plugin"
	"github.com/yfyadmin/go-apisix-extend/pkg/proxy"
	"github.com/yfyadmin/go-apisix-extend/pkg/router"
)

// buildRouter 从配置创建路由器
func buildRouter(cfg *config.Config) router.Router {
	r := router.NewRouter()
	for _, routeCfg := range cfg.Routes {
		r.AddRoute(&router.Route{
			Method:  routeCfg.Method,
			Path:    routeCfg.Path,
			Backend: routeCfg.Backend,
			Weight:  routeCfg.Weight,
		})
	}
	return r
}

// buildPipeline 从配置创建插件流水线
func buildPipeline(cfg *config.Config) plugin.Pipeline {
	p := plugin.NewPipeline()

	if cfg.Plugins.Auth.Enabled {
		authPlugin := plugin.NewAuthPlugin(cfg.Plugins.Auth.Secret)
		p.Use(authPlugin.Auth())
		log.Println("✅ 鉴权插件已启用")
	}

	if cfg.Plugins.RateLimit.Enabled {
		rlPlugin := plugin.NewRateLimitPlugin(cfg.Plugins.RateLimit.Rate, cfg.Plugins.RateLimit.Capacity)
		p.Use(rlPlugin.RateLimit())
		log.Println("✅ 限流插件已启用")
	}

	return p
}

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	// 加载初始配置
	cfg, err := config.LoadFromFile(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 创建路由和插件流水线
	r := buildRouter(cfg)
	p := buildPipeline(cfg)

	// 创建网关代理
	gp := proxy.NewGatewayProxy(r, p)

	// 创建 HTTP Server
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      gp,
		ReadTimeout:  cfg.Server.ReadTimeout.Duration,
		WriteTimeout: cfg.Server.WriteTimeout.Duration,
	}

	// 启动配置热加载监听
	watcher, err := config.NewWatcher(*configPath, func(newCfg *config.Config) {
		log.Println("🔄 配置已变更，热加载中...")

		// 重新构建路由和插件，原子替换 Server Handler
		newGP := proxy.NewGatewayProxy(buildRouter(newCfg), buildPipeline(newCfg))
		srv.Handler = newGP

		log.Println("✅ 配置热加载完成")
	})
	if err != nil {
		log.Fatalf("创建配置监听器失败: %v", err)
	}
	watcher.Start()
	defer watcher.Stop()

	// 启动 Server（goroutine）
	go func() {
		log.Printf("🚀 网关服务启动于 %s", addr)
		log.Printf("📋 已加载 %d 条路由规则", len(cfg.Routes))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务启动失败: %v", err)
		}
	}()

	// 等待中断信号（优雅关闭）
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 正在关闭服务...")

	// 优雅关闭（带超时）
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout.Duration)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("服务关闭异常: %v", err)
	}

	log.Println("✅ 服务已安全关闭")
	os.Exit(0)
}
