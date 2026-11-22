// Package router 提供无锁（Lock-Free）路由匹配功能
//
// 设计原则：
// - 读操作：使用 sync/atomic.Value 实现无锁读取
// - 写操作：采用 Copy-On-Write（写时复制）机制
// - 路由匹配：支持精确匹配，预留前缀匹配与正则匹配扩展接口
package router

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

// MethodAny 表示该路由匹配任意 HTTP 方法（配置里写 "*"）。
const MethodAny = "*"

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
	// prefix 前缀匹配时缓存的路径前缀（已去掉结尾的 *），避免每次匹配重复切片
	prefix string
}

// methodMatches 判断请求方法是否命中本条目
func (e *routeEntry) methodMatches(method string) bool {
	return e.route.Method == MethodAny || e.route.Method == method
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
	// prefix 前缀匹配路由表（按前缀长度倒序，保证最长前缀优先命中）
	prefix []*routeEntry
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

// routeKeySep 路由键分隔符
const routeKeySep = ":"

// buildRouteKey 构建路由键
//
// 使用字符串拼接而非 fmt.Sprintf：Sprintf 会带来多次反射/缓冲分配，
// 拼接在等长输出下只产生 1 次字符串分配（写入路径才会调用）。
func buildRouteKey(method, path string) string {
	return method + routeKeySep + path
}

// classifyPath 判定路径匹配类型：以 * 结尾视为前缀匹配（/api/*），否则精确匹配。
func classifyPath(path string) (matchType, string) {
	if strings.HasSuffix(path, "*") {
		return matchPrefix, strings.TrimSuffix(path, "*")
	}
	return matchExact, ""
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
	key := buildRouteKey(route.Method, route.Path)
	newTable := &routeTable{
		exact:  make(map[string]*routeEntry, len(oldTable.exact)+1),
		prefix: make([]*routeEntry, 0, len(oldTable.prefix)+1),
	}
	for k, v := range oldTable.exact {
		if k != key {
			newTable.exact[k] = v
		}
	}
	for _, entry := range oldTable.prefix {
		// 直接比字段，避免为每条前缀路由构建临时 key（写入路径也免分配）
		if entry.route.Method == route.Method && entry.route.Path == route.Path {
			continue
		}
		newTable.prefix = append(newTable.prefix, entry)
	}

	// 添加新路由
	match, prefix := classifyPath(route.Path)
	entry := &routeEntry{route: route, matchType: match, prefix: prefix}
	if match == matchPrefix {
		newTable.prefix = append(newTable.prefix, entry)
		// 最长前缀优先：/api/v1/* 应优先于 /api/*
		sort.SliceStable(newTable.prefix, func(i, j int) bool {
			return len(newTable.prefix[i].prefix) > len(newTable.prefix[j].prefix)
		})
	} else {
		newTable.exact[key] = entry
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

	_, inExact := oldTable.exact[key]
	hasPrefix := false
	for _, entry := range oldTable.prefix {
		if entry.route.Method == method && entry.route.Path == path {
			hasPrefix = true
			break
		}
	}
	if !inExact && !hasPrefix {
		return fmt.Errorf("route not found: %s %s", method, path)
	}

	// Copy-On-Write：创建新路由表
	newTable := &routeTable{
		exact:  make(map[string]*routeEntry, len(oldTable.exact)),
		prefix: make([]*routeEntry, 0, len(oldTable.prefix)),
	}
	for k, v := range oldTable.exact {
		if k != key {
			newTable.exact[k] = v
		}
	}
	for _, entry := range oldTable.prefix {
		if buildRouteKey(entry.route.Method, entry.route.Path) == key {
			continue
		}
		newTable.prefix = append(newTable.prefix, entry)
	}

	r.routes.Store(newTable)
	return nil
}

// Match 匹配路由（Lock-Free 读取）
//
// 先 O(1) 精确匹配，再按最长前缀命中前缀规则；
// 两者都不命中才返回 false。
func (r *router) Match(method, path string) (*Route, bool) {
	table := r.routes.Load().(*routeTable)

	// 路由键在 map 下标里内联拼接：编译器不会为这次查找分配临时字符串，
	// 读路径（每请求必经）因此保持零分配；key 只由写入路径显式构建。
	if entry, ok := table.exact[method+routeKeySep+path]; ok {
		return entry.route, true
	}

	for _, entry := range table.prefix {
		if !entry.methodMatches(method) {
			continue
		}
		if strings.HasPrefix(path, entry.prefix) {
			return entry.route, true
		}
	}

	return nil, false
}

// Snapshot 获取当前路由快照
func (r *router) Snapshot() []*Route {
	table := r.routes.Load().(*routeTable)
	routes := make([]*Route, 0, len(table.exact)+len(table.prefix))
	for _, entry := range table.exact {
		routes = append(routes, entry.route)
	}
	for _, entry := range table.prefix {
		routes = append(routes, entry.route)
	}
	return routes
}
