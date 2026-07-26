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
	"github.com/aiops/control-plane/internal/auth"
	"github.com/aiops/control-plane/internal/bus"
	"github.com/aiops/control-plane/internal/config"
	"github.com/aiops/control-plane/internal/gateway"
	"github.com/aiops/control-plane/internal/incident"
	"github.com/aiops/control-plane/internal/objstore"
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

	// ---- 认证器(SECURITY §1)----
	authn := auth.NewAuthenticator(auth.Config{
		Mode:     auth.Mode(cfg.AuthMode),
		HS256Key: cfg.HS256Secret,
		Issuer:   cfg.Issuer,
		Audience: cfg.Audience,
		// OIDC verifier 生产接入(JWKS);此处 hs256/disabled 无需
	})
	if cfg.AuthMode == "disabled" {
		log.Warn("AUTH DISABLED — 仅限本地测试,切勿用于生产")
	}

	// ---- 对象存储(证据快照,SECURITY §6;不可用则降级)----
	var rawStore gateway.RawSnapshotStore
	if os, oerr := objstore.New(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL); oerr != nil {
		log.Warn("object storage unavailable, evidence snapshots disabled (summaries still persisted)", "err", oerr)
	} else {
		rawStore = os
		log.Info("connected to object storage", "bucket", cfg.S3Bucket)
	}

	// ---- Cluster Agent 客户端(mTLS 可选,SECURITY §3)----
	var agent *agentclient.Client
	if cfg.AgentMTLSEnabled {
		ac, aerr := agentclient.NewMTLS(cfg.ClusterAgentURL, agentclient.MTLSConfig{
			ClientCert: cfg.AgentClientCert, ClientKey: cfg.AgentClientKey, CA: cfg.AgentCA,
		})
		if aerr != nil {
			log.Error("mTLS client init failed", "err", aerr)
			os.Exit(1)
		}
		agent = ac
		log.Info("cluster-agent client using mTLS", "url", cfg.ClusterAgentURL)
	} else {
		agent = agentclient.New(cfg.ClusterAgentURL)
	}

	// ---- 组件装配 ----
	gw := gateway.New(st, agent, rawStore, log)
	mgr := incident.New(st, log)
	orch := trigger.NewOrchestrator(st, wf, cfg.InternalURL, cfg.Tenant, log)
	ingress := api.NewIngress(st, cfg.ClusterID, cfg.Tenant, cfg.WebhookSecret, log)

	agentScope := auth.AgentServiceScope{Clusters: []string{cfg.ClusterID}}
	publicAPI := api.NewPublicAPI(st, ingress, orch, wf, authn, agentScope, log)
	internalAPI := api.NewInternalAPI(st, gw, cfg.InternalToken, log)

	outboxPub := outbox.New(st, publisher, log)

	var wg sync.WaitGroup

	// ---- Outbox 投递循环 ----
	wg.Add(1)
	go func() { defer wg.Done(); outboxPub.Run(ctx, 500*time.Millisecond) }()

	// DLQ 回调:重试超限的消息落 dead_letters 表 + 审计告警(SECURITY §7)
	deadLetter := func(ctx context.Context, topic, key string, value []byte, lastErr error, attempts int) error {
		errMsg := ""
		if lastErr != nil {
			errMsg = lastErr.Error()
		}
		if err := st.InsertDeadLetter(ctx, topic, key, value, errMsg, attempts); err != nil {
			return err
		}
		st.Audit(ctx, cfg.Tenant, "system", "dead_letter", topic, key, "error", nil,
			map[string]any{"attempts": attempts, "error": errMsg})
		log.Warn("message dead-lettered", "topic", topic, "key", key, "attempts", attempts, "err", errMsg)
		return nil
	}

	// ---- 消费者:signals → Incident Manager ----
	sigConsumer := bus.NewConsumer(cfg.KafkaBrokers, "signals", "incident-manager")
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer sigConsumer.Close()
		log.Info("incident-manager consuming signals")
		_ = sigConsumer.RunWithOptions(ctx, mgr.HandleSignal, bus.RunOptions{
			Topic: "signals", MaxAttempts: cfg.MaxDeliveryAttempts, OnDeadLetter: deadLetter,
		})
	}()

	// ---- 消费者:incidents → Trigger/Orchestrator ----
	incConsumer := bus.NewConsumer(cfg.KafkaBrokers, "incidents", "trigger-policy")
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer incConsumer.Close()
		log.Info("trigger-policy consuming incidents")
		_ = incConsumer.RunWithOptions(ctx, orch.HandleIncidentEvent, bus.RunOptions{
			Topic: "incidents", MaxAttempts: cfg.MaxDeliveryAttempts, OnDeadLetter: deadLetter,
		})
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
