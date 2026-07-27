package obsquery

import (
	"strings"
	"testing"
)

// TestPlannerSuppliedExprIsScoped 验证"模型自带查询"这条新链路:
// planner 产出的 PromQL / LogQL 经过守卫后仍被强制限定在 cluster+namespace,
// 且表达式主体(聚合、正则过滤)被保留——即模型能收窄问题,不能扩大范围。
func TestPlannerSuppliedExprIsScoped(t *testing.T) {
	req := []ScopeLabel{{Name: "namespace", Value: "shop"}, {Name: "cluster", Value: "prod-cn-1"}}

	t.Run("promql from planner", func(t *testing.T) {
		in := `sum by (version) (rate(http_requests_total{status=~"5.."}[5m]))`
		got, err := scopePromQL(in, req...)
		if err != nil {
			t.Fatalf("scopePromQL(%q) 报错: %v", in, err)
		}
		for _, want := range []string{`namespace="shop"`, `cluster="prod-cn-1"`, `status=~"5.."`, "sum by"} {
			if !contains(got, want) {
				t.Errorf("结果缺少 %q:\n%s", want, got)
			}
		}
	})

	t.Run("logql from planner with empty selector", func(t *testing.T) {
		in := `{} |~ "(?i)(exception|stack trace)"`
		got, err := injectNamespaceMatchers(in, req...)
		if err != nil {
			t.Fatalf("injectNamespaceMatchers(%q) 报错: %v", in, err)
		}
		for _, want := range []string{`namespace="shop"`, `cluster="prod-cn-1"`, `|~ "(?i)(exception|stack trace)"`} {
			if !contains(got, want) {
				t.Errorf("结果缺少 %q:\n%s", want, got)
			}
		}
	})

	t.Run("planner cannot widen scope", func(t *testing.T) {
		// 模型试图查别的 namespace / 用非精确算子 —— 必须拒绝,不是静默改写。
		for _, bad := range []string{
			`rate(http_requests_total{namespace="other"}[5m])`,
			`rate(http_requests_total{namespace=~".*"}[5m])`,
			`rate(http_requests_total{cluster="staging"}[5m])`,
		} {
			if _, err := scopePromQL(bad, req...); err == nil {
				t.Errorf("越界表达式未被拒绝: %s", bad)
			}
		}
	})
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
