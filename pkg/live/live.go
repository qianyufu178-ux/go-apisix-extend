// Package live 提供网关侧的直播接入面（MPEG-TS over HTTP）。
//
// 为什么网关需要「直播接入面」？
//   - 控制面（REST / WebSocket / 大屏静态资源）走无锁路由 + 插件链 + 反向代理即可；
//   - 媒体面是长连接、大流量、低延迟，不适合逐请求代理，
//     于是内嵌一个发布/订阅扇出中心：采集端推一次，转码器（可多实例）各拉一份。
//
// 路径约定：
//
//	PUT|POST /ingest/live/{name}         采集端推流（chunked body，视频封装建议 MPEG-TS）
//	GET      /ingest/live/{name}         转码器拉流（chunked 响应）
//	GET      /ingest/live/{name}/stats   单路统计
//	GET      /ingest/live               全部流统计
//
// 设计要点：
//   - 每个订阅者独立缓冲队列，慢订阅者按「整块丢弃」处理，发布端永不被阻塞；
//   - MPEG-TS 自带 0x47 同步字节，丢块后 FFmpeg 可自行重新同步，不会把整条流打挂；
//   - 同一路流只允许一个发布者（多发布者会让下游 SSRC / 时间戳错乱），冲突返回 409。
package live

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// TokenHeader 推流鉴权头（与配置里的 live.publish_token 对应）。
	TokenHeader = "X-Ingest-Token"
	// Prefix 直播接入面的路径前缀。
	Prefix = "/ingest/live"
	// DefaultQueueSize 每个订阅者默认缓冲块数。
	DefaultQueueSize = 64
	// DefaultIdleTimeout 订阅连接无数据多久后主动断开（交给上游 FFmpeg 重连）。
	DefaultIdleTimeout = 30 * time.Second
	// readChunkSize 单次读取的块大小。
	readChunkSize = 64 * 1024
)

// Options 是 Hub 的可选配置。
type Options struct {
	// PublishToken 非空时，推流必须携带匹配的 X-Ingest-Token。
	PublishToken string
	// QueueSize 每个订阅者的缓冲块数，<=0 时取 DefaultQueueSize。
	QueueSize int
	// IdleTimeout 订阅空闲超时，<=0 时取 DefaultIdleTimeout。
	IdleTimeout time.Duration
}

// StreamStats 是单路直播流的对外统计快照。
type StreamStats struct {
	Name        string    `json:"name"`
	Publishing  bool      `json:"publishing"`
	Publishers  int       `json:"publishers"`
	Subscribers int       `json:"subscribers"`
	BytesIn     uint64    `json:"bytes_in"`
	BytesOut    uint64    `json:"bytes_out"`
	ChunksIn    uint64    `json:"chunks_in"`
	Dropped     uint64    `json:"dropped"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	LastChunkAt time.Time `json:"last_chunk_at,omitempty"`
	SilenceMs   int64     `json:"silence_ms"`
}

// Hub 是直播流的注册表与扇出中心，可并发安全使用。
type Hub struct {
	token       string
	queueSize   int
	idleTimeout time.Duration

	mu      sync.RWMutex
	streams map[string]*Stream
}

// NewHub 创建 Hub。
func NewHub(opts Options) *Hub {
	queueSize := opts.QueueSize
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	return &Hub{
		token:       opts.PublishToken,
		queueSize:   queueSize,
		idleTimeout: idle,
		streams:     make(map[string]*Stream, 4),
	}
}

// Handler 返回直播接入面的 HTTP 处理器，通常注册为 local://live 路由。
func (h *Hub) Handler() http.Handler {
	return http.HandlerFunc(h.serveHTTP)
}

// Snapshot 返回所有直播流的统计快照。
func (h *Hub) Snapshot() []StreamStats {
	h.mu.RLock()
	streams := make([]*Stream, 0, len(h.streams))
	for _, s := range h.streams {
		streams = append(streams, s)
	}
	h.mu.RUnlock()

	out := make([]StreamStats, 0, len(streams))
	for _, s := range streams {
		out = append(out, s.stats())
	}
	return out
}

// Stats 返回指定流的统计快照。
func (h *Hub) Stats(name string) (StreamStats, bool) {
	h.mu.RLock()
	s, ok := h.streams[name]
	h.mu.RUnlock()
	if !ok {
		return StreamStats{}, false
	}
	return s.stats(), true
}

// stream 返回（必要时创建）指定名称的流对象。
func (h *Hub) stream(name string) *Stream {
	h.mu.RLock()
	s, ok := h.streams[name]
	h.mu.RUnlock()
	if ok {
		return s
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok = h.streams[name]; ok {
		return s
	}
	s = &Stream{name: name, subs: make(map[uint64]*subscriber, 4)}
	h.streams[name] = s
	return s
}

func (h *Hub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == Prefix || path == Prefix+"/" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"prefix":  Prefix,
			"streams": h.Snapshot(),
		})
		return
	}

	name, isStats, ok := splitStreamPath(path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "invalid stream path"})
		return
	}

	if isStats {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		stats, found := h.Stats(name)
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "stream not found", "name": name})
			return
		}
		writeJSON(w, http.StatusOK, stats)
		return
	}

	switch r.Method {
	case http.MethodPut, http.MethodPost:
		h.servePublish(w, r, name)
	case http.MethodGet:
		h.serveSubscribe(w, r, name)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodPost)
	}
}

// servePublish 处理采集端推流：把请求体按块广播给所有订阅者。
func (h *Hub) servePublish(w http.ResponseWriter, r *http.Request, name string) {
	if !h.tokenOK(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid publish token"})
		return
	}

	st := h.stream(name)
	if !st.beginPublish() {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "stream is already being published",
			"name":  name,
		})
		return
	}
	start := time.Now()
	var bytesIn uint64

	defer func() {
		st.endPublish()
		log.Printf("[live] 推流结束 stream=%s bytes=%d cost=%s", name, bytesIn, time.Since(start).Round(time.Millisecond))
	}()

	log.Printf("[live] 推流开始 stream=%s remote=%s", name, r.RemoteAddr)

	// 注意：这里刻意不在读完 body 之前写响应头。
	// Go 的 HTTP 客户端在收到「响应早于请求体结束」时会中止请求体写入，
	// 长连接推流必须先读流、推流结束后再回统计 JSON；鉴权/冲突检查在读取前完成，
	// 采集端依然能第一时间感知 401 / 409。
	buf := make([]byte, readChunkSize)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			bytesIn += uint64(n)
			// 复制一份再广播：buf 下一轮会被复用
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			st.broadcast(chunk)
		}
		if err != nil {
			break
		}
	}

	writeJSON(w, http.StatusOK, st.stats())
}

// serveSubscribe 处理转码器拉流：以 chunked 响应持续下发数据块。
func (h *Hub) serveSubscribe(w http.ResponseWriter, r *http.Request, name string) {
	st := h.stream(name)
	sub := st.addSubscriber(h.queueSize)
	defer st.removeSubscriber(sub)

	log.Printf("[live] 拉流接入 stream=%s remote=%s", name, r.RemoteAddr)
	defer log.Printf("[live] 拉流断开 stream=%s remote=%s", name, r.RemoteAddr)

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = flush(w)

	idle := time.NewTimer(h.idleTimeout)
	defer idle.Stop()

	for {
		select {
		case chunk := <-sub.ch:
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if err := flush(w); err != nil {
				return
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(h.idleTimeout)
		case <-idle.C:
			return
		case <-r.Context().Done():
			return
		}
	}
}

// tokenOK 校验推流令牌（未配置令牌时直接放行）。
func (h *Hub) tokenOK(r *http.Request) bool {
	h.mu.RLock()
	token := h.token
	h.mu.RUnlock()

	if token == "" {
		return true
	}
	got := r.Header.Get(TokenHeader)
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// SetPublishToken 热更新推流令牌（配置热加载时调用，已有连接不受影响）。
func (h *Hub) SetPublishToken(token string) {
	h.mu.Lock()
	h.token = token
	h.mu.Unlock()
}

// Stream 是单路直播流。
type Stream struct {
	name string

	mu          sync.Mutex
	subs        map[uint64]*subscriber
	nextID      uint64
	publishing  bool
	startedAt   time.Time
	lastChunkAt time.Time
	bytesIn     uint64
	bytesOut    uint64
	chunksIn    uint64
	dropped     uint64
}

// subscriber 是一个独立的订阅缓冲。
type subscriber struct {
	ch chan []byte
}

func (s *Stream) beginPublish() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publishing {
		return false
	}
	s.publishing = true
	s.startedAt = time.Now()
	return true
}

func (s *Stream) endPublish() {
	s.mu.Lock()
	s.publishing = false
	s.mu.Unlock()
}

func (s *Stream) addSubscriber(queueSize int) *subscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	sub := &subscriber{ch: make(chan []byte, queueSize)}
	s.subs[s.nextID] = sub
	return sub
}

func (s *Stream) removeSubscriber(sub *subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, candidate := range s.subs {
		if candidate == sub {
			delete(s.subs, id)
			close(sub.ch)
			return
		}
	}
}

// broadcast 把数据块投递给所有订阅者；队列满的订阅者直接丢块并计数。
func (s *Stream) broadcast(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.bytesIn += uint64(len(chunk))
	s.chunksIn++
	s.lastChunkAt = time.Now()

	for _, sub := range s.subs {
		select {
		case sub.ch <- chunk:
			s.bytesOut += uint64(len(chunk))
		default:
			s.dropped++
		}
	}
}

func (s *Stream) stats() StreamStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	stats := StreamStats{
		Name:        s.name,
		Publishing:  s.publishing,
		Subscribers: len(s.subs),
		BytesIn:     s.bytesIn,
		BytesOut:    s.bytesOut,
		ChunksIn:    s.chunksIn,
		Dropped:     s.dropped,
		LastChunkAt: s.lastChunkAt,
	}
	if s.publishing {
		stats.Publishers = 1
		stats.StartedAt = s.startedAt
	}
	if !s.lastChunkAt.IsZero() {
		stats.SilenceMs = time.Since(s.lastChunkAt).Milliseconds()
	}
	return stats
}

// splitStreamPath 从 /ingest/live/{name}[/stats] 中解析流名。
func splitStreamPath(path string) (name string, isStats bool, ok bool) {
	rest := strings.TrimPrefix(path, Prefix+"/")
	if rest == path || rest == "" {
		return "", false, false
	}
	if idx := strings.LastIndex(rest, "/"); idx >= 0 {
		if strings.TrimPrefix(rest[idx+1:], "/") == "stats" {
			name = rest[:idx]
			isStats = true
		} else {
			return "", false, false
		}
	} else {
		name = rest
	}
	if name == "" || strings.Contains(name, "/") {
		return "", false, false
	}
	return name, isStats, true
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}

// flush 立即把响应头/已写数据推给对端（长连接场景必须）。
//
// 这里用 http.Flusher 而不是 http.NewResponseController，
// 以保持与 go.mod 里声明的 Go 1.18 最低版本兼容。
func flush(w http.ResponseWriter) error {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
