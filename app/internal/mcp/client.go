// Package mcp is a small, dependency-free client for the Tutu MCP server.
//
// The server speaks JSON-RPC 2.0 over plain HTTP POST, is stateless and needs
// no auth. Tool results are double-encoded: the useful payload is a JSON string
// living at result.content[0].text, which this client unwraps for callers.
//
// The client adds what an orchestrator needs on top of raw calls:
//   - a TTL cache keyed by (tool, arguments) so a fan-out over guests that
//     revisits the same (origin,dest,date) hits the network once;
//   - bounded retries with backoff on transient (5xx / network) failures;
//   - a single flight de-dup so concurrent identical calls collapse into one;
//   - a politeness gate (bounded concurrency + minimum spacing between
//     requests) so a guest fan-out never reads as an attack to the WAF;
//   - a circuit breaker: consecutive transport failures open the circuit and
//     calls fail fast during a cooldown instead of retry-storming a server
//     that is already refusing us. Verified live: an unthrottled fan-out got
//     the IP TLS-banned by tutu.ru — бережливость is a correctness feature.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	endpoint string
	http     *http.Client
	// pool are proxied HTTP clients rotated round-robin; empty pool = direct
	// via c.http. A retry after a transport failure lands on the next proxy,
	// and a proxy that keeps failing is benched for a cooldown so cheap
	// residential IPs that die mid-day stop eating retries.
	pool     []*proxyEntry
	poolSeq  uint64
	poolMu   sync.Mutex
	retries  int
	cacheTTL time.Duration

	mu     sync.Mutex
	cache  map[string]cacheEntry
	flight map[string]*call // single-flight in-progress calls

	idSeq int64
	idMu  sync.Mutex

	// Politeness gate: bounded concurrency + minimum spacing between requests.
	gate        chan struct{}
	minInterval time.Duration
	lastReqMu   sync.Mutex
	lastReq     time.Time

	// Circuit breaker over transport-level failures.
	brMu        sync.Mutex
	brFails     int           // consecutive transport failures
	brThreshold int           // failures that open the circuit
	brCooldown  time.Duration // how long the circuit stays open
	brOpenUntil time.Time

	// metrics (best-effort, for the /health and decision transparency).
	statsMu   sync.Mutex
	callsMade int
	cacheHits int
}

// ErrCircuitOpen is returned while the breaker cools down; callers show it as
// «сервер Туту остывает» instead of piling on more requests.
var ErrCircuitOpen = fmt.Errorf("mcp: circuit open — сервер отдыхает после серии сетевых отказов")

// proxyEntry is one pooled proxy with its health state.
type proxyEntry struct {
	client    *http.Client
	fails     int       // consecutive transport failures through this proxy
	benchedTo time.Time // skipped until this instant after repeated failures
}

const (
	proxyBenchFails    = 2                // failures in a row before benching
	proxyBenchCooldown = 10 * time.Minute // dead residential proxies stay dead

	// retryMaxBackoff caps the exponential retry backoff: with a proxy pool
	// retries scale with pool size, and an uncapped 2^n growth would spend the
	// whole ctx budget sleeping instead of walking the pool.
	retryMaxBackoff = 5 * time.Second
)

type cacheEntry struct {
	payload json.RawMessage
	expires time.Time
}

type call struct {
	done    chan struct{}
	payload json.RawMessage
	err     error
}

// Option configures the client.
type Option func(*Client)

func WithRetries(n int) Option             { return func(c *Client) { c.retries = n } }
func WithCacheTTL(d time.Duration) Option  { return func(c *Client) { c.cacheTTL = d } }
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithPoliteness bounds in-flight requests and spaces consecutive ones.
func WithPoliteness(maxConcurrent int, minInterval time.Duration) Option {
	return func(c *Client) {
		if maxConcurrent > 0 {
			c.gate = make(chan struct{}, maxConcurrent)
		}
		c.minInterval = minInterval
	}
}

// WithBreaker opens the circuit after `fails` consecutive transport failures
// for the `cooldown` duration.
func WithBreaker(fails int, cooldown time.Duration) Option {
	return func(c *Client) { c.brThreshold, c.brCooldown = fails, cooldown }
}

// WithProxies installs a rotating pool of upstream proxies. Each entry is
// either a full URL (http://user:pass@host:port) or the compact
// host:port:user:pass form. Invalid entries are skipped; an empty result
// keeps the direct client.
func WithProxies(entries []string, timeout time.Duration) Option {
	return func(c *Client) {
		for _, e := range entries {
			u := ParseProxy(e)
			if u == nil {
				continue
			}
			proxyURL := u
			// A DEAD proxy must fail in seconds, not eat the whole request
			// timeout: живой прогон показал, что зависший dial на мёртвый IP
			// сжигает бюджет времени плеча и город выглядит «без дороги».
			tr := &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
				DialContext: (&net.Dialer{
					Timeout:   4 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: timeout,
			}
			c.pool = append(c.pool, &proxyEntry{client: &http.Client{
				Timeout:   timeout,
				Transport: tr,
			}})
		}
	}
}

// ProbeProxies pings every pooled proxy once (cheap HEAD to the endpoint) and
// benches the dead ones immediately, so the first real fan-out never pays for
// discovering them. Call it in a goroutine at startup.
func (c *Client) ProbeProxies(ctx context.Context) {
	c.poolMu.Lock()
	pool := append([]*proxyEntry(nil), c.pool...)
	c.poolMu.Unlock()
	var wg sync.WaitGroup
	for _, e := range pool {
		wg.Add(1)
		go func(e *proxyEntry) {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.endpoint, nil)
			if err != nil {
				return
			}
			resp, err := e.client.Do(req)
			c.poolMu.Lock()
			defer c.poolMu.Unlock()
			if err != nil {
				e.benchedTo = time.Now().Add(proxyBenchCooldown)
				return
			}
			resp.Body.Close()
			e.fails = 0
			e.benchedTo = time.Time{}
		}(e)
	}
	wg.Wait()
}

// ParseProxy accepts "http://user:pass@host:port" or "host:port:user:pass"
// and returns a proxy URL, or nil when the entry is unusable.
func ParseProxy(entry string) *url.URL {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil
	}
	if strings.Contains(entry, "://") {
		u, err := url.Parse(entry)
		if err != nil || u.Host == "" {
			return nil
		}
		return u
	}
	parts := strings.Split(entry, ":")
	switch len(parts) {
	case 2: // host:port
		return &url.URL{Scheme: "http", Host: parts[0] + ":" + parts[1]}
	case 4: // host:port:user:pass
		return &url.URL{
			Scheme: "http",
			Host:   parts[0] + ":" + parts[1],
			User:   url.UserPassword(parts[2], parts[3]),
		}
	default:
		return nil
	}
}

// nextHTTP picks the client for this attempt: round-robin across HEALTHY
// proxies, falling back to a benched one only when the whole pool is down.
// The returned report func must be called with the attempt's transport
// outcome so the proxy's health advances.
func (c *Client) nextHTTP() (*http.Client, func(ok bool)) {
	if len(c.pool) == 0 {
		return c.http, func(bool) {}
	}
	c.poolMu.Lock()
	defer c.poolMu.Unlock()
	now := time.Now()
	var pick *proxyEntry
	for range c.pool {
		n := atomic.AddUint64(&c.poolSeq, 1)
		e := c.pool[int(n)%len(c.pool)]
		if now.After(e.benchedTo) {
			pick = e
			break
		}
	}
	if pick == nil { // всё на скамейке — берём следующего, вдруг ожил
		n := atomic.AddUint64(&c.poolSeq, 1)
		pick = c.pool[int(n)%len(c.pool)]
	}
	return pick.client, func(ok bool) {
		c.poolMu.Lock()
		defer c.poolMu.Unlock()
		if ok {
			pick.fails = 0
			pick.benchedTo = time.Time{}
			return
		}
		pick.fails++
		if pick.fails >= proxyBenchFails {
			pick.benchedTo = time.Now().Add(proxyBenchCooldown)
			pick.fails = 0
		}
	}
}

// ProxyStats reports pool size and how many proxies are currently benched.
func (c *Client) ProxyStats() (total, benched int) {
	c.poolMu.Lock()
	defer c.poolMu.Unlock()
	now := time.Now()
	for _, e := range c.pool {
		if now.Before(e.benchedTo) {
			benched++
		}
	}
	return len(c.pool), benched
}

// ProxyCount reports the pool size for /healthz transparency.
func (c *Client) ProxyCount() int { return len(c.pool) }

func New(endpoint string, timeout time.Duration, opts ...Option) *Client {
	c := &Client{
		endpoint:    endpoint,
		http:        &http.Client{Timeout: timeout},
		retries:     2,
		cacheTTL:    90 * time.Second,
		cache:       map[string]cacheEntry{},
		flight:      map[string]*call{},
		gate:        make(chan struct{}, 4),
		minInterval: 150 * time.Millisecond,
		brThreshold: 5,
		brCooldown:  60 * time.Second,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ToolError is returned when the server marks a tool result isError=true.
type ToolError struct {
	Tool string
	Msg  string
}

func (e *ToolError) Error() string { return fmt.Sprintf("mcp tool %s: %s", e.Tool, e.Msg) }

// Stats reports coarse counters for transparency panels.
func (c *Client) Stats() (calls, hits int) {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	return c.callsMade, c.cacheHits
}

// CallTool invokes an MCP tool and returns the unwrapped domain payload
// (already un-double-encoded from result.content[0].text). Results are cached
// for cacheTTL unless noCache is true.
func (c *Client) CallTool(ctx context.Context, tool string, args any, noCache bool) (json.RawMessage, error) {
	key := cacheKey(tool, args)

	if !noCache {
		if p, ok := c.cacheGet(key); ok {
			c.bump(0, 1)
			return p, nil
		}
	}

	// Single-flight: collapse concurrent identical calls.
	c.mu.Lock()
	if inflight, ok := c.flight[key]; ok {
		c.mu.Unlock()
		select {
		case <-inflight.done:
			return inflight.payload, inflight.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	fc := &call{done: make(chan struct{})}
	c.flight[key] = fc
	c.mu.Unlock()

	payload, err := c.doWithRetry(ctx, tool, args)

	fc.payload, fc.err = payload, err
	close(fc.done)
	c.mu.Lock()
	delete(c.flight, key)
	c.mu.Unlock()

	if err == nil && !noCache {
		c.cacheSet(key, payload)
	}
	c.bump(1, 0)
	return payload, err
}

func (c *Client) doWithRetry(ctx context.Context, tool string, args any) (json.RawMessage, error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	// С прокси-пулом попыток должно хватить, чтобы обойти мёртвые IP до
	// того, как скамейка их выучит: минимум по одной на прокси.
	retries := c.retries
	if n := len(c.pool); n > retries {
		retries = n
	}
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			backoff *= 2
			if backoff > retryMaxBackoff {
				backoff = retryMaxBackoff
			}
		}
		if err := c.breakerAllow(); err != nil {
			return nil, err // fail fast while the server cools down
		}
		payload, retryable, err := c.doOnce(ctx, tool, args)
		if err == nil {
			c.breakerReport(true)
			return payload, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
		c.breakerReport(false)
	}
	return nil, fmt.Errorf("mcp %s: exhausted retries: %w", tool, lastErr)
}

// breakerAllow rejects calls while the circuit is open.
func (c *Client) breakerAllow() error {
	c.brMu.Lock()
	defer c.brMu.Unlock()
	if !c.brOpenUntil.IsZero() && time.Now().Before(c.brOpenUntil) {
		return ErrCircuitOpen
	}
	return nil
}

// breakerReport tracks consecutive transport failures and opens the circuit
// when they cross the threshold.
func (c *Client) breakerReport(ok bool) {
	c.brMu.Lock()
	defer c.brMu.Unlock()
	if ok {
		c.brFails = 0
		c.brOpenUntil = time.Time{}
		return
	}
	c.brFails++
	if c.brThreshold > 0 && c.brFails >= c.brThreshold {
		c.brOpenUntil = time.Now().Add(c.brCooldown)
		c.brFails = 0
	}
}

// BreakerOpen reports whether the circuit is currently open and until when —
// surfaced on /healthz so the board can say «Туту остывает» honestly.
func (c *Client) BreakerOpen() (bool, time.Time) {
	c.brMu.Lock()
	defer c.brMu.Unlock()
	open := !c.brOpenUntil.IsZero() && time.Now().Before(c.brOpenUntil)
	return open, c.brOpenUntil
}

// polite acquires a concurrency slot and enforces the minimum spacing between
// consecutive requests. Returns a release func.
func (c *Client) polite(ctx context.Context) (func(), error) {
	if c.gate != nil {
		select {
		case c.gate <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	interval := c.minInterval
	if n := len(c.pool); n > 1 {
		// Spacing protects one IP; a pool spreads traffic across N of them.
		interval /= time.Duration(n)
	}
	if interval > 0 {
		c.lastReqMu.Lock()
		wait := time.Until(c.lastReq.Add(interval))
		if wait > 0 {
			c.lastReq = c.lastReq.Add(interval)
		} else {
			c.lastReq = time.Now()
		}
		c.lastReqMu.Unlock()
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				if c.gate != nil {
					<-c.gate
				}
				return nil, ctx.Err()
			}
		}
	}
	return func() {
		if c.gate != nil {
			<-c.gate
		}
	}, nil
}

type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int64          `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  *toolResult     `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// doOnce performs a single request. The returned bool says whether the failure
// is worth retrying.
func (c *Client) doOnce(ctx context.Context, tool string, args any) (json.RawMessage, bool, error) {
	release, err := c.polite(ctx)
	if err != nil {
		return nil, false, err
	}
	defer release()

	argMap, err := toMap(args)
	if err != nil {
		return nil, false, err
	}
	reqBody := rpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "tools/call",
		Params:  map[string]any{"name": tool, "arguments": argMap},
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return nil, false, err // fail before hitting the wire with an empty body
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, false, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	httpClient, report := c.nextHTTP()
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			// The caller went away (cancel/timeout): not the proxy's or the
			// server's fault — don't bench the proxy, don't feed the breaker,
			// don't retry.
			return nil, false, err
		}
		report(false)
		return nil, true, err // network error: retry (next attempt = next proxy)
	}
	report(resp.StatusCode < 500)
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		io.Copy(io.Discard, resp.Body)
		return nil, true, fmt.Errorf("mcp http %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, false, fmt.Errorf("mcp http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	rpc, err := decodeRPC(resp)
	if err != nil {
		return nil, false, err
	}
	if rpc.Error != nil {
		return nil, false, fmt.Errorf("mcp rpc error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}
	if rpc.Result == nil || len(rpc.Result.Content) == 0 {
		return nil, false, fmt.Errorf("mcp %s: empty result", tool)
	}

	text := rpc.Result.Content[0].Text
	if rpc.Result.IsError {
		return nil, false, &ToolError{Tool: tool, Msg: strings.TrimSpace(text)}
	}
	// The payload is itself a JSON document encoded as a string.
	return json.RawMessage(text), false, nil
}

// decodeRPC handles both a plain JSON body and an SSE (text/event-stream) body,
// since the server may negotiate either based on Accept.
func decodeRPC(resp *http.Response) (*rpcResponse, error) {
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return decodeSSE(resp.Body)
	}
	var rpc rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		return nil, fmt.Errorf("decode json-rpc: %w", err)
	}
	return &rpc, nil
}

func decodeSSE(r io.Reader) (*rpcResponse, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(line[5:]))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if data.Len() == 0 {
		return nil, fmt.Errorf("empty sse stream")
	}
	var rpc rpcResponse
	if err := json.Unmarshal([]byte(data.String()), &rpc); err != nil {
		return nil, fmt.Errorf("decode sse json-rpc: %w", err)
	}
	return &rpc, nil
}

func (c *Client) nextID() int64 {
	c.idMu.Lock()
	defer c.idMu.Unlock()
	c.idSeq++
	return c.idSeq
}

func (c *Client) bump(calls, hits int) {
	c.statsMu.Lock()
	c.callsMade += calls
	c.cacheHits += hits
	c.statsMu.Unlock()
}

func (c *Client) cacheGet(key string) (json.RawMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.payload, true
}

func (c *Client) cacheSet(key string, payload json.RawMessage) {
	c.mu.Lock()
	c.cache[key] = cacheEntry{payload: payload, expires: time.Now().Add(c.cacheTTL)}
	c.mu.Unlock()
}

func cacheKey(tool string, args any) string {
	b, _ := json.Marshal(args)
	h := sha256.Sum256(append([]byte(tool+"\x00"), b...))
	return fmt.Sprintf("%x", h[:16])
}

func toMap(args any) (map[string]any, error) {
	if m, ok := args.(map[string]any); ok {
		return m, nil
	}
	b, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
