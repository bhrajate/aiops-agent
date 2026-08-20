package config

import (
	"strings"
	"testing"
)

func base(prod bool) Config {
	env := "development"
	if prod {
		env = "production"
	}
	return Config{
		Env:           env,
		AuthMode:      "hs256",
		HS256Secret:   "a-strong-random-secret-of-length->=32-chars",
		InternalToken: "internal-token",
		WebhookSecret: "webhook-secret",
		// 生产要求接真实观测后端并指明集群 label(否则会回退 mock / 跨集群串数据)
		PrometheusURL: "http://prometheus:9090",
		ClusterLabel:  "cluster",
	}
}

func TestValidate_DevAllowsWeak(t *testing.T) {
	c := base(false)
	c.HS256Secret = DefaultHS256Secret
	c.InternalToken = ""
	c.WebhookSecret = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("dev 模式不应因弱配置失败: %v", err)
	}
}

func TestValidate_ProdRejectsDefaultSecret(t *testing.T) {
	c := base(true)
	c.HS256Secret = DefaultHS256Secret
	if err := c.Validate(); err == nil {
		t.Fatal("生产模式必须拒绝默认 HS256 密钥")
	}
}

func TestValidate_ProdRejectsShortSecret(t *testing.T) {
	c := base(true)
	c.HS256Secret = "short"
	if err := c.Validate(); err == nil {
		t.Fatal("生产模式必须拒绝过短 HS256 密钥")
	}
}

func TestValidate_ProdRequiresInternalToken(t *testing.T) {
	c := base(true)
	c.InternalToken = ""
	if err := c.Validate(); err == nil {
		t.Fatal("生产模式必须要求 internal token")
	}
}

func TestValidate_ProdRequiresWebhookSecret(t *testing.T) {
	c := base(true)
	c.WebhookSecret = ""
	if err := c.Validate(); err == nil {
		t.Fatal("生产模式必须要求 webhook secret")
	}
}

func TestValidate_ProdRejectsDisabledAuth(t *testing.T) {
	c := base(true)
	c.AuthMode = "disabled"
	if err := c.Validate(); err == nil {
		t.Fatal("生产模式必须拒绝 auth disabled")
	}
}

func TestValidate_ProdOIDCRequiresJWKS(t *testing.T) {
	c := base(true)
	c.AuthMode = "oidc"
	// 未配置 issuer/jwks
	if err := c.Validate(); err == nil {
		t.Fatal("oidc 模式必须要求 issuer 与 jwks url")
	}
	c.OIDCIssuer = "https://idp.example"
	c.OIDCJWKSURL = "https://idp.example/jwks"
	if err := c.Validate(); err != nil {
		t.Fatalf("配置齐全后不应失败: %v", err)
	}
}

func TestHasRole(t *testing.T) {
	// 未配置 / all → 全部启用(向后兼容单体部署)
	for _, c := range []Config{{}, {Roles: []string{"all"}}} {
		for _, r := range []string{"api", "internal", "ingest", "trigger", "outbox"} {
			if !c.HasRole(r) {
				t.Errorf("roles=%v 应启用 %s", c.Roles, r)
			}
		}
	}
	// 仅 api:其他角色关闭
	c := Config{Roles: []string{"api"}}
	if !c.HasRole("api") {
		t.Error("api 应启用")
	}
	for _, r := range []string{"internal", "ingest", "trigger", "outbox"} {
		if c.HasRole(r) {
			t.Errorf("roles=[api] 不应启用 %s", r)
		}
	}
	// 组合角色
	c2 := Config{Roles: []string{"ingest", "trigger", "outbox"}}
	if !c2.HasRole("ingest") || !c2.HasRole("outbox") {
		t.Error("组合角色应启用其成员")
	}
	if c2.HasRole("api") {
		t.Error("组合角色不应启用未列出的 api")
	}
}

func TestValidate_ProdHappyPath(t *testing.T) {
	if err := base(true).Validate(); err != nil {
		t.Fatalf("完整生产配置不应失败: %v", err)
	}
}

func TestValidate_ProdRejectsMockObservability(t *testing.T) {
	t.Setenv("AIOPS_OBS_DATASOURCE", "mock")
	if err := base(true).Validate(); err == nil {
		t.Fatal("生产模式必须拒绝 mock 观测数据源(会产出虚假证据)")
	}
}

func TestValidate_ProdRequiresObservabilityBackend(t *testing.T) {
	c := base(true)
	c.PrometheusURL, c.LokiURL, c.TempoURL = "", "", ""
	if err := c.Validate(); err == nil {
		t.Fatal("生产模式未配置任何观测后端应失败(否则回退 mock)")
	}
}

func TestValidate_ProdRequiresClusterIsolation(t *testing.T) {
	// 三个后端命名法不同,所以"配了集群维度隔离"有三条合法路径:全局变量、
	// 任一后端专属变量、或显式声明后端为单集群专用。三条都没有才算配置缺失。
	t.Run("三者皆无应失败", func(t *testing.T) {
		c := base(true)
		c.ClusterLabel = ""
		if err := c.Validate(); err == nil {
			t.Fatal("生产模式未配置任何集群维度隔离应失败")
		}
	})
	t.Run("全局变量可满足", func(t *testing.T) {
		c := base(true)
		if err := c.Validate(); err != nil {
			t.Fatalf("配了全局 AIOPS_CLUSTER_LABEL 应通过: %v", err)
		}
	})
	t.Run("仅后端专属变量也可满足", func(t *testing.T) {
		// Tempo 用 OTel 的 k8s.cluster.name,与 Prom/Loki 不同名——这正是
		// 必须允许"只配后端专属变量"的原因。
		c := base(true)
		c.ClusterLabel = ""
		c.TempoClusterLabel = "k8s.cluster.name"
		if err := c.Validate(); err != nil {
			t.Fatalf("配了后端专属变量应通过: %v", err)
		}
	})
	t.Run("显式声明单集群专用可满足", func(t *testing.T) {
		c := base(true)
		c.ClusterLabel = ""
		c.ClusterLabelDisabled = true
		if err := c.Validate(); err != nil {
			t.Fatalf("显式 DISABLED 应通过: %v", err)
		}
	})
}

func TestValidate_DevAllowsMockObservability(t *testing.T) {
	c := base(false)
	c.PrometheusURL, c.LokiURL, c.TempoURL, c.ClusterLabel = "", "", "", ""
	if err := c.Validate(); err != nil {
		t.Fatalf("开发模式应允许 mock 观测数据源: %v", err)
	}
}

func TestValidate_RejectsShortTemporalRunTimeout(t *testing.T) {
	// 与环境无关:开发环境同样会被这个坑到(调查永久卡在 waiting_feedback),
	// 因此两种 env 都必须拒绝。
	for _, prod := range []bool{false, true} {
		c := base(prod)
		c.TemporalRunTimeoutSec = 24 * 3600 // 24h:对单次调查够用,但小于 48h 反馈等待
		err := c.Validate()
		if err == nil {
			t.Fatalf("prod=%v: 期望拒绝 24h run timeout", prod)
		}
		if !strings.Contains(err.Error(), "waiting_feedback") {
			t.Fatalf("prod=%v: 错误信息应说明后果,实际: %v", prod, err)
		}
	}
}

func TestValidate_AcceptsZeroTemporalRunTimeout(t *testing.T) {
	// 0 = 未配置,由 temporalx 回落到默认值,不该在此报错。
	c := base(true)
	c.TemporalRunTimeoutSec = 0
	if err := c.Validate(); err != nil {
		t.Fatalf("未配置时不应报错: %v", err)
	}
}

func TestValidate_AcceptsDefaultTemporalRunTimeout(t *testing.T) {
	// Load() 的默认值必须能通过自己的校验,否则不配该变量就起不来。
	c := base(true)
	c.TemporalRunTimeoutSec = 7 * 24 * 3600
	if err := c.Validate(); err != nil {
		t.Fatalf("默认值应通过校验: %v", err)
	}
}

// 生产校验必须把"全空白的 webhook secret"当成未设。
//
// 为什么单独测:webhookauth 在 0 个密钥时的行为是**放行且不校验**
// (开发便利)。若 Validate 只判 `== ""`,那么 AIOPS_WEBHOOK_SECRET=" , "
// 能通过生产校验,然后在运行时对所有未签名请求放行 ——
// 配置看起来设了、启动没报错、而 Signal Ingress 实际上完全没有鉴权。
func TestProductionRejectsBlankWebhookSecret(t *testing.T) {
	base := func(secret string) Config {
		return Config{
			Env:           "production",
			AuthMode:      "hs256",
			HS256Secret:   "a-sufficiently-long-random-secret-value",
			InternalToken: "tok",
			WebhookSecret: secret,
			PrometheusURL: "http://prom:9090",
			ClusterLabel:  "cluster",
		}
	}
	for _, blank := range []string{"", " ", ",", " , ", ",,"} {
		if err := base(blank).Validate(); err == nil {
			t.Errorf("AIOPS_WEBHOOK_SECRET=%q 应被生产校验拒绝(它会退化成不鉴权)", blank)
		}
	}
	// 单个密钥与轮换用的两个密钥都应通过
	for _, ok := range []string{"s1", "new,old", " new , old "} {
		if err := base(ok).Validate(); err != nil {
			t.Errorf("AIOPS_WEBHOOK_SECRET=%q 应通过,得到 %v", ok, err)
		}
	}
}
