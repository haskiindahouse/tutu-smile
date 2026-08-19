// Command smile runs the «Улыбка» web service: a single binary that serves the
// frontend and orchestrates Tutu MCP searches to gather event guests to one
// place by one deadline.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/api"
	"github.com/ma4ypic4y/tutu-smile/internal/config"
	"github.com/ma4ypic4y/tutu-smile/internal/event"
	"github.com/ma4ypic4y/tutu-smile/internal/llm"
	"github.com/ma4ypic4y/tutu-smile/internal/mcp"
	"github.com/ma4ypic4y/tutu-smile/internal/orchestrator"
	"github.com/ma4ypic4y/tutu-smile/internal/planner"
	"github.com/ma4ypic4y/tutu-smile/internal/tutu"
	"github.com/ma4ypic4y/tutu-smile/web"
)

func main() {
	cfg := config.Load()

	mcpClient := mcp.New(cfg.MCPEndpoint, cfg.MCPTimeout,
		mcp.WithRetries(cfg.MCPRetries),
		mcp.WithCacheTTL(cfg.MCPCacheTTL),
		mcp.WithPoliteness(cfg.MCPMaxConcurrent, cfg.MCPMinInterval),
		mcp.WithBreaker(cfg.MCPBreakerFails, cfg.MCPBreakerCooldown),
		mcp.WithProxies(cfg.MCPProxies, cfg.MCPTimeout),
	)
	// Прозвон прокси-пула на старте: мёртвые IP садятся на скамейку до того,
	// как первый пользовательский запрос заплатит за их обнаружение.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		mcpClient.ProbeProxies(ctx)
	}()

	svc := tutu.NewService(mcpClient)
	plan := planner.New(svc)
	llmClient := llm.New(cfg.OpenRouterKey, cfg.OpenRouterModels, cfg.LLMTimeout)
	orch := orchestrator.New(plan, svc, llmClient, cfg.MaxConcurrency)
	store := event.NewStore()
	mgr := orchestrator.NewManager(orch, store, cfg.RecheckEvery)
	defer mgr.StopAll()

	srv := api.NewServer(cfg, store, orch, mgr, llmClient, mcpClient, svc, web.FS())

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("«Улыбка» слушает на %s  (LLM: %v, MCP: %s)", cfg.Addr, llmClient.Enabled(), cfg.MCPEndpoint)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("останавливаюсь…")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mgr.StopAll()
	_ = httpServer.Shutdown(ctx)
}
