// Package api exposes the HTTP surface: a small JSON REST API plus an SSE
// stream for the live board, and it serves the static frontend.
package api

import (
	"io/fs"
	"net/http"

	"github.com/ma4ypic4y/tutu-smile/internal/config"
	"github.com/ma4ypic4y/tutu-smile/internal/event"
	"github.com/ma4ypic4y/tutu-smile/internal/llm"
	"github.com/ma4ypic4y/tutu-smile/internal/mcp"
	"github.com/ma4ypic4y/tutu-smile/internal/orchestrator"
	"github.com/ma4ypic4y/tutu-smile/internal/tutu"
)

type Server struct {
	cfg    config.Config
	store  *event.Store
	orch   *orchestrator.Orchestrator
	mgr    *orchestrator.Manager
	llm    *llm.Client
	mcp    *mcp.Client
	svc    *tutu.Service
	static fs.FS
}

func NewServer(cfg config.Config, store *event.Store, orch *orchestrator.Orchestrator, mgr *orchestrator.Manager, l *llm.Client, m *mcp.Client, svc *tutu.Service, static fs.FS) *Server {
	return &Server{cfg: cfg, store: store, orch: orch, mgr: mgr, llm: l, mcp: m, svc: svc, static: static}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// REST API.
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("POST /api/parse", s.handleParse)
	mux.HandleFunc("POST /api/vibe", s.handleVibe)
	mux.HandleFunc("POST /api/meet", s.handleMeet)
	mux.HandleFunc("POST /api/roulette", s.handleRoulette)
	mux.HandleFunc("POST /api/roulette/pool", s.handleRoulettePool)
	mux.HandleFunc("POST /api/roulette/price", s.handleRoulettePrice)
	mux.HandleFunc("POST /api/spots", s.handleSpots)
	mux.HandleFunc("POST /api/cityplan", s.handleCityPlan)
	mux.HandleFunc("POST /api/events", s.handleCreateEvent)
	mux.HandleFunc("GET /api/events", s.handleListEvents)
	mux.HandleFunc("GET /api/events/{id}", s.handleGetEvent)
	mux.HandleFunc("GET /api/events/{id}/board", s.handleGetBoard)
	mux.HandleFunc("GET /api/events/{id}/stream", s.handleStream)
	mux.HandleFunc("GET /api/events/{id}/ics", s.handleICS)
	mux.HandleFunc("POST /api/events/{id}/recheck", s.handleRecheck)
	mux.HandleFunc("POST /api/events/{id}/guests", s.handleAddGuest)
	mux.HandleFunc("GET /api/events/{id}/join", s.handleJoinInfo)
	mux.HandleFunc("POST /api/events/{id}/join", s.handleJoin)
	mux.HandleFunc("POST /api/events/{id}/amend", s.handleAmend)
	mux.HandleFunc("GET /api/events/{id}/guest/{gid}", s.handleGuestCard)
	mux.HandleFunc("POST /api/events/{id}/guest/{gid}/choose", s.handleGuestChoose)
	mux.HandleFunc("POST /api/events/{id}/guest/{gid}/purchased", s.handleGuestPurchased)
	mux.HandleFunc("POST /api/events/{id}/guest/{gid}/consent", s.handleGuestConsent)
	mux.HandleFunc("POST /api/events/{id}/demo/collapse", s.handleDemoCollapse)

	// Static frontend (SPA fallback to index.html).
	fileServer := http.FileServer(http.FS(s.static))
	mux.Handle("/", s.spaHandler(fileServer))

	return withCORS(mux)
}

// spaHandler serves static files, falling back to index.html for unknown paths
// so client-side routes (e.g. /e/{id}, /g/{id}/{gid}) resolve. Embedded files
// carry no modtime, so browsers must revalidate: an aggressively cached app.js
// once showed a user yesterday's product.
func (s *Server) spaHandler(fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		path := r.URL.Path
		if path == "/" {
			serveIndex(w, r, s.static)
			return
		}
		if f, err := s.static.Open(trimLeadingSlash(path)); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// Unknown path → SPA entrypoint.
		serveIndex(w, r, s.static)
	})
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}

func serveIndex(w http.ResponseWriter, r *http.Request, static fs.FS) {
	data, err := fs.ReadFile(static, "index.html")
	if err != nil {
		http.Error(w, "index not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
