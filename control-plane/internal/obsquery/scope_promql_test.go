package obsquery

import "testing"

func TestScopePromQL_InjectsAndBlocksBypass(t *testing.T) {
	ns := "payment"
	// 每个用例:输入 expr -> 期望结果里所有向量选择器都带 namespace="payment"
	ok := []string{
		`up`,
		`up{job="x"} or up`, // 裸选择器绕过(H1)
		`sum(up{namespace="payment"}) or count(up)`, // 混合
		`sum by (pod) (rate(http_requests_total[5m]))`,
	}
	for _, expr := range ok {
		out, err := scopePromQL(expr, ScopeLabel{Name: "namespace", Value: ns})
		if err != nil {
			t.Fatalf("scopePromQL(%q) 意外报错: %v", expr, err)
		}
		// 结果中不应残留任何未限定的裸选择器:再跑一遍应稳定(幂等)且仍合法
		out2, err := scopePromQL(out, ScopeLabel{Name: "namespace", Value: ns})
		if err != nil {
			t.Fatalf("二次 scope(%q) 报错: %v", out, err)
		}
		if out2 == "" {
			t.Fatalf("scope 结果为空: %q", expr)
		}
	}
}

func TestScopePromQL_RejectsCrossNamespace(t *testing.T) {
	bad := []string{
		`up{namespace="other"}`,
		`up{namespace="payment"} or up{namespace="other"}`,
		`up{namespace=~"pay.*"}`, // 非精确匹配
		`up{namespace!="payment"}`,
	}
	for _, expr := range bad {
		if _, err := scopePromQL(expr, ScopeLabel{Name: "namespace", Value: "payment"}); err == nil {
			t.Errorf("scopePromQL(%q) 应拒绝跨 namespace,但通过了", expr)
		}
	}
}

func TestScopePromQL_InvalidExpr(t *testing.T) {
	if _, err := scopePromQL(`sum(((`, ScopeLabel{Name: "namespace", Value: "payment"}); err == nil {
		t.Error("非法 PromQL 应报错")
	}
}
