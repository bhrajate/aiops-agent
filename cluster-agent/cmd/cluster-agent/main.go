// Command cluster-agent 运行按集群部署的只读 Cluster Agent。
//
// 它在 :9100 上暴露强类型只读工具(见 docs/INTEGRATION.md)供 Tool Gateway 代理调用,
// 绝不变更集群状态。
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

	// 可插拔的只读数据源。AIOPS_DATASOURCE 选择 mock(默认)或 live
	// (client-go / Prometheus / Loki / Tempo)。live 模式下若上游 URL 或
	// Kubernetes 客户端未配置,则按工具粒度降级。
	ds, mode := datasource.FromEnv()
	reg := tools.NewRegistry(ds)
	srv := server.New(clusterID, reg, log)

	tlsCfg, err := server.TLSConfigFromEnv().Build()
	if err != nil {
		log.Error("invalid mTLS configuration", "err", err)
		os.Exit(1)
	}

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		// 限制请求头大小,避免超大头部在路由之前就耗尽内存
		// (请求体的上限在 handleInvoke 中控制)。
		MaxHeaderBytes: 1 << 20, // 1 MiB
	}

	// 独立端口上的专用明文 HTTP 健康检查端点。当工具端口启用 mTLS
	// (RequireAndVerifyClientCert)时,kubelet 无法提供客户端证书,对 :9100 的
	// HTTPS 探针必然失败。健康端口只暴露 /healthz(不含工具、不含集群数据),
	// 供存活/就绪探针使用。
	healthAddr := env("AIOPS_CLUSTER_AGENT_HEALTH_ADDR", ":9101")
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	healthSrv := &http.Server{Addr: healthAddr, Handler: healthMux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := healthSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("health server failed", "err", err)
		}
	}()

	go func() {
		tlsEnabled := tlsCfg != nil
		log.Info("cluster-agent starting",
			"addr", addr, "health_addr", healthAddr, "cluster_id", clusterID,
			"datasource", mode, "mode", "read-only", "mtls", tlsEnabled)
		var serveErr error
		if tlsEnabled {
			// 证书与私钥已加载进 TLSConfig,这里传空路径即可。
			serveErr = httpSrv.ListenAndServeTLS("", "")
		} else {
			serveErr = httpSrv.ListenAndServe()
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Error("server failed", "err", serveErr)
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
	_ = healthSrv.Shutdown(shutdownCtx)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
