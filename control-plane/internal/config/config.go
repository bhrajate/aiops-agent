// Package config 从环境变量(统一前缀 AIOPS_)加载控制面配置。
package config

import (
	"os"
	"strconv"
	"strings"
)

// DefaultHS256Secret 是开发用不安全默认值;生产模式启动校验会拒绝它。
const DefaultHS256Secret = "dev-insecure-change-me"

type Config struct {
	// 运行环境:development | production。production 触发严格启动校验。
	Env string

	// 网络
	PublicAddr   string // 公共 API,默认 :8080
	InternalAddr string // 内部 API,默认 :8090

	// 依赖
	DBDSN            string
	KafkaBrokers     []string
	TemporalHostPort string
	TemporalNS       string
	TemporalQueue    string

	// AutoMigrate 让进程启动时自行把业务库 schema 迁到最新。
	// 默认 false:生产迁移应是独立步骤(Helm pre-upgrade Job),因为多副本滚动
	// 更新期间自动迁移会让尚未替换的旧副本面对新 schema。开发/单副本可开启。
	// 无论开关如何,启动时都会**校验**版本,不匹配即拒绝启动。
	AutoMigrate bool

	// 组件互联
	ClusterAgentURL string // 单集群兼容:仅当未配置 ClusterAgents 时生效
	// 多集群:cluster_id=url 逗号分隔,如 "prod-cn-1=https://a:9100,edge-eu-2=https://b:9100"
	ClusterAgents string
	// 共享观测后端(控制面直连)。URL 也由 internal/obsquery 直接读取环境变量,
	// 这里保留副本用于启动校验(生产必须接真实后端,不能回退 mock)。
	// ClusterLabel 是后端中区分集群的 label 名,多集群共用一套后端时必须设置,
	// 否则只按 namespace 过滤会跨集群串数据。
	PrometheusURL string
	LokiURL       string
	TempoURL      string
	// ClusterLabel 是全局回落值;各后端命名法不同,可用下面三个专属变量分别覆盖
	// (详见 internal/obsquery/clusterlabel.go)。
	ClusterLabel      string
	PromClusterLabel  string
	LokiClusterLabel  string
	TempoClusterLabel string
	// ClusterLabelDisabled 显式声明"后端为单集群专用,无需集群维度隔离"。
	// 要求显式表态而非留空即关闭,见 Validate。
	ClusterLabelDisabled bool
	InternalURL          string // 供 AI Worker 回写的内部 API base(下发给 workflow)
	ClusterID            string
	Tenant               string

	// 认证(SECURITY §1)
	AuthMode     string // hs256 | oidc | disabled
	HS256Secret  string
	OIDCIssuer   string
	OIDCJWKSURL  string
	OIDCAudience string
	Issuer       string // 本地签发 issuer
	Audience     string

	// 内部 API 共享密钥(SECURITY §2)
	InternalToken string

	// Webhook 签名(SECURITY §4)
	WebhookSecret string

	// mTLS 到 cluster-agent(SECURITY §3,客户端侧)
	AgentMTLSEnabled bool
	AgentClientCert  string
	AgentClientKey   string
	AgentCA          string

	// 对象存储(SECURITY §6)
	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3UseSSL    bool

	// 可靠性
	MaxDeliveryAttempts int

	// 触发策略硬停止(文档 6.3):冷却期与并发上限,防告警风暴/成本失控
	CooldownSec          int // 同 grouping_key 两次调查最小间隔;<=0 关闭
	MaxActivePerTenant   int // 每租户同时活跃调查上限;<=0 关闭
	CorrelationWindowSec int // Incident 相关性合并时间窗(秒)
	// 孤儿调查对账(A2):创建后多久仍无 run_id 视为孤儿,以及扫描间隔(秒)
	ReconcileGraceSec    int
	ReconcileIntervalSec int

	// 服务依赖拓扑(拓扑关联)。topology_refs 此前恒为 '[]' —— 相关性合并只按
	// tenant|cluster|namespace,调用链上的故障传播识别不了。
	// 边从 Tempo service graph 指标同步(需 Tempo 启用 metrics-generator)。
	TopologyEnabled     bool
	TopologySyncSec     int     // 同步间隔
	TopologyMaxEdgeAge  int     // 边的最大年龄(秒);超过视为已下线
	TopologyMinConf     float64 // 进入 topology_refs 的最低置信度
	TopologyMinLinkConf float64 // 链接 incident 的最低置信度(更严)

	// 自动触发策略(F7)。此前 EvaluateAuto 四个分支全返回 true(伪装成策略的
	// 常量),每个 incident 都消耗一次 triage 模型调用,含 P4 单信号。
	// 跳过的 incident 仍入库、仍可人工发起调查,且会写审计与指标。
	//
	// AutoTriggerAll=true 完整回到旧行为(一律触发),用于回退或对照。
	AutoTriggerAll bool
	// AutoTriggerAlwaysSeverities 无条件触发的严重度,逗号分隔。
	AutoTriggerAlwaysSeverities []string
	// AutoTriggerSkipSeverities 允许被跳过的严重度(仍需其他判据均未命中)。
	// 默认只含 P4:P3 是最常见级别且混着真问题,拦它会显著改变值班预期。
	AutoTriggerSkipSeverities []string
	// AutoTriggerBurstSignals 信号数达到此值视为突发;<=0 关闭该判据。
	AutoTriggerBurstSignals int
	// AutoTriggerOnChange 变更关联时触发。默认开:变更关联是最容易被自动定位的
	// 一类根因,放过它损失最大。
	AutoTriggerOnChange bool

	// 可观测性(架构第 16 节)
	ServiceName  string
	OTLPEndpoint string // 为空则不导出 trace(仍可埋点)

	// CORS 允许的前端源(逗号分隔);为空则仅放行 localhost 开发源
	CORSOrigins []string

	// 角色:控制哪些子系统在本进程启用,实现按角色拆分部署单元。
	// 取值(逗号分隔):api / internal / ingest / trigger / outbox / janitor /
	// topology / slo,或 all(默认)。
	//   api      公共 API(:8088,前端与 webhook 入口)
	//   internal 内部 API(:8090,Tool Gateway + AI Worker 回写)
	//   ingest   signals consumer → Incident Manager(两层聚合)
	//   trigger  incidents consumer → Trigger Policy/Orchestrator
	//   outbox   Outbox 投递循环
	//   janitor  数据保留清理(多副本下靠 advisory lock 互斥)
	//   topology 服务依赖拓扑同步(周期性查 Tempo service graph 指标)
	//   slo      SLO 燃尽率监视(主动异常检测,合成 signal 进入既有管道)
	Roles []string

	// 信号入口限流(F6):按租户令牌桶,按**信号条数**计费。
	// <=0 表示关闭。抗告警风暴:保护 ingress/DB/outbox 不被打穿。
	// 注意:进程内限流,每副本独立配额,总容量随副本数放大(见 httpx.TokenBucket)。
	IngressRatePerSec float64
	IngressBurst      float64

	// 数据保留(retention):各表保留天数,<=0 表示**不清理该表**。
	// 清理由 janitor 角色分批执行,只删终态数据,永不触碰活跃 incident。
	Retention RetentionConfig
}

// RetentionConfig 各类数据的保留期与清理节奏。
//
// 分两类:
//   - 运营数据(signals/events/audit/outbox/dead_letters/idempotency):按时间清理;
//   - 案例数据(incidents + 其调查/证据/假设):只有**终态且过期**才整案清理。
//
// 默认值偏保守(审计留最久),可按合规要求调整。
type RetentionConfig struct {
	Enabled bool

	SignalDays      int // 原始信号
	EventDays       int // investigation_events 时间线
	AuditDays       int // audit_log(合规相关,默认最长)
	OutboxDays      int // 已发布的 outbox 记录
	DeadLetterDays  int // 死信
	TopologyDays    int // 陈旧拓扑边(服务下线后边停止刷新)
	IdempotencyDays int // 幂等键(短生命周期)
	// CaseDays 终态 incident 及其级联数据(investigations/evidence/hypotheses/
	// alert_groups/human_feedback)的保留天数。
	CaseDays int

	IntervalSec int // 清理循环间隔
	BatchSize   int // 单批删除行数上限(控制锁持有时间)
}

// HasRole 判断某角色是否在本进程启用(all 或空表示全部启用)。
func (c Config) HasRole(role string) bool {
	if len(c.Roles) == 0 {
		return true
	}
	for _, r := range c.Roles {
		if r == "all" || r == role {
			return true
		}
	}
	return false
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getbool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes"
}

func getint(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getfloat(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// Load 读取环境变量并填充默认值。
func Load() Config {
	brokers := strings.Split(getenv("AIOPS_KAFKA_BROKERS", "localhost:19092"), ",")
	for i := range brokers {
		brokers[i] = strings.TrimSpace(brokers[i])
	}
	return Config{
		Env:                  getenv("AIOPS_ENV", "development"),
		PublicAddr:           getenv("AIOPS_PUBLIC_ADDR", ":8088"),
		InternalAddr:         getenv("AIOPS_INTERNAL_ADDR", ":8090"),
		DBDSN:                getenv("AIOPS_DB_DSN", "postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable"),
		AutoMigrate:          getbool("AIOPS_AUTO_MIGRATE", false),
		KafkaBrokers:         brokers,
		TemporalHostPort:     getenv("AIOPS_TEMPORAL_HOSTPORT", "localhost:7233"),
		TemporalNS:           getenv("AIOPS_TEMPORAL_NAMESPACE", "default"),
		TemporalQueue:        getenv("AIOPS_TEMPORAL_QUEUE", "investigation-ai"),
		ClusterAgentURL:      getenv("AIOPS_CLUSTER_AGENT_URL", "http://localhost:9100"),
		ClusterAgents:        getenv("AIOPS_CLUSTER_AGENTS", ""),
		ClusterLabel:         getenv("AIOPS_CLUSTER_LABEL", ""),
		PromClusterLabel:     getenv("AIOPS_PROM_CLUSTER_LABEL", ""),
		LokiClusterLabel:     getenv("AIOPS_LOKI_CLUSTER_LABEL", ""),
		TempoClusterLabel:    getenv("AIOPS_TEMPO_CLUSTER_LABEL", ""),
		ClusterLabelDisabled: getbool("AIOPS_CLUSTER_LABEL_DISABLED", false),
		InternalURL:          getenv("AIOPS_CONTROL_INTERNAL_URL", "http://localhost:8090"),
		ClusterID:            getenv("AIOPS_CLUSTER_ID", "prod-cn-1"),
		Tenant:               getenv("AIOPS_TENANT", "default"),

		AuthMode:     getenv("AIOPS_AUTH_MODE", "hs256"),
		HS256Secret:  getenv("AIOPS_AUTH_HS256_SECRET", DefaultHS256Secret),
		OIDCIssuer:   getenv("AIOPS_OIDC_ISSUER", ""),
		OIDCJWKSURL:  getenv("AIOPS_OIDC_JWKS_URL", ""),
		OIDCAudience: getenv("AIOPS_OIDC_AUDIENCE", "aiops"),
		Issuer:       getenv("AIOPS_AUTH_ISSUER", "aiops-dev"),
		Audience:     getenv("AIOPS_AUTH_AUDIENCE", "aiops"),

		InternalToken: getenv("AIOPS_INTERNAL_TOKEN", ""),
		WebhookSecret: getenv("AIOPS_WEBHOOK_SECRET", ""),

		AgentMTLSEnabled: getbool("AIOPS_AGENT_MTLS_ENABLED", false),
		AgentClientCert:  getenv("AIOPS_AGENT_CLIENT_CERT", ""),
		AgentClientKey:   getenv("AIOPS_AGENT_CLIENT_KEY", ""),
		AgentCA:          getenv("AIOPS_AGENT_CA", ""),

		S3Endpoint:  getenv("AIOPS_S3_ENDPOINT", "localhost:9000"),
		S3Bucket:    getenv("AIOPS_S3_BUCKET", "aiops-evidence"),
		S3AccessKey: getenv("AIOPS_S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey: getenv("AIOPS_S3_SECRET_KEY", "minioadmin"),
		S3UseSSL:    getbool("AIOPS_S3_USE_SSL", false),

		MaxDeliveryAttempts:  getint("AIOPS_MAX_DELIVERY_ATTEMPTS", 5),
		CooldownSec:          getint("AIOPS_INVESTIGATION_COOLDOWN_SEC", 300),
		MaxActivePerTenant:   getint("AIOPS_MAX_ACTIVE_INVESTIGATIONS", 20),
		CorrelationWindowSec: getint("AIOPS_CORRELATION_WINDOW_SEC", 900),
		ReconcileGraceSec:    getint("AIOPS_RECONCILE_GRACE_SEC", 60),
		ReconcileIntervalSec: getint("AIOPS_RECONCILE_INTERVAL_SEC", 60),

		// 默认开启:未启用 Tempo metrics-generator 时同步查到 0 条边并告警一次,
		// 不影响任何既有路径;而默认关闭会让这个能力事实上没人用。
		TopologyEnabled:    getbool("AIOPS_TOPOLOGY_ENABLED", true),
		TopologySyncSec:    getint("AIOPS_TOPOLOGY_SYNC_SEC", 300),
		TopologyMaxEdgeAge: getint("AIOPS_TOPOLOGY_MAX_EDGE_AGE_SEC", 3600),
		// 0.5:K8s Service selector 边(0.7)也进 topology_refs 供 planner 参考。
		TopologyMinConf: getfloat("AIOPS_TOPOLOGY_MIN_CONFIDENCE", 0.5),
		// 0.8:只有真实调用边(Tempo,0.9)足以链接 incident。selector 边只表达
		// 入口关系,用它链接会把"同一 Service 后的两个无关工作负载"判为同源。
		TopologyMinLinkConf: getfloat("AIOPS_TOPOLOGY_MIN_LINK_CONFIDENCE", 0.8),

		AutoTriggerAll:              getbool("AIOPS_AUTO_TRIGGER_ALL", false),
		AutoTriggerAlwaysSeverities: splitNonEmpty(strings.ToUpper(getenv("AIOPS_AUTO_TRIGGER_ALWAYS_SEVERITIES", "P1,P2"))),
		AutoTriggerSkipSeverities:   splitNonEmpty(strings.ToUpper(getenv("AIOPS_AUTO_TRIGGER_SKIP_SEVERITIES", "P4"))),
		AutoTriggerBurstSignals:     getint("AIOPS_AUTO_TRIGGER_BURST_SIGNALS", 3),
		AutoTriggerOnChange:         getbool("AIOPS_AUTO_TRIGGER_ON_CHANGE", true),

		ServiceName:  getenv("AIOPS_SERVICE_NAME", "aiops-control-plane"),
		OTLPEndpoint: getenv("AIOPS_OTLP_ENDPOINT", ""),
		CORSOrigins:  splitNonEmpty(getenv("AIOPS_CORS_ORIGINS", "")),
		Roles:        splitNonEmpty(strings.ToLower(getenv("AIOPS_ROLES", "all"))),

		// 默认值按"每秒 50 条、突发 500 条"设定:正常告警量远低于此,
		// 只在风暴时生效;设为 0 可关闭。
		IngressRatePerSec: getfloat("AIOPS_INGRESS_RATE_PER_SEC", 50),
		IngressBurst:      getfloat("AIOPS_INGRESS_BURST", 500),

		Retention: RetentionConfig{
			// 默认开启:无界增长是生产事故的常见来源,默认不清理等于默认埋雷。
			Enabled:        getbool("AIOPS_RETENTION_ENABLED", true),
			SignalDays:     getint("AIOPS_RETENTION_SIGNAL_DAYS", 30),
			EventDays:      getint("AIOPS_RETENTION_EVENT_DAYS", 90),
			AuditDays:      getint("AIOPS_RETENTION_AUDIT_DAYS", 365),
			OutboxDays:     getint("AIOPS_RETENTION_OUTBOX_DAYS", 7),
			DeadLetterDays: getint("AIOPS_RETENTION_DEAD_LETTER_DAYS", 30),
			// 7 天:拓扑同步每 5 分钟一轮,7 天不刷新说明该依赖确实没了。
			// 取太短会在 Tempo 短暂不可用时误删仍然存在的边。
			TopologyDays:    getint("AIOPS_RETENTION_TOPOLOGY_DAYS", 7),
			IdempotencyDays: getint("AIOPS_RETENTION_IDEMPOTENCY_DAYS", 7),
			CaseDays:        getint("AIOPS_RETENTION_CASE_DAYS", 180),
			IntervalSec:     getint("AIOPS_RETENTION_INTERVAL_SEC", 3600),
			BatchSize:       getint("AIOPS_RETENTION_BATCH_SIZE", 5000),
		},
	}
}

func splitNonEmpty(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// hasPerBackendClusterLabel 报告是否至少配了一个后端专属集群 label。
func (c Config) hasPerBackendClusterLabel() bool {
	return c.PromClusterLabel != "" || c.LokiClusterLabel != "" || c.TempoClusterLabel != ""
}

// IsProduction 报告是否处于生产模式(触发严格校验)。
func (c Config) IsProduction() bool {
	return strings.EqualFold(c.Env, "production") || strings.EqualFold(c.Env, "prod")
}

// Validate 在启动时检查关键安全配置(SECURITY §1/§2/§4)。
// 生产模式下,任何弱/缺失的安全配置都直接拒绝启动(fail-fast),
// 避免"默认密钥 / 空 token / 空 webhook secret 静默启动"。
func (c Config) Validate() error {
	var problems []string

	if c.IsProduction() {
		switch strings.ToLower(c.AuthMode) {
		case "disabled":
			problems = append(problems, "AIOPS_AUTH_MODE=disabled 不允许用于生产")
		case "hs256":
			if c.HS256Secret == "" || c.HS256Secret == DefaultHS256Secret {
				problems = append(problems, "生产模式必须设置强随机 AIOPS_AUTH_HS256_SECRET(不能为默认值/空)")
			}
			if len(c.HS256Secret) < 32 {
				problems = append(problems, "AIOPS_AUTH_HS256_SECRET 长度应 >= 32")
			}
		case "oidc":
			if c.OIDCIssuer == "" || c.OIDCJWKSURL == "" {
				problems = append(problems, "AIOPS_AUTH_MODE=oidc 必须配置 AIOPS_OIDC_ISSUER 与 AIOPS_OIDC_JWKS_URL")
			}
		default:
			problems = append(problems, "AIOPS_AUTH_MODE 必须为 hs256 或 oidc")
		}
		if c.InternalToken == "" {
			problems = append(problems, "生产模式必须设置 AIOPS_INTERNAL_TOKEN(内部 API 共享密钥)")
		}
		if c.WebhookSecret == "" {
			problems = append(problems, "生产模式必须设置 AIOPS_WEBHOOK_SECRET(Signal webhook HMAC 密钥)")
		}
		if c.AgentMTLSEnabled {
			if c.AgentClientCert == "" || c.AgentClientKey == "" || c.AgentCA == "" {
				problems = append(problems, "启用 mTLS 时必须配置 client cert/key/ca")
			}
		}
		// 观测数据源:生产必须接真实后端。mock 会产出确定性假证据,
		// 若在生产静默启用,RCA 会基于虚构数据得出"有证据支撑"的结论。
		if strings.EqualFold(os.Getenv("AIOPS_OBS_DATASOURCE"), "mock") {
			problems = append(problems, "生产模式不允许 AIOPS_OBS_DATASOURCE=mock(会产出虚假证据)")
		} else if c.PrometheusURL == "" && c.LokiURL == "" && c.TempoURL == "" {
			problems = append(problems,
				"生产模式必须配置至少一个观测后端(AIOPS_PROM_URL / AIOPS_LOKI_URL / AIOPS_TEMPO_URL),否则将回退到 mock 假证据")
		}
		// 集群维度隔离:三个后端命名法不同,可用全局 AIOPS_CLUSTER_LABEL 一把配,
		// 也可用后端专属变量分别配(Tempo 通常需要,它用 OTel 的 k8s.cluster.name)。
		// 若后端确为单集群专用,必须**显式**声明 DISABLED——留空静默不隔离会让 RCA
		// 读到其他集群同名 namespace 的数据,而这个错误在诊断结论里看不出来。
		if c.ClusterLabel == "" && !c.hasPerBackendClusterLabel() && !c.ClusterLabelDisabled {
			problems = append(problems,
				"生产模式必须配置集群维度隔离:设 AIOPS_CLUSTER_LABEL,"+
					"或按后端设 AIOPS_{PROM,LOKI,TEMPO}_CLUSTER_LABEL;"+
					"若后端确为单集群专用,显式设 AIOPS_CLUSTER_LABEL_DISABLED=true")
		}
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// ValidationError 聚合启动校验问题。
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "配置校验失败:\n  - " + strings.Join(e.Problems, "\n  - ")
}
