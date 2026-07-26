// Command cluster-agent runs the per-cluster, read-only Cluster Agent.
//
// It exposes typed read-only tools (see docs/INTEGRATION.md) on :9100 for the
// Tool Gateway to proxy. It never mutates cluster state.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aiops/cluster-agent/internal/datasource"
	"github.com/aiops/cluster-agent/internal/server"
	"github.com/aiops/cluster-agent/internal/tools"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr := env("AIOPS_CLUSTER_AGENT_ADDR", ":9100")
	clusterID := env("AIOPS_CLUSTER_ID", "prod-cn-1")

	// First version: deterministic Mock DataSource. Swap here for a real
	// client-go / Prometheus / Loki / Tempo backed source in the future.
	ds := datasource.NewMock()
	reg := tools.NewRegistry(ds)
	srv := server.New(clusterID, reg, log)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Info("cluster-agent starting", "addr", addr, "cluster_id", clusterID, "datasource", "mock", "mode", "read-only")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	log.Info("cluster-agent shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
