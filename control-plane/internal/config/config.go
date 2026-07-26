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

	// 组件互联
	ClusterAgentURL string
	InternalURL     string // 供 AI Worker 回写的内部 API base(下发给 workflow)
	ClusterID       string
	Tenant          string

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

	// 可观测性(架构第 16 节)
	ServiceName  string
	OTLPEndpoint string // 为空则不导出 trace(仍可埋点)

	// CORS 允许的前端源(逗号分隔);为空则仅放行 localhost 开发源
	CORSOrigins []string
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

// Load 读取环境变量并填充默认值。
func Load() Config {
	brokers := strings.Split(getenv("AIOPS_KAFKA_BROKERS", "localhost:19092"), ",")
	for i := range brokers {
		brokers[i] = strings.TrimSpace(brokers[i])
	}
	return Config{
		Env:              getenv("AIOPS_ENV", "development"),
		PublicAddr:       getenv("AIOPS_PUBLIC_ADDR", ":8088"),
		InternalAddr:     getenv("AIOPS_INTERNAL_ADDR", ":8090"),
		DBDSN:            getenv("AIOPS_DB_DSN", "postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable"),
		KafkaBrokers:     brokers,
		TemporalHostPort: getenv("AIOPS_TEMPORAL_HOSTPORT", "localhost:7233"),
		TemporalNS:       getenv("AIOPS_TEMPORAL_NAMESPACE", "default"),
		TemporalQueue:    getenv("AIOPS_TEMPORAL_QUEUE", "investigation-ai"),
		ClusterAgentURL:  getenv("AIOPS_CLUSTER_AGENT_URL", "http://localhost:9100"),
		InternalURL:      getenv("AIOPS_CONTROL_INTERNAL_URL", "http://localhost:8090"),
		ClusterID:        getenv("AIOPS_CLUSTER_ID", "prod-cn-1"),
		Tenant:           getenv("AIOPS_TENANT", "default"),

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

		MaxDeliveryAttempts: getint("AIOPS_MAX_DELIVERY_ATTEMPTS", 5),

		ServiceName:  getenv("AIOPS_SERVICE_NAME", "aiops-control-plane"),
		OTLPEndpoint: getenv("AIOPS_OTLP_ENDPOINT", ""),
		CORSOrigins:  splitNonEmpty(getenv("AIOPS_CORS_ORIGINS", "")),
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
