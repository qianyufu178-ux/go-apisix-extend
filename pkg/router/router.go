// Package router 提供无锁（Lock-Free）路由匹配功能
//
// 设计原则：
// - 读操作：使用 sync/atomic.Value 实现无锁读取
// - 写操作：采用 Copy-On-Write（写时复制）机制
// - 路由匹配：支持精确匹配，预留前缀匹配与正则匹配扩展接口
package router

import (
	"fmt"
	"sync/atomic"
)

// Route 路由规则
type Route struct {
	// Method HTTP 方法 (GET, POST, PUT, DELETE 等)
	Method string
	// Path 路径模式，支持精确匹配
	// 预留扩展：前缀匹配 /api/v1/*，正则匹配 /api/v1/:id
	Path string
	// Backend 后端服务地址 (http://backend-service:8080)
	Backend string
	// Weight 权重，预留负载均衡
	Weight int
}

// matchType 路由匹配类型
type matchType int

const (
	matchExact  matchType = iota // 精确匹配
	matchPrefix                  // 前缀匹配（预留）
	matchRegex                   // 正则匹配（预留）
)

// routeEntry 内部路由条目
type routeEntry struct {
	route     *Route
	matchType matchType
}

// Router 路由接口
type Router interface {
	// AddRoute 添加路由规则（写操作 - Copy-On-Write）
	AddRoute(route *Route) error

	// RemoveRoute 移除路由规则（写操作 - Copy-On-Write）
	RemoveRoute(method, path string) error

	// Match 匹配路由（读操作 - Lock-Free）
	Match(method, path string) (*Route, bool)

	// Snapshot 获取当前路由快照（用于观测）
	Snapshot() []*Route
}

// router 无锁路由实现
type router struct {
	// routes 使用 atomic.Value 存储路由表，实现无锁读取
	routes atomic.Value
}

// routeTable 路由表
type routeTable struct {
	// exact 精确匹配路由表
	// key: "METHOD:path", value: *routeEntry
	exact map[string]*routeEntry
}

// NewRouter 创建无锁路由器
func NewRouter() Router {
	r := &router{}
	// 初始化空路由表
	r.routes.Store(&routeTable{
		exact: make(map[string]*routeEntry),
	})
	return r
}

// buildRouteKey 构建路由键
func buildRouteKey(method, path string) string {
	return fmt.Sprintf("%s:%s", method, path)
}

// AddRoute 添加路由规则（Copy-On-Write）
func (r *router) AddRoute(route *Route) error {
	if route == nil {
		return fmt.Errorf("route cannot be nil")
	}
	if route.Method == "" {
		return fmt.Errorf("route method cannot be empty")
	}
	if route.Path == "" {
		return fmt.Errorf("route path cannot be empty")
	}
	if route.Backend == "" {
		return fmt.Errorf("route backend cannot be empty")
	}

	// 读取当前路由表快照
	oldTable := r.routes.Load().(*routeTable)

	// Copy-On-Write：创建新路由表
	newTable := &routeTable{
		exact: make(map[string]*routeEntry, len(oldTable.exact)+1),
	}

	// 复制旧数据
	for k, v := range oldTable.exact {
		newTable.exact[k] = v
	}

	// 添加新路由
	key := buildRouteKey(route.Method, route.Path)
	newTable.exact[key] = &routeEntry{
		route:     route,
		matchType: matchExact,
	}

	// 原子替换
	r.routes.Store(newTable)
	return nil
}

// RemoveRoute 移除路由规则（Copy-On-Write）
func (r *router) RemoveRoute(method, path string) error {
	if method == "" || path == "" {
		return fmt.Errorf("method and path cannot be empty")
	}

	oldTable := r.routes.Load().(*routeTable)
	key := buildRouteKey(method, path)

	if _, exists := oldTable.exact[key]; !exists {
		return fmt.Errorf("route not found: %s %s", method, path)
	}

	// Copy-On-Write：创建新路由表
	newTable := &routeTable{
		exact: make(map[string]*routeEntry, len(oldTable.exact)-1),
	}

	for k, v := range oldTable.exact {
		if k != key {
			newTable.exact[k] = v
		}
	}

	r.routes.Store(newTable)
	return nil
}

// Match 匹配路由（Lock-Free 读取）
func (r *router) Match(method, path string) (*Route, bool) {
	table := r.routes.Load().(*routeTable)
	key := buildRouteKey(method, path)

	if entry, ok := table.exact[key]; ok {
		return entry.route, true
	}

	return nil, false
}

// Snapshot 获取当前路由快照
func (r *router) Snapshot() []*Route {
	table := r.routes.Load().(*routeTable)
	routes := make([]*Route, 0, len(table.exact))
	for _, entry := range table.exact {
		routes = append(routes, entry.route)
	}
	return routes
}
