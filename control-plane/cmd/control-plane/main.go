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
	"github.com/aiops/control-plane/internal/telemetry"
	"github.com/aiops/control-plane/internal/temporalx"
	"github.com/aiops/control-plane/internal/trigger"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	cfg := config.Load()

	// 启动配置校验(SECURITY §1/§2/§4):生产模式下弱/缺失安全配置直接 fail-fast。
	if err := cfg.Validate(); err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	log.Info("configuration validated", "env", cfg.Env, "auth_mode", cfg.AuthMode)

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

	// ---- 可观测性(架构第 16 节)----
	metrics := telemetry.New()
	shutdownTracing, terr2 := telemetry.InitTracing(ctx, cfg.ServiceName, cfg.OTLPEndpoint)
	if terr2 != nil {
		log.Warn("tracing init failed (continuing without export)", "err", terr2)
		shutdownTracing = func(context.Context) error { return nil }
	} else if cfg.OTLPEndpoint != "" {
		log.Info("otlp tracing enabled", "endpoint", cfg.OTLPEndpoint)
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

	// ---- 认证器(SECURITY §1)----
	authCfg := auth.Config{
		Mode:     auth.Mode(cfg.AuthMode),
		HS256Key: cfg.HS256Secret,
		Issuer:   cfg.Issuer,
		Audience: cfg.Audience,
	}
	if cfg.AuthMode == "oidc" {
		// 装配真实 JWKS verifier(此前为空壳)
		authCfg.OIDCVerif = auth.NewJWKSVerifier(cfg.OIDCJWKSURL, cfg.OIDCIssuer, cfg.OIDCAudience)
		log.Info("oidc auth enabled", "issuer", cfg.OIDCIssuer, "jwks", cfg.OIDCJWKSURL)
	}
	authn := auth.NewAuthenticator(authCfg)
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

	// ---- Cluster Agent 客户端注册表(每集群一个 Agent;mTLS 可选,SECURITY §3)----
	mtls := agentclient.MTLSConfig{ClientCert: cfg.AgentClientCert, ClientKey: cfg.AgentClientKey, CA: cfg.AgentCA}
	newAgent := func(url string) (*agentclient.Client, error) {
		if cfg.AgentMTLSEnabled {
			return agentclient.NewMTLS(url, mtls)
		}
		return agentclient.New(url), nil
	}
	agentURLs, perr := agentclient.ParseAgentMap(cfg.ClusterAgents)
	if perr != nil {
		log.Error("invalid AIOPS_CLUSTER_AGENTS", "err", perr)
		os.Exit(1)
	}
	byCluster := make(map[string]*agentclient.Client, len(agentURLs))
	for cid, url := range agentURLs {
		c, aerr := newAgent(url)
		if aerr != nil {
			log.Error("cluster-agent client init failed", "cluster", cid, "err", aerr)
			os.Exit(1)
		}
		byCluster[cid] = c
	}
	var fallback *agentclient.Client
	if len(byCluster) == 0 {
		// 未配置多集群映射:单集群兼容模式
		fc, aerr := newAgent(cfg.ClusterAgentURL)
		if aerr != nil {
			log.Error("cluster-agent client init failed", "err", aerr)
			os.Exit(1)
		}
		fallback = fc
	}
	agents := agentclient.NewRegistry(byCluster, fallback)
	if len(byCluster) > 0 {
		log.Info("cluster-agent routing enabled", "clusters", agents.Clusters(), "mtls", cfg.AgentMTLSEnabled)
	} else {
		log.Info("cluster-agent single-cluster mode", "url", cfg.ClusterAgentURL, "mtls", cfg.AgentMTLSEnabled)
	}

	// ---- 中心 Observability Agent(查询共享 Prometheus/Loki/Tempo)----
	// 观测后端是多集群共用的中心服务,不在任一集群内:凭据集中一份、
	// 不进 ai-worker,查询仍经 Gateway 强制范围注入与审计。
	var obsInvoker gateway.ToolInvoker
	if cfg.ObservabilityAgentURL != "" {
		if cfg.AgentMTLSEnabled {
			oc, oerr := agentclient.NewMTLS(cfg.ObservabilityAgentURL, mtls)
			if oerr != nil {
				log.Error("observability agent mTLS init failed", "err", oerr)
				os.Exit(1)
			}
			obsInvoker = oc
		} else {
			obsInvoker = agentclient.New(cfg.ObservabilityAgentURL)
		}
		log.Info("observability queries routed to central agent", "url", cfg.ObservabilityAgentURL)
	} else {
		log.Info("observability queries use per-cluster agents (no central observability agent configured)")
	}

	// ---- 组件装配 ----
	gw := gateway.New(st, agents, obsInvoker, rawStore, metrics, log)
	mgr := incident.New(st, cfg.CorrelationWindowSec, log)
	orch := trigger.NewOrchestrator(st, wf, cfg.InternalURL, cfg.Tenant,
		trigger.Limits{CooldownSec: cfg.CooldownSec, MaxActive: cfg.MaxActivePerTenant}, log)
	ingress := api.NewIngress(st, cfg.ClusterID, cfg.Tenant, cfg.WebhookSecret, metrics, log)

	agentScope := auth.AgentServiceScope{Clusters: []string{cfg.ClusterID}}
	publicAPI := api.NewPublicAPI(st, ingress, orch, wf, authn, agentScope, cfg.CORSOrigins, log)
	internalAPI := api.NewInternalAPI(st, gw, cfg.InternalToken, cfg.IsProduction(), metrics.Handler(), log)

	outboxPub := outbox.New(st, publisher, cfg.MaxDeliveryAttempts, log)

	var wg sync.WaitGroup
	log.Info("enabled roles", "roles", cfg.Roles)

	// ---- Outbox 投递循环(role: outbox)----
	if cfg.HasRole("outbox") {
		wg.Add(1)
		go func() { defer wg.Done(); outboxPub.Run(ctx, 500*time.Millisecond) }()
	}

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
		metrics.IncDeadLetter(topic)
		log.Warn("message dead-lettered", "topic", topic, "key", key, "attempts", attempts, "err", errMsg)
		return nil
	}

	// ---- 消费者:signals → Incident Manager(role: ingest)----
	if cfg.HasRole("ingest") {
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
	}

	// ---- 孤儿调查对账(A2,role: trigger)----
	// 崩溃窗口:CreateInvestigation 与 wf.Start 非原子,之间被杀会留下永远 queued
	// 且无 run_id 的调查。启动时立即对账一次(覆盖重启恢复),之后周期性扫描。
	if cfg.HasRole("trigger") {
		rec := trigger.NewReconciler(st, wf, cfg.InternalURL, cfg.ReconcileGraceSec, log)
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec.Run(ctx, time.Duration(cfg.ReconcileIntervalSec)*time.Second)
		}()
	}

	// ---- 消费者:incidents → Trigger/Orchestrator(role: trigger)----
	if cfg.HasRole("trigger") {
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
	}

	// ---- HTTP 服务(role: api / internal)----
	// 注:metrics 端点挂在内部 API 上;只跑 api 角色的副本不暴露 /metrics,
	// 由 internal 角色副本提供采集端点。
	var publicSrv, internalSrv *http.Server
	if cfg.HasRole("api") {
		publicSrv = &http.Server{Addr: cfg.PublicAddr, Handler: publicAPI.Routes(), ReadHeaderTimeout: 10 * time.Second}
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("public API listening", "addr", cfg.PublicAddr)
			if err := publicSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("public API failed", "err", err)
			}
		}()
	}
	if cfg.HasRole("internal") {
		internalSrv = &http.Server{Addr: cfg.InternalAddr, Handler: internalAPI.Routes(), ReadHeaderTimeout: 10 * time.Second}
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("internal API listening", "addr", cfg.InternalAddr)
			if err := internalSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("internal API failed", "err", err)
			}
		}()
	}

	// ---- 优雅退出 ----
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("shutting down...")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if publicSrv != nil {
		_ = publicSrv.Shutdown(shutCtx)
	}
	if internalSrv != nil {
		_ = internalSrv.Shutdown(shutCtx)
	}
	cancel()
	wg.Wait()
	log.Info("bye")
}
