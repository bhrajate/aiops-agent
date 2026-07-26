// control-plane 是 AIOps Agent 的基础设施控制平面单一进程,组合运行:
//   - 公共 API(:8080):前端 + webhook
//   - 内部 API(:8090):Tool Gateway + AI Worker 回写
//   - Incident Manager:消费 signals topic
//   - Trigger Policy / Orchestrator:消费 incidents topic,启动 Temporal 工作流
//   - Outbox Publisher:投递领域事件到 Kafka
//
// 各子系统按文档分平面拆分为独立 internal 包,此处仅做装配。
// 生产可拆为多个独立部署单元;单进程便于本地端到端运行。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aiops/control-plane/internal/agentclient"
	"github.com/aiops/control-plane/internal/api"
	"github.com/aiops/control-plane/internal/bus"
	"github.com/aiops/control-plane/internal/config"
	"github.com/aiops/control-plane/internal/gateway"
	"github.com/aiops/control-plane/internal/incident"
	"github.com/aiops/control-plane/internal/outbox"
	"github.com/aiops/control-plane/internal/store"
	"github.com/aiops/control-plane/internal/temporalx"
	"github.com/aiops/control-plane/internal/trigger"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ---- 业务库(事实源)----
	st, err := store.New(ctx, cfg.DBDSN)
	if err != nil {
		log.Error("connect db failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	log.Info("connected to business database")

	// ---- Temporal(可降级)----
	var wf interface {
		trigger.WorkflowStarter
		api.Signaler
	}
	tc, terr := temporalx.Dial(cfg.TemporalHostPort, cfg.TemporalNS, cfg.TemporalQueue)
	if terr != nil {
		log.Warn("temporal unavailable, running degraded (investigations persist but workflows won't start)", "err", terr)
		wf = temporalx.Noop{}
	} else {
		defer tc.Close()
		wf = tc
		log.Info("connected to temporal", "hostport", cfg.TemporalHostPort, "queue", cfg.TemporalQueue)
	}

	// ---- 事件总线 ----
	publisher := bus.NewPublisher(cfg.KafkaBrokers)
	defer publisher.Close()

	// ---- 组件装配 ----
	agent := agentclient.New(cfg.ClusterAgentURL)
	gw := gateway.New(st, agent, log)
	mgr := incident.New(st, log)
	orch := trigger.NewOrchestrator(st, wf, cfg.InternalURL, cfg.Tenant, log)
	ingress := api.NewIngress(st, cfg.ClusterID, cfg.Tenant, log)

	publicAPI := api.NewPublicAPI(st, ingress, orch, wf, log)
	internalAPI := api.NewInternalAPI(st, gw, log)

	outboxPub := outbox.New(st, publisher, log)

	var wg sync.WaitGroup

	// ---- Outbox 投递循环 ----
	wg.Add(1)
	go func() { defer wg.Done(); outboxPub.Run(ctx, 500*time.Millisecond) }()

	// ---- 消费者:signals → Incident Manager ----
	sigConsumer := bus.NewConsumer(cfg.KafkaBrokers, "signals", "incident-manager")
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer sigConsumer.Close()
		log.Info("incident-manager consuming signals")
		_ = sigConsumer.Run(ctx, mgr.HandleSignal)
	}()

	// ---- 消费者:incidents → Trigger/Orchestrator ----
	incConsumer := bus.NewConsumer(cfg.KafkaBrokers, "incidents", "trigger-policy")
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer incConsumer.Close()
		log.Info("trigger-policy consuming incidents")
		_ = incConsumer.Run(ctx, orch.HandleIncidentEvent)
	}()

	// ---- HTTP 服务 ----
	publicSrv := &http.Server{Addr: cfg.PublicAddr, Handler: publicAPI.Routes(), ReadHeaderTimeout: 10 * time.Second}
	internalSrv := &http.Server{Addr: cfg.InternalAddr, Handler: internalAPI.Routes(), ReadHeaderTimeout: 10 * time.Second}

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Info("public API listening", "addr", cfg.PublicAddr)
		if err := publicSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("public API failed", "err", err)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Info("internal API listening", "addr", cfg.InternalAddr)
		if err := internalSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("internal API failed", "err", err)
		}
	}()

	// ---- 优雅退出 ----
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("shutting down...")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	_ = publicSrv.Shutdown(shutCtx)
	_ = internalSrv.Shutdown(shutCtx)
	cancel()
	wg.Wait()
	log.Info("bye")
}
