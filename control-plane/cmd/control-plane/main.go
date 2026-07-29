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
	"github.com/aiops/control-plane/internal/httpx"
	"github.com/aiops/control-plane/internal/incident"
	"github.com/aiops/control-plane/internal/objstore"
	"github.com/aiops/control-plane/internal/obsquery"
	"github.com/aiops/control-plane/internal/outbox"
	"github.com/aiops/control-plane/internal/retention"
	"github.com/aiops/control-plane/internal/store"
	"github.com/aiops/control-plane/internal/telemetry"
	"github.com/aiops/control-plane/internal/temporalx"
	"github.com/aiops/control-plane/internal/trigger"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	// 子命令分发。`migrate` 不启动任何服务,执行完即退出——生产由 Helm
	// pre-install/pre-upgrade Job 调用它,与常驻进程共用镜像以保证 SQL 与代码同版本。
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrateCmd(log, os.Args[2:])
		return
	}

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

	// ---- schema 版本校验(fail-fast)----
	// 刻意**只校验、不自动迁移**:多副本滚动更新时,自动迁移会让尚未替换的旧副本
	// 面对新 schema。迁移是独立步骤(Helm pre-upgrade Job / CI),此处只确认库已就绪,
	// 落后就立刻拒绝启动——而不是带着不匹配的 schema 跑到第一次查询才炸。
	// 开发环境可设 AIOPS_AUTO_MIGRATE=true 让单副本自行迁移,省去手动步骤。
	if err := ensureSchema(ctx, cfg, log); err != nil {
		log.Error("schema check failed", "err", err)
		os.Exit(1)
	}

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
	// 队列积压指标(P4):在抓取时查库,而非后台轮询 + Gauge.Set()。
	// 轮询失败会让 Gauge 里留着上一次的成功值——数据库挂了、仪表盘还显示健康数字。
	// 只在 internal 角色注册:/metrics 挂在内部 API 上,别的角色注册了也没人抓,
	// 反而让不暴露端点的副本白查数据库。
	if cfg.HasRole("internal") {
		metrics.RegisterQueue(queueStatsAdapter{st: st}, log)
		log.Info("queue depth metrics registered (scraped on demand)")
	}
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

	// ---- 共享观测后端(控制面直连 Prometheus/Loki/Tempo)----
	// 这些后端是多集群共用的中心服务,不在任一 K8s 集群内,因此不再绕经集群 agent:
	// 少一跳网络、少一个必经故障点(故障时不会同时失去 metrics/logs/traces)、
	// 凭据只此一份。查询仍在 Gateway 之后,范围注入/脱敏/审计/预算一律不变。
	// 集群 label 名按后端各自的命名法校验(点号在 PromQL/LogQL 里是语法错误,
	// 而 Tempo 的 OTel 语义约定恰恰用点号)。语法非法在此 fail-fast,
	// 而不是留到运行期每次查询都失败。
	clusterLabels := obsquery.ConfigFromEnv().ClusterLabels
	if err := clusterLabels.Validate(); err != nil {
		log.Error("集群 label 配置非法", "err", err)
		os.Exit(1)
	}
	obsQuerier, obsMode := obsquery.FromEnv()
	obsClient := gateway.ObsQuerier(obsQuerier)
	if obsMode == "live" {
		log.Info("observability backends connected directly", "mode", obsMode,
			"cluster_labels", clusterLabels.Describe())
		// 各后端都有非空默认值,所以经环境变量**唯一**能出现"不强制"的路径是
		// 显式 DISABLED。这条路径值得一条醒目告警:它把跨集群串数据的可能性
		// 交给了"后端确为单集群专用"这个人工判断,判断错了在诊断结论里看不出来
		// ——证据齐全、逻辑自洽,只是来自错误的集群。
		if len(clusterLabels.Unenforced()) > 0 {
			log.Warn("集群维度隔离已显式关闭(AIOPS_CLUSTER_LABEL_DISABLED=true):" +
				"仅当观测后端确为本集群专用时才安全;若为多集群共享后端," +
				"RCA 会读到其他集群的同名 namespace")
		}
	} else {
		log.Warn("observability datasource is MOCK (no AIOPS_PROM_URL/LOKI_URL/TEMPO_URL); " +
			"metrics/logs/traces return deterministic demo data — 切勿用于生产")
	}

	// ---- 组件装配 ----
	gw := gateway.New(st, agents, obsClient, rawStore, metrics, log)
	mgr := incident.New(st, cfg.CorrelationWindowSec, metrics, log)
	orch := trigger.NewOrchestrator(st, wf, cfg.InternalURL, cfg.Tenant,
		trigger.Limits{CooldownSec: cfg.CooldownSec, MaxActive: cfg.MaxActivePerTenant}, log)
	// 信号入口限流(F6):告警风暴是预期故障模式,ingress 之前只有 2MB body 上限。
	// nil(未配置)即不限流。
	ingressLimiter := httpx.NewTokenBucket(cfg.IngressRatePerSec, cfg.IngressBurst)
	if ingressLimiter != nil {
		log.Info("signal ingress rate limiting enabled",
			"rate_per_sec", cfg.IngressRatePerSec, "burst", cfg.IngressBurst)
	}
	ingress := api.NewIngress(st, cfg.ClusterID, cfg.Tenant, cfg.WebhookSecret, metrics, ingressLimiter, log)

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

	// ---- 数据保留清理(F4,role: janitor)----
	// 高写入表(signals/events/audit_log/outbox/…)此前无界增长。Janitor 分批清理,
	// 只删终态数据;多副本下靠 PG advisory lock 互斥,启用多个副本也安全。
	if cfg.Retention.Enabled && cfg.HasRole("janitor") {
		jan := retention.New(st, retention.Config{
			SignalDays:      cfg.Retention.SignalDays,
			EventDays:       cfg.Retention.EventDays,
			AuditDays:       cfg.Retention.AuditDays,
			OutboxDays:      cfg.Retention.OutboxDays,
			DeadLetterDays:  cfg.Retention.DeadLetterDays,
			IdempotencyDays: cfg.Retention.IdempotencyDays,
			CaseDays:        cfg.Retention.CaseDays,
			IntervalSec:     cfg.Retention.IntervalSec,
			BatchSize:       cfg.Retention.BatchSize,
		}, metrics, log)
		wg.Add(1)
		go func() { defer wg.Done(); jan.Run(ctx) }()
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
