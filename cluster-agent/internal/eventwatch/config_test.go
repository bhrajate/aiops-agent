package eventwatch

import (
	"errors"
	"testing"
)

func TestEnabledDefaultsOff(t *testing.T) {
	// 默认关闭:它让 agent 从"纯被动"变成"会主动出站",
	// 这种形态变化不该靠升级悄悄生效。
	t.Setenv("AIOPS_EVENT_WATCH_ENABLED", "")
	if Enabled() {
		t.Error("未设置时应默认关闭")
	}
	for _, v := range []string{"1", "true", "TRUE", "yes"} {
		t.Setenv("AIOPS_EVENT_WATCH_ENABLED", v)
		if !Enabled() {
			t.Errorf("%q 应视为启用", v)
		}
	}
	t.Setenv("AIOPS_EVENT_WATCH_ENABLED", "0")
	if Enabled() {
		t.Error("0 应视为关闭")
	}
}

func TestMockDatasourceRejectedInAnyEnv(t *testing.T) {
	// 比 datasource.ErrMockInProduction 更硬:那条只在生产拒绝,这条任何环境都拒。
	// 假 evidence 只污染一次结论,假 signal 会**创建真 incident** ——
	// 值班人员会去排查一个不存在的故障。
	t.Setenv("AIOPS_CONTROL_INGRESS_URL", "http://cp:8088")
	for _, env := range []string{"", "development", "staging", "production"} {
		t.Setenv("AIOPS_ENV", env)
		if _, err := ConfigFromEnv("c1", "mock"); !errors.Is(err, ErrMockDatasource) {
			t.Errorf("AIOPS_ENV=%q 下 mock 应被拒,得到 %v", env, err)
		}
	}
	// live 才放行
	if _, err := ConfigFromEnv("c1", "live"); err != nil {
		t.Errorf("live 应放行,得到 %v", err)
	}
}

func TestMissingIngressRejected(t *testing.T) {
	t.Setenv("AIOPS_CONTROL_INGRESS_URL", "")
	if _, err := ConfigFromEnv("c1", "live"); !errors.Is(err, ErrMissingIngress) {
		t.Errorf("缺 ingress 地址应被拒,得到 %v", err)
	}
}

func TestIngressPathAutoCompleted(t *testing.T) {
	// 部署清单里写基地址更自然,而拼错路径的失败表现是 404 ——
	// 那时 agent 只看到非 2xx,不会知道是路径问题。
	cases := map[string]string{
		"http://cp:8088":            "http://cp:8088/v1/signals",
		"http://cp:8088/":           "http://cp:8088/v1/signals",
		"http://cp:8088/v1/signals": "http://cp:8088/v1/signals",
	}
	for in, want := range cases {
		t.Setenv("AIOPS_CONTROL_INGRESS_URL", in)
		cfg, err := ConfigFromEnv("c1", "live")
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if cfg.IngressURL != want {
			t.Errorf("%s → %s, want %s", in, cfg.IngressURL, want)
		}
	}
}

func TestInvalidRateRejected(t *testing.T) {
	t.Setenv("AIOPS_CONTROL_INGRESS_URL", "http://cp:8088")
	for _, v := range []string{"0", "-1", "abc"} {
		t.Setenv("AIOPS_EVENT_WATCH_RATE_PER_SEC", v)
		if _, err := ConfigFromEnv("c1", "live"); err == nil {
			t.Errorf("速率 %q 应被拒", v)
		}
	}
	// 合法值放行
	t.Setenv("AIOPS_EVENT_WATCH_RATE_PER_SEC", "7.5")
	cfg, err := ConfigFromEnv("c1", "live")
	if err != nil || cfg.RatePerSec != 7.5 {
		t.Errorf("7.5 应放行,得到 rate=%v err=%v", cfg.RatePerSec, err)
	}
}

func TestListParsing(t *testing.T) {
	t.Setenv("AIOPS_CONTROL_INGRESS_URL", "http://cp:8088")
	t.Setenv("AIOPS_EVENT_WATCH_REASONS", " OOMKilling , Evicted ,, ")
	t.Setenv("AIOPS_EVENT_WATCH_NAMESPACES", "payment")
	cfg, err := ConfigFromEnv("c1", "live")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Reasons) != 2 {
		t.Errorf("reasons = %v, want 2 项(空白项应被丢弃)", cfg.Reasons)
	}
	if len(cfg.Namespaces) != 1 {
		t.Errorf("namespaces = %v, want 1 项", cfg.Namespaces)
	}
}
