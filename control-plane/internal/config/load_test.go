package config

import (
	"strings"
	"testing"
)

// 本文件测的是 **Load() → Validate() 这条真实路径**,而不是直接构造 Config。
//
// 这个区别不是形式上的:config_test.go 里的用例都手工构造 Config 字面量,
// 因此它们只能证明"Validate 的判断逻辑对",证明不了"Load 真的把环境变量读进了
// 被判断的那些字段"。PrometheusURL / LokiURL / TempoURL 就栽在这个缝里 ——
// 字段声明了、校验引用了、用例覆盖了,但 Load 从没赋值,于是生产模式下
// "至少配一个观测后端"恒判为未配置,配对了也拒绝启动。
//
// 那个缺陷能长期存在,是因为 AIOPS_ENV 从没出现在任何部署清单里:生产分支
// 从来没跑过,坏掉的护栏和好的护栏表现完全一样。

// prodEnv 返回一份能通过生产校验的最小环境变量集合。
func prodEnv() map[string]string {
	return map[string]string{
		"AIOPS_ENV":                      "production",
		"AIOPS_AUTH_MODE":                "oidc",
		"AIOPS_OIDC_ISSUER":              "https://idp.corp.example/realms/aiops",
		"AIOPS_OIDC_JWKS_URL":            "https://idp.corp.example/realms/aiops/protocol/openid-connect/certs",
		"AIOPS_INTERNAL_TOKEN":           "internal-token-value",
		"AIOPS_WEBHOOK_SECRET":           "webhook-secret-value",
		"AIOPS_PROM_URL":                 "http://prometheus.monitoring.svc.cluster.local:9090",
		"AIOPS_LOKI_URL":                 "http://loki-gateway.monitoring.svc.cluster.local:80",
		"AIOPS_TEMPO_URL":                "http://tempo-query-frontend.monitoring.svc.cluster.local:3200",
		"AIOPS_PROM_CLUSTER_LABEL":       "cluster",
		"AIOPS_LOKI_CLUSTER_LABEL":       "cluster",
		"AIOPS_TEMPO_CLUSTER_LABEL":      "k8s.cluster.name",
		"AIOPS_TEMPORAL_RUN_TIMEOUT_SEC": "604800",
	}
}

func applyEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
}

// TestLoad_ReadsObservabilityURLs 直接钉住漏赋值:Load 必须把三个后端 URL
// 读进 Config,否则 Validate 看到的永远是空值。
func TestLoad_ReadsObservabilityURLs(t *testing.T) {
	t.Setenv("AIOPS_PROM_URL", "http://prom:9090")
	t.Setenv("AIOPS_LOKI_URL", "http://loki:3100")
	t.Setenv("AIOPS_TEMPO_URL", "http://tempo:3200")

	c := Load()

	if c.PrometheusURL != "http://prom:9090" {
		t.Errorf("PrometheusURL = %q, want http://prom:9090", c.PrometheusURL)
	}
	if c.LokiURL != "http://loki:3100" {
		t.Errorf("LokiURL = %q, want http://loki:3100", c.LokiURL)
	}
	if c.TempoURL != "http://tempo:3200" {
		t.Errorf("TempoURL = %q, want http://tempo:3200", c.TempoURL)
	}
}

// TestLoad_ProdManifestPasses 是这组用例的核心:一份**与部署清单等价**的
// 生产环境变量必须通过校验。若它失败,说明生产部署会起不来。
func TestLoad_ProdManifestPasses(t *testing.T) {
	applyEnv(t, prodEnv())

	c := Load()
	if !c.IsProduction() {
		t.Fatal("AIOPS_ENV=production 未被识别为生产模式")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("等价于生产清单的配置必须通过校验,否则生产起不来: %v", err)
	}
}

// TestLoad_ProdManifestWithMTLSPasses 覆盖 mTLS 开启(生产 values 的实际形态)。
func TestLoad_ProdManifestWithMTLSPasses(t *testing.T) {
	env := prodEnv()
	env["AIOPS_AGENT_MTLS_ENABLED"] = "true"
	env["AIOPS_AGENT_CLIENT_CERT"] = "/certs/client.crt"
	env["AIOPS_AGENT_CLIENT_KEY"] = "/certs/client.key"
	env["AIOPS_AGENT_CA"] = "/certs/ca.crt"
	applyEnv(t, env)

	if err := Load().Validate(); err != nil {
		t.Fatalf("mTLS 开启的生产配置必须通过: %v", err)
	}
}

// TestLoad_ProdRejectsMissingObservability 反向确认:三个 URL 都不给时必须拒绝。
// 与上面那条合起来才说明"这条校验真的在看环境变量",而不是恒真或恒假。
func TestLoad_ProdRejectsMissingObservability(t *testing.T) {
	env := prodEnv()
	delete(env, "AIOPS_PROM_URL")
	delete(env, "AIOPS_LOKI_URL")
	delete(env, "AIOPS_TEMPO_URL")
	applyEnv(t, env)
	// t.Setenv 只能设值不能删,显式置空模拟"未配置"。
	t.Setenv("AIOPS_PROM_URL", "")
	t.Setenv("AIOPS_LOKI_URL", "")
	t.Setenv("AIOPS_TEMPO_URL", "")

	err := Load().Validate()
	if err == nil {
		t.Fatal("生产模式未配置任何观测后端应被拒绝(否则静默回退 mock 假证据)")
	}
	if !strings.Contains(err.Error(), "观测后端") {
		t.Errorf("错误信息应指出观测后端缺失,实际: %v", err)
	}
}

// TestLoad_DefaultEnvIsNotProduction 说明严格性的来源:代码默认值是
// development(否则本地跑测试会被生产护栏挡住),严格性由**部署清单显式声明
// production** 提供。这正是缺陷 A 的成因 —— 清单里没有这一项,于是永远宽松。
func TestLoad_DefaultEnvIsNotProduction(t *testing.T) {
	t.Setenv("AIOPS_ENV", "")

	c := Load()
	if c.Env != "development" {
		t.Errorf("默认 Env = %q, want development", c.Env)
	}
	if c.IsProduction() {
		t.Error("默认不应是生产模式")
	}
}

// TestLoad_ProdEnvAliases production 与 prod 两种写法都要生效
// (values-prod.yaml 写 production,但运维手写 prod 很常见)。
func TestLoad_ProdEnvAliases(t *testing.T) {
	for _, v := range []string{"production", "prod", "Production", "PROD"} {
		t.Run(v, func(t *testing.T) {
			applyEnv(t, prodEnv())
			t.Setenv("AIOPS_ENV", v)
			if !Load().IsProduction() {
				t.Errorf("AIOPS_ENV=%q 应被识别为生产", v)
			}
		})
	}
}
