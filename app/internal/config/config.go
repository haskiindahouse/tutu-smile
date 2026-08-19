// Package config loads runtime configuration from environment variables.
// Secrets (OpenRouter key, Mapbox token) never live in source — they arrive
// via the process environment (see .env.example).
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr string // HTTP listen address, e.g. ":8080"

	MCPEndpoint string        // Tutu MCP JSON-RPC endpoint
	MCPTimeout  time.Duration // per-call timeout
	MCPCacheTTL time.Duration // how long a search result stays fresh in cache
	MCPRetries  int           // retry attempts on transient failure

	// Politeness to the shared MCP (its WAF TLS-bans an unthrottled fan-out —
	// verified live): bounded concurrency, spacing, and a circuit breaker.
	MCPMaxConcurrent   int           // in-flight request cap
	MCPMinInterval     time.Duration // min spacing between consecutive requests
	MCPBreakerFails    int           // consecutive transport failures to open
	MCPBreakerCooldown time.Duration // how long the circuit stays open
	// MCPProxies rotate outbound requests across several IPs (WAF relief).
	// Entries: full URL or host:port:user:pass, comma-separated. Secrets —
	// only via env/.env, never in source.
	MCPProxies []string

	// LLM (OpenRouter) — optional. When the key is empty the vibe mode and
	// human-card enrichment degrade gracefully to deterministic fallbacks.
	// Models is a fallback chain: the first that answers wins.
	OpenRouterKey    string
	OpenRouterModels []string
	LLMTimeout       time.Duration

	MapboxToken string // public token, injected into the frontend

	// Orchestration knobs.
	MaxConcurrency int           // bounded fan-out across guests
	RecheckEvery   time.Duration // background board re-verification period
}

// loadDotEnv reads ./.env (KEY=VALUE lines, # comments) and fills only the
// variables not already present in the process environment — so real env
// always wins and the README's «скопируйте в .env» actually works.
func loadDotEnv() {
	data, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" || os.Getenv(k) != "" {
			continue
		}
		os.Setenv(k, strings.Trim(v, `"'`))
	}
}

func Load() Config {
	loadDotEnv()
	cfg := Config{
		Addr:               env("SMILE_ADDR", ":8080"),
		MCPEndpoint:        env("SMILE_MCP_ENDPOINT", "https://mcp.tutu.ru/mcp"),
		MCPTimeout:         envDur("SMILE_MCP_TIMEOUT", 25*time.Second),
		MCPCacheTTL:        envDur("SMILE_MCP_CACHE_TTL", 90*time.Second),
		MCPRetries:         envInt("SMILE_MCP_RETRIES", 2),
		MCPMaxConcurrent:   envInt("SMILE_MCP_MAX_CONCURRENT", 4),
		MCPMinInterval:     envDur("SMILE_MCP_MIN_INTERVAL", 150*time.Millisecond),
		MCPBreakerFails:    envInt("SMILE_MCP_BREAKER_FAILS", 5),
		MCPBreakerCooldown: envDur("SMILE_MCP_BREAKER_COOLDOWN", 60*time.Second),
		MCPProxies:         envListRaw("SMILE_MCP_PROXIES"),
		OpenRouterKey:      env("OPENROUTER_API_KEY", ""),
		OpenRouterModels:   envList("SMILE_LLM_MODELS", []string{"google/gemini-3.6-flash", "openai/gpt-5.6-luna", "z-ai/glm-5.2"}),
		LLMTimeout:         envDur("SMILE_LLM_TIMEOUT", 30*time.Second),
		MapboxToken:        env("MAPBOX_TOKEN", ""),
		MaxConcurrency:     envInt("SMILE_MAX_CONCURRENCY", 4),
		RecheckEvery:       envDur("SMILE_RECHECK_EVERY", 150*time.Second),
	}
	// A non-positive period would panic time.NewTicker in the recheck loop.
	if cfg.RecheckEvery <= 0 {
		cfg.RecheckEvery = 150 * time.Second
	}
	return cfg
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envList reads a comma-separated list; SMILE_LLM_MODEL is honoured as a
// single-model backward-compatible alias.
func envList(k string, def []string) []string {
	v := os.Getenv(k)
	if v == "" {
		if single := os.Getenv("SMILE_LLM_MODEL"); single != "" {
			return []string{single}
		}
		return def
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

// envListRaw reads a comma-separated list with no default.
func envListRaw(k string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(k), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envDur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
