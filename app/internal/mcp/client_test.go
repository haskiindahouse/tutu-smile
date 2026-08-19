package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func rpcOK(result string) string {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{"content": []map[string]any{{"type": "text", "text": result}}},
	})
	return string(b)
}

func newTestClient(url string, opts ...Option) *Client {
	base := []Option{WithRetries(0), WithPoliteness(2, 0), WithBreaker(3, 200*time.Millisecond)}
	return New(url, 5*time.Second, append(base, opts...)...)
}

func TestCallToolUnwrapsDoubleEncoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, rpcOK(`{"hello":"мир"}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)
	raw, err := c.CallTool(context.Background(), "test", map[string]any{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if json.Unmarshal(raw, &out) != nil || out["hello"] != "мир" {
		t.Fatalf("payload not unwrapped: %s", raw)
	}
}

func TestCacheAndSingleFlight(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, rpcOK(`{"n":1}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)
	for i := 0; i < 5; i++ {
		if _, err := c.CallTool(context.Background(), "t", map[string]any{"a": 1}, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("cache must collapse repeats into 1 network call, got %d", got)
	}
}

func TestBreakerOpensAndCloses(t *testing.T) {
	var fail int32 = 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&fail) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, rpcOK(`{}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)

	// Three transport failures open the circuit (distinct args dodge the cache).
	for i := 0; i < 3; i++ {
		c.CallTool(context.Background(), "t", map[string]any{"i": i}, true)
	}
	if open, _ := c.BreakerOpen(); !open {
		t.Fatal("breaker must be open after the failure streak")
	}
	if _, err := c.CallTool(context.Background(), "t", map[string]any{"i": 99}, true); err != ErrCircuitOpen {
		t.Fatalf("open circuit must fail fast, got %v", err)
	}

	// After the cooldown a healthy server closes it again.
	atomic.StoreInt32(&fail, 0)
	time.Sleep(250 * time.Millisecond)
	if _, err := c.CallTool(context.Background(), "t", map[string]any{"i": 100}, true); err != nil {
		t.Fatalf("call after cooldown must succeed, got %v", err)
	}
	if open, _ := c.BreakerOpen(); open {
		t.Fatal("breaker must close after a success")
	}
}

func TestPolitenessSpacing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, rpcOK(`{}`))
	}))
	defer srv.Close()
	c := New(srv.URL, 5*time.Second, WithRetries(0), WithPoliteness(1, 60*time.Millisecond), WithBreaker(0, 0))
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := c.CallTool(context.Background(), "t", map[string]any{"i": i}, true); err != nil {
			t.Fatal(err)
		}
	}
	if e := time.Since(start); e < 120*time.Millisecond {
		t.Fatalf("3 spaced calls must take >=120ms, took %v", e)
	}
}

func TestParseProxyForms(t *testing.T) {
	u := ParseProxy("176.103.86.34:63134:USER:PASS")
	if u == nil || u.Host != "176.103.86.34:63134" || u.User.Username() != "USER" {
		t.Fatalf("compact form parse failed: %v", u)
	}
	if p, _ := u.User.Password(); p != "PASS" {
		t.Fatal("password lost")
	}
	u2 := ParseProxy("http://u:p@h:1")
	if u2 == nil || u2.Scheme != "http" || u2.Host != "h:1" {
		t.Fatalf("url form parse failed: %v", u2)
	}
	if ParseProxy("garbage") != nil || ParseProxy("") != nil {
		t.Fatal("invalid entries must be skipped")
	}
}
