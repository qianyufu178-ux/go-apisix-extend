package live

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestHub 启动一个内嵌 Hub 的测试服务器。
func newTestHub(t *testing.T, opts Options) (*Hub, *httptest.Server) {
	t.Helper()
	hub := NewHub(opts)
	srv := httptest.NewServer(hub.Handler())
	t.Cleanup(srv.Close)
	return hub, srv
}

// waitFor 轮询等待条件成立，超时即测试失败。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// TestPublishSubscribeRoundTrip 验证「推流 → 扇出 → 拉流」的完整链路。
func TestPublishSubscribeRoundTrip(t *testing.T) {
	hub, srv := newTestHub(t, Options{QueueSize: 8, IdleTimeout: 3 * time.Second})

	sub, err := http.Get(srv.URL + Prefix + "/cam0")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Body.Close()

	if sub.StatusCode != http.StatusOK {
		t.Fatalf("subscribe status = %d, want 200", sub.StatusCode)
	}

	waitFor(t, "subscriber registered", func() bool {
		stats, ok := hub.Stats("cam0")
		return ok && stats.Subscribers == 1
	})

	payload := bytes.Repeat([]byte("ts-chunk-0123456789"), 1024)
	go func() {
		req, reqErr := http.NewRequest(http.MethodPut, srv.URL+Prefix+"/cam0", bytes.NewReader(payload))
		if reqErr != nil {
			return
		}
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(sub.Body, got); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("payload mismatch between publisher and subscriber")
	}

	waitFor(t, "publish finished", func() bool {
		stats, _ := hub.Stats("cam0")
		return stats.BytesIn == uint64(len(payload))
	})

	stats, _ := hub.Stats("cam0")
	if stats.BytesOut < uint64(len(payload)) {
		t.Fatalf("bytes_out = %d, want >= %d", stats.BytesOut, len(payload))
	}
	if stats.ChunksIn == 0 {
		t.Fatal("chunks_in should be greater than 0")
	}
}

// TestPublishTokenRequired 验证推流令牌校验。
func TestPublishTokenRequired(t *testing.T) {
	_, srv := newTestHub(t, Options{PublishToken: "demo-token"})

	put := func(token string) int {
		req, err := http.NewRequest(http.MethodPut, srv.URL+Prefix+"/cam0", bytes.NewReader([]byte("x")))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if token != "" {
			req.Header.Set(TokenHeader, token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	if code := put(""); code != http.StatusUnauthorized {
		t.Fatalf("publish without token status = %d, want 401", code)
	}
	if code := put("wrong-token"); code != http.StatusUnauthorized {
		t.Fatalf("publish with wrong token status = %d, want 401", code)
	}
	if code := put("demo-token"); code != http.StatusOK {
		t.Fatalf("publish with correct token status = %d, want 200", code)
	}
}

// TestConcurrentPublishRejected 验证同一路流只允许一个发布者。
func TestConcurrentPublishRejected(t *testing.T) {
	hub, srv := newTestHub(t, Options{})

	stream := hub.stream("cam0")
	if !stream.beginPublish() {
		t.Fatal("first publish should be accepted")
	}
	defer stream.endPublish()

	req, err := http.NewRequest(http.MethodPost, srv.URL+Prefix+"/cam0", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second publisher status = %d, want 409", resp.StatusCode)
	}
}

// TestStatsEndpoints 验证统计接口（单路 / 全量 / 未知流）。
func TestStatsEndpoints(t *testing.T) {
	hub, srv := newTestHub(t, Options{})
	hub.stream("cam0").broadcast([]byte("hello"))

	resp, err := http.Get(srv.URL + Prefix + "/cam0/stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d, body=%s", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"chunks_in":1`)) {
		t.Fatalf("stats body = %s, want chunks_in=1", body)
	}

	resp, err = http.Get(srv.URL + Prefix)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !bytes.Contains(body, []byte(`"name":"cam0"`)) {
		t.Fatalf("list body = %s, want cam0", body)
	}

	resp, err = http.Get(srv.URL + Prefix + "/unknown/stats")
	if err != nil {
		t.Fatalf("unknown stats: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown stats status = %d, want 404", resp.StatusCode)
	}
}

// TestSplitStreamPath 验证路径解析。
func TestSplitStreamPath(t *testing.T) {
	cases := []struct {
		path    string
		name    string
		isStats bool
		ok      bool
	}{
		{"/ingest/live/cam0", "cam0", false, true},
		{"/ingest/live/cam0/stats", "cam0", true, true},
		{"/ingest/live/a/b", "", false, false},
		{"/ingest/live/", "", false, false},
		{"/ingest/live", "", false, false},
		{"/other", "", false, false},
	}

	for _, tc := range cases {
		name, isStats, ok := splitStreamPath(tc.path)
		if ok != tc.ok || name != tc.name || isStats != tc.isStats {
			t.Errorf("splitStreamPath(%q) = (%q, %v, %v), want (%q, %v, %v)",
				tc.path, name, isStats, ok, tc.name, tc.isStats, tc.ok)
		}
	}
}
