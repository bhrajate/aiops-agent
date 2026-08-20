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
	"github.com/aiops/control-plane/internal/slo"
	"github.com/aiops/control-plane/internal/store"
	"github.com/aiops/control-plane/internal/telemetry"
	"github.com/aiops/control-plane/internal/temporalx"
	"github.com/aiops/control-plane/internal/topology"
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

	// `validate-config` 只跑启动校验后退出,不连任何基础设施 —— 供 CI 与上线前
	// dry-run 回答"这份环境变量能不能用于生产",那些场景通常没有生产库可连。
	if len(os.Args) > 1 && os.Args[1] == "validate-config" {
		runValidateConfigCmd(log)
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

	// ---- 单租户边界护栏 ----
	// 本系统的设计范围是单租户部署(tenant_id 为未来多租户预留),读路径**不按
	// tenant_id 过滤**。把两个租户指向同一个库时,系统会照常跑,而
	// GET /v1/incidents 会把两边的 incident 一起返回 —— ABAC 拦不住(它按
	// cluster/namespace 过滤,而不同租户完全可能用同名 namespace),
	// 审计里每条也都是"合法用户读了存在的 incident"。没有任何症状。
	//
	// 生产 fail-fast;非生产只警告 —— 开发库里混着各种测试租户是常态,
	// 在那里硬拒会让人为了起服务去删数据,而那比警告更糟。
	if err := st.CheckSingleTenant(ctx, cfg.Tenant); err != nil {
		if cfg.IsProduction() {
			log.Error("单租户边界校验失败", "err", err)
			os.Exit(1)
		}
		log.Warn("单租户边界校验未通过(非生产,仅警告)", "err", err)
	}

	// ---- Temporal(可降级)----
	var wf interface {
		trigger.WorkflowStarter
		api.Signaler
	}
	runTimeout := time.Duration(cfg.TemporalRunTimeoutSec) * time.Second
	tc, terr := temporalx.Dial(cfg.TemporalHostPort, cfg.TemporalNS, cfg.TemporalQueue, runTimeout)
	if terr != nil {
		// 区分两类失败:连不上是可降级的运行时状况;配置非法不是 —— 降级会让
		// 运维只看到一条 warn,而工作流永远不启动,日志里也看不出是配错了。
		//
		// 正常路径上 cfg.Validate() 已经先拦住已知的配置问题(那里能一次报出
		// 全部问题,且不产生任何 I/O)。这里是**兜底**:temporalx 日后新增的任何
		// 配置校验都自动走 fail-fast,而不是被降级路径静默吞掉。
		if errors.Is(terr, temporalx.ErrConfig) {
			log.Error("invalid temporal configuration", "err", terr)
			os.Exit(1)
		}
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
	// 拓扑关联:回填 topology_refs(此前恒为 '[]')并链接调用链上相邻的活跃 incident。
	// 只在 ingest 角色启用增强(它在信号处理路径上),同步循环单独按 topology 角色。
	var topoSyncer *topology.Syncer
	if cfg.TopologyEnabled {
		corr := topology.New(st, topology.Config{
			MaxEdgeAgeSec:     cfg.TopologyMaxEdgeAge,
			MinConfidence:     cfg.TopologyMinConf,
			MinLinkConfidence: cfg.TopologyMinLinkConf,
		}, log)
		mgr = mgr.WithTopology(corr)
		// 同步依赖 Prometheus(Tempo 的 service graph 指标落在那里)。
		// 用 mock 数据源时不启用:那会把假拓扑写进库,而假拓扑会产出
		// 看似合理的错误关联 —— 比没有拓扑更糟。
		if live, ok := obsQuerier.(*obsquery.Client); ok && live.HasPrometheus() {
			topoSyncer = topology.NewSyncer(st, live, cfg.Tenant, cfg.ClusterID,
				time.Duration(cfg.TopologySyncSec)*time.Second, log).WithMetrics(metrics)
		} else {
			log.Warn("拓扑同步未启用:需要真实 Prometheus(mock 数据源会写入假拓扑," +
				"而假拓扑会产出看似合理的错误关联)")
		}
	}
	// 自动触发策略(F7):此前一律触发,每个 incident 都烧一次 triage 调用。
	// 跳过的仍入库、仍可人工发起调查,并写审计 + 指标。
	autoPolicy := trigger.AutoPolicyConfig{
		TriggerAll:                 cfg.AutoTriggerAll,
		AlwaysSeverities:           toSet(cfg.AutoTriggerAlwaysSeverities),
		SkipSeverities:             toSet(cfg.AutoTriggerSkipSeverities),
		BurstSignalCount:           cfg.AutoTriggerBurstSignals,
		TriggerOnChangeCorrelation: cfg.AutoTriggerOnChange,
	}
	orch := trigger.NewOrchestrator(st, wf, cfg.InternalURL, cfg.Tenant,
		trigger.Limits{
			CooldownSec: cfg.CooldownSec, MaxActive: cfg.MaxActivePerTenant,
			AutoPolicy: autoPolicy,
		}, log).WithMetrics(metrics)
	if cfg.AutoTriggerAll {
		log.Warn("AIOPS_AUTO_TRIGGER_ALL=true:每个 incident 都会消耗一次分诊模型调用" +
			"(含 P4 单信号),仅用于回退或对照")
	} else {
		log.Info("auto trigger policy",
			"always", cfg.AutoTriggerAlwaysSeverities, "skip", cfg.AutoTriggerSkipSeverities,
			"burst_signals", cfg.AutoTriggerBurstSignals, "on_change", cfg.AutoTriggerOnChange)
	}
	// 信号入口限流(F6):告警风暴是预期故障模式,ingress 之前只有 2MB body 上限。
	// nil(未配置)即不限流。
	ingressLimiter := httpx.NewTokenBucket(cfg.IngressRatePerSec, cfg.IngressBurst)
	if ingressLimiter != nil {
		log.Info("signal ingress rate limiting enabled",
			"rate_per_sec", cfg.IngressRatePerSec, "burst", cfg.IngressBurst)
	}
	ingress := api.NewIngress(st, cfg.ClusterID, cfg.Tenant, cfg.WebhookSecret, metrics, ingressLimiter, log)

	agentScope := auth.AgentServiceScope{Clusters: []string{cfg.ClusterID}}
	publicAPI := api.NewPublicAPI(st, ingress, orch, wf, authn, agentScope, cfg.CORSOrigins, log).
		WithFeedbackMetrics(metrics).WithGoldenMetrics(metrics).WithTenant(cfg.Tenant)
	// 成效与成本指标(F10):usage 此前只落库、从不导出,模型花了多少钱、
	// 诊断多快、结论被采纳多少,在 Prometheus 上完全不可见。
	internalAPI := api.NewInternalAPI(st, gw, cfg.InternalToken, cfg.IsProduction(), metrics.Handler(), log).
		WithOutcomeMetrics(metrics)

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
			TopologyDays:    cfg.Retention.TopologyDays,
			IdempotencyDays: cfg.Retention.IdempotencyDays,
			CaseDays:        cfg.Retention.CaseDays,
			IntervalSec:     cfg.Retention.IntervalSec,
			BatchSize:       cfg.Retention.BatchSize,
		}, metrics, log)
		wg.Add(1)
		go func() { defer wg.Done(); jan.Run(ctx) }()
	}

	// ---- SLO 燃尽率监视(role: slo)----
	// 主动异常检测:此前系统完全被动,没有告警规则覆盖的缓慢退化看不见。
	// 检测到燃尽后合成 signal 走既有入口(自动获得两层聚合/触发策略/幂等/审计)。
	if cfg.SLOEnabled && cfg.HasRole("slo") {
		slis, serr := slo.LoadSLIs(cfg.SLODefinitions, cfg.SLODefinitionsPath)
		if serr != nil {
			// 配置错误 fail-fast:静默跳过会让运维以为 SLO 在监视,而实际没有。
			log.Error("SLO 定义非法", "err", serr,
				"example", slo.ExampleSLIsJSON)
			os.Exit(1)
		}
		live, ok := obsQuerier.(*obsquery.Client)
		switch {
		case len(slis) == 0:
			log.Warn("AIOPS_SLO_ENABLED=true 但未提供任何 SLI 定义:" +
				"SLO 监视不会做任何事。设 AIOPS_SLO_DEFINITIONS 或 _PATH")
		case !ok || !live.HasPrometheus():
			log.Warn("SLO 监视未启用:需要真实 Prometheus(mock 数据源会产出假故障)")
		default:
			watcher := slo.NewWatcher(live, st, slis, cfg.Tenant, cfg.ClusterID,
				time.Duration(cfg.SLOIntervalSec)*time.Second, log).WithMetrics(metrics)
			wg.Add(1)
			go func() { defer wg.Done(); watcher.Run(ctx) }()
			log.Info("slo watcher enabled", "slis", len(slis),
				"interval_sec", cfg.SLOIntervalSec)
		}
	}

	// ---- 拓扑同步(role: topology)----
	// 独立角色:它是周期性外部查询,与信号处理无关,拆开可单独伸缩/关闭。
	// 多副本同时同步是安全的(upsert 幂等),但没必要 —— 生产建议只给一个副本这个角色。
	if topoSyncer != nil && cfg.HasRole("topology") {
		wg.Add(1)
		go func() { defer wg.Done(); topoSyncer.Run(ctx) }()
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
