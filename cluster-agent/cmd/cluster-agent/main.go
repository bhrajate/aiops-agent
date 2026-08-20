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
	"github.com/aiops/cluster-agent/internal/eventwatch"
	"github.com/aiops/cluster-agent/internal/server"
	"github.com/aiops/cluster-agent/internal/tools"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr := env("AIOPS_CLUSTER_AGENT_ADDR", ":9100")
	clusterID := env("AIOPS_CLUSTER_ID", "prod-cn-1")

	// 可插拔的只读数据源。AIOPS_DATASOURCE 选择 mock(默认)或 live(client-go)。
	// live 模式下若 Kubernetes 客户端未配置,则按工具粒度降级(返回 unavailable)。
	//
	// 生产模式(AIOPS_ENV=production)下解析出 mock 会 fail-fast:mock 产出虚构但
	// 自洽的假证据,而它会一路走到"有证据支撑"的诊断结论里,在结论上看不出来。
	// 默认值就是 mock,所以漏配和显式配 mock 一样被拒。
	ds, mode, err := datasource.FromEnv()
	if err != nil {
		log.Error("invalid datasource configuration", "err", err)
		os.Exit(1)
	}
	reg := tools.NewRegistry(ds)
	srv := server.New(clusterID, reg, log)

	// K8s Event watch(可选,默认关闭)。补的是能力边界里那条
	// "仅 pull —— 无主动上报、无 Event watch:瞬时事件超出查询时间窗或被 K8s
	// 回收即不可得"。没有告警规则覆盖的故障此前根本不会被看见。
	//
	// 配置错误在这里 **fail-fast**:mock 数据源会合成假 signal,而假 signal 会
	// 创建真 incident 并可能拉起自动调查 —— 值班人员会去排查一个不存在的故障。
	// 这比假 evidence 严重(后者只污染一次调查的结论)。
	var watcher *eventwatch.Watcher
	if eventwatch.Enabled() {
		ewCfg, err := eventwatch.ConfigFromEnv(clusterID, mode)
		if err != nil {
			log.Error("invalid event watch configuration", "err", err)
			os.Exit(1)
		}
		kc, err := datasource.KubeClient(ds)
		if err != nil {
			log.Error("event watch 需要可用的 Kubernetes 客户端", "err", err)
			os.Exit(1)
		}
		watcher = eventwatch.New(kc, ewCfg, log)
	}

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

	// 独立 goroutine 跑 watch。Run 内部 recover 且永不返回 error ——
	// event watch 的任何问题都不该影响 :9100 上的只读工具,那是 agent 的主职责
	// ("失败只降级、不影响既有路径")。
	if watcher != nil {
		go watcher.Run(ctx)
	}

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
