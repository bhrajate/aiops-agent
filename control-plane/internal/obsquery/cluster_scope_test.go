package obsquery

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
	// 三个后端各取自己的 label 名——这是本改动的核心:点号在 PromQL/LogQL 里是
	// 语法错误,而 Tempo 的 OTel 语义约定恰恰用点号,共用一个值无法同时满足。
	l := &Client{clusterLabels: ClusterLabels{
		Prometheus: "cluster",
		Loki:       "k8s_cluster_name",
		Tempo:      "k8s.cluster.name",
	}}
	want := map[string]string{
		BackendPrometheus: "cluster",
		BackendLoki:       "k8s_cluster_name",
		BackendTempo:      "k8s.cluster.name",
	}
	for backend, wantName := range want {
		cs := l.clusterScope(backend, Scope{ClusterID: "prod-cn-1"})
		if cs.Name != wantName || cs.Value != "prod-cn-1" {
			t.Errorf("%s 的 clusterScope 错误: got %+v, want name=%q", backend, cs, wantName)
		}
	}

	// 某后端未配置则该后端返回零值(不强制),不影响其他后端。
	partial := &Client{clusterLabels: ClusterLabels{Prometheus: "cluster"}}
	if (partial.clusterScope(BackendTempo, Scope{ClusterID: "x"}) != ScopeLabel{}) {
		t.Error("未配置的后端应返回零值")
	}
	if partial.clusterScope(BackendPrometheus, Scope{ClusterID: "x"}).Name != "cluster" {
		t.Error("已配置的后端不应受其他后端未配置影响")
	}

	// 未知后端返回零值,不会误注入。
	if (partial.clusterScope("elasticsearch", Scope{ClusterID: "x"}) != ScopeLabel{}) {
		t.Error("未知后端应返回零值")
	}
}
