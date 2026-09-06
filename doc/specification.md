# 高性能微服务网关 - 技术规范文档 (SDD)

## 1. 项目结构规范

```
go-apisix-extend/
├── doc/                          # 文档目录
│   ├── requestment.md            # 需求文档
│   └── specification.md          # 技术规范 (本文件)
├── pkg/
│   ├── router/                   # 无锁路由模块
│   │   ├── router.go             # 路由定义与匹配
│   │   └── router_test.go        # 路由测试
│   ├── context/                  # 零拷贝上下文模块
│   │   ├── gateway_context.go    # 网关上下文
│   │   └── gateway_context_test.go
│   ├── plugin/                   # 插件引擎模块
│   │   ├── pipeline.go           # 插件流水线
│   │   ├── pipeline_test.go
│   │   ├── auth.go               # 鉴权插件
│   │   ├── rate_limit.go         # 限流插件
│   │   └── metrics.go            # 监控插件
│   └── proxy/                    # 反向代理扩展模块
│       ├── proxy.go              # 代理核心
│       └── proxy_test.go
├── go.mod
├── go.sum
└── Makefile
```

## 2. 模块接口规范

### 2.1 无锁路由模块 (Lock-Free Router)

```go
// Route 路由规则
type Route struct {
    Method  string            // HTTP 方法 (GET, POST, PUT, DELETE 等)
    Path    string            // 路径模式 (/api/v1/users/:id)
    Backend string            // 后端服务地址 (http://backend-service:8080)
    Weight  int               // 权重 (预留负载均衡)
}

// Router 路由接口
type Router interface {
    // AddRoute 添加路由规则 (写操作 - Copy-On-Write)
    AddRoute(route *Route) error
    
    // RemoveRoute 移除路由规则 (写操作 - Copy-On-Write)
    RemoveRoute(method, path string) error
    
    // Match 匹配路由 (读操作 - Lock-Free)
    Match(method, path string) (*Route, bool)
    
    // Snapshot 获取当前路由快照 (用于观测)
    Snapshot() []*Route
}
```

### 2.2 零拷贝上下文模块 (Zero-Allocation Context)

```go
// GatewayContext 网关上下文
type GatewayContext struct {
    Request     *http.Request
    Response    http.ResponseWriter
    UserID      string
    Route       *Route
    startTime   time.Time
    data        map[string]interface{}  // 预分配容量
    index       int                     // 插件执行索引
    handlers    []HandlerFunc
    aborted     bool
}

// Reset 重置上下文，释放回 Pool
func (ctx *GatewayContext) Reset() {
    ctx.Request = nil
    ctx.Response = nil
    ctx.UserID = ""
    ctx.Route = nil
    ctx.startTime = time.Time{}
    // 清空 data 但保留预分配内存
    for k := range ctx.data {
        delete(ctx.data, k)
    }
    ctx.index = 0
    ctx.handlers = ctx.handlers[:0]
    ctx.aborted = false
}

// GetContextPool 获取上下文池
func GetContextPool() *sync.Pool
```

### 2.3 链式插件引擎 (Plugin Pipeline)

```go
// HandlerFunc 插件处理函数
type HandlerFunc func(ctx *GatewayContext)

// Pipeline 插件流水线
type Pipeline interface {
    // Use 注册插件
    Use(handlers ...HandlerFunc)
    
    // Execute 执行插件链
    Execute(ctx *GatewayContext)
}

// 内置插件接口
type Plugin interface {
    Name() string
    Handle(ctx *GatewayContext)
}
```

### 2.4 反向代理扩展模块 (Reverse Proxy Kernel)

```go
// GatewayProxy 网关代理
type GatewayProxy struct {
    router      Router
    pipeline    Pipeline
    pool        *sync.Pool
    reverseProxy *httputil.ReverseProxy
}

// NewGatewayProxy 创建网关代理
func NewGatewayProxy(router Router, pipeline Pipeline) *GatewayProxy

// ServeHTTP 实现 http.Handler 接口
func (p *GatewayProxy) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

## 3. 性能规范

| 指标 | 目标值 |
|------|--------|
| QPS | 25,000+ req/sec |
| P99 Latency | < 5ms |
| Allocs/Op | 0-2 allocs/op |
| 路由匹配 | O(1) 时间复杂度 |

## 4. 开发流程 (SDD + TDD)

### 4.1 SDD 阶段 (规范先行)
1. 定义模块接口规范
2. 定义数据结构规范
3. 定义性能指标规范

### 4.2 TDD 阶段 (测试驱动)
1. **红**: 根据规范编写测试用例（预期失败）
2. **绿**: 编写最简实现代码使测试通过
3. **重构**: 优化代码结构，保持测试通过

### 4.3 开发顺序
1. `pkg/router/` - 无锁路由（基础依赖）
2. `pkg/context/` - 零拷贝上下文（插件依赖）
3. `pkg/plugin/` - 链式插件引擎
4. `pkg/proxy/` - 反向代理核心（整合所有模块）
