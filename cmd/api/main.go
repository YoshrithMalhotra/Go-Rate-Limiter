package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/ritik-kharya/gobouncer/internal/config"
	"github.com/ritik-kharya/gobouncer/internal/handlers"
	"github.com/ritik-kharya/gobouncer/internal/limiter"
	"github.com/ritik-kharya/gobouncer/internal/metrics"
	"github.com/ritik-kharya/gobouncer/internal/policy"
)

// Build-time variables injected via ldflags by GoReleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	setupLogger()

	cfg := config.Load()
	rdb := setupRedis(cfg.RedisAddr)
	algos := setupAlgorithms(rdb, cfg.FailOpen)
	policyStore := setupPolicies(cfg.PolicyFile)
	metricsRegistry := metrics.NewRegistry()

	mux := setupRoutes(rdb, algos, policyStore, metricsRegistry)

	srv := &http.Server{
		Addr:         cfg.ServerPort,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	startServer(srv, cfg.ServerPort)
	waitForShutdown(srv)
}

func setupLogger() {
	// Structured logger — JSON in prod, text for local dev
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)
}

func setupRedis(redisAddr string) *redis.Client {
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Error("cannot connect to redis", "addr", redisAddr, "error", err)
		os.Exit(1)
	}
	slog.Info("redis connected", "addr", redisAddr)
	return rdb
}

func setupAlgorithms(rdb *redis.Client, failOpen bool) handlers.Algorithms {
	algos := handlers.Algorithms{
		SlidingWindow: limiter.NewSlidingWindow(rdb, limiter.WithSlidingWindowFailOpen(failOpen)),
		GCRA:          limiter.NewGCRA(rdb, limiter.WithGCRAFailOpen(failOpen)),
	}
	slog.Info("algorithms ready", "default", "sliding_window", "fail_open", failOpen)
	return algos
}

func setupPolicies(policyFile string) *policy.MemoryStore {
	policyStore := policy.DefaultStore()
	if policyFile != "" {
		loadedStore, err := policy.LoadFile(policyFile)
		if err != nil {
			slog.Error("cannot load policy file", "path", policyFile, "error", err)
			os.Exit(1)
		}
		policyStore = loadedStore
	}
	slog.Info("policies loaded", "count", len(policyStore.List()))
	return policyStore
}

func setupRoutes(rdb *redis.Client, algos handlers.Algorithms, policyStore *policy.MemoryStore, metricsRegistry *metrics.Registry) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head>
	<title>GoBouncer</title>
</head>
<body>
	<h1>Welcome to GoBouncer!</h1>
	<p>This is the GoBouncer rate-limiting service.</p>
</body>
</html>`)
	})
	mux.HandleFunc("/check", handlers.NewCheckHandler(algos, policyStore, metricsRegistry))
	mux.HandleFunc("/policies", handlers.NewPoliciesHandler(policyStore))
	mux.HandleFunc("/metrics", metricsRegistry.Handler())
	// Liveness — the process is up. Never touches Redis, so a Redis outage
	// does not get the container restarted.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "OK")
	})
	// Readiness — the process can actually serve checks. Returns 503 when
	// Redis is unreachable so orchestrators stop routing traffic here.
	mux.HandleFunc("/ready", newReadyHandler(rdb))
	return mux
}

func newReadyHandler(rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		defer cancel()

		if err := rdb.Ping(ctx).Err(); err != nil {
			slog.Warn("readiness check failed", "error", err)
			http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "READY")
	}
}

func startServer(srv *http.Server, port string) {
	// Start server in a goroutine
	go func() {
		slog.Info("server starting", "port", port, "version", version, "commit", commit, "date", date)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()
}

func waitForShutdown(srv *http.Server) {
	// Graceful shutdown — wait for SIGINT or SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	slog.Info("shutdown signal received", "signal", sig)

	// Give in-flight requests 10 seconds to complete
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := srv.Shutdown(shutdownCtx)
	cancel()

	if err != nil {
		slog.Error("forced shutdown", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped gracefully")
}
