package datasource

import (
	"strings"
	"testing"
)

// 共享观测后端:必须同时强制 namespace + cluster,否则跨集群串数据。

func TestScopePromQL_InjectsClusterAndNamespace(t *testing.T) {
	ns := ScopeLabel{Name: "namespace", Value: "payment"}
	cl := ScopeLabel{Name: "cluster", Value: "prod-cn-1"}
	out, err := scopePromQL(`up`, ns, cl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `namespace="payment"`) || !strings.Contains(out, `cluster="prod-cn-1"`) {
		t.Errorf("应同时注入 namespace 与 cluster,得到 %q", out)
	}
}

func TestScopePromQL_BareSelectorGetsBothLabels(t *testing.T) {
	ns := ScopeLabel{Name: "namespace", Value: "payment"}
	cl := ScopeLabel{Name: "cluster", Value: "prod-cn-1"}
	// 裸选择器绕过面:`... or up` 里的裸 up 也必须被两个 label 限定
	out, err := scopePromQL(`up{namespace="payment",cluster="prod-cn-1"} or up`, ns, cl)
	if err != nil {
		t.Fatal(err)
	}
	// 结果里出现两次 cluster= 两次 namespace=(每个 selector 各一次)
	if strings.Count(out, `cluster="prod-cn-1"`) != 2 || strings.Count(out, `namespace="payment"`) != 2 {
		t.Errorf("裸选择器未被同时限定 namespace+cluster: %q", out)
	}
}

func TestScopePromQL_RejectsCrossCluster(t *testing.T) {
	ns := ScopeLabel{Name: "namespace", Value: "payment"}
	cl := ScopeLabel{Name: "cluster", Value: "prod-cn-1"}
	bad := []string{
		`up{cluster="other-cluster"}`,                        // 显式打别的集群
		`up{namespace="payment"} or up{cluster="edge-eu-2"}`, // 混入别集群
		`up{cluster=~"prod.*"}`,                              // 非精确
		`up{cluster!="prod-cn-1"}`,                           // 取反
	}
	for _, expr := range bad {
		if _, err := scopePromQL(expr, ns, cl); err == nil {
			t.Errorf("跨集群查询应被拒绝: %q", expr)
		}
	}
}

func TestLogQL_InjectsClusterAndNamespace(t *testing.T) {
	ns := ScopeLabel{Name: "namespace", Value: "payment"}
	cl := ScopeLabel{Name: "cluster", Value: "prod-cn-1"}
	out, err := injectNamespaceMatchers(`{app="checkout"}`, ns, cl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `namespace="payment"`) || !strings.Contains(out, `cluster="prod-cn-1"`) {
		t.Errorf("LogQL 应同时注入 namespace 与 cluster: %q", out)
	}
}

func TestLogQL_RejectsCrossCluster(t *testing.T) {
	ns := ScopeLabel{Name: "namespace", Value: "payment"}
	cl := ScopeLabel{Name: "cluster", Value: "prod-cn-1"}
	if _, err := injectNamespaceMatchers(`{cluster="other",app="x"}`, ns, cl); err == nil {
		t.Error("LogQL 跨集群应被拒绝")
	}
}

func TestScope_ClusterLabelEmpty_NotEnforced(t *testing.T) {
	// clusterLabel 未配置(后端为本集群专用)时,只强制 namespace
	ns := ScopeLabel{Name: "namespace", Value: "payment"}
	out, err := scopePromQL(`up{cluster="anything"}`, ns, ScopeLabel{})
	if err != nil {
		t.Fatalf("未配置集群维度时不应因 cluster label 报错: %v", err)
	}
	if !strings.Contains(out, `namespace="payment"`) {
		t.Errorf("仍应注入 namespace: %q", out)
	}
}

func TestLiveClusterScope(t *testing.T) {
	// clusterLabel 配置后,clusterScope 返回带该 label 名的约束
	l := &Live{clusterLabel: "cluster"}
	cs := l.clusterScope(Scope{ClusterID: "prod-cn-1"})
	if cs.Name != "cluster" || cs.Value != "prod-cn-1" {
		t.Errorf("clusterScope 错误: %+v", cs)
	}
	// 未配置则返回零值(不强制)
	l2 := &Live{clusterLabel: ""}
	if (l2.clusterScope(Scope{ClusterID: "x"}) != ScopeLabel{}) {
		t.Error("未配置 clusterLabel 应返回零值")
	}
}
