package incident

import (
	"testing"

	"github.com/aiops/control-plane/internal/model"
)

func TestGroupingKeyStableAndDedupes(t *testing.T) {
	s1 := model.Signal{
		TenantID: "default", ClusterID: "prod-cn-1",
		SignalType:  "alert",
		ResourceRef: model.ResourceRef{Namespace: "payment", Kind: "Deployment", Name: "checkout"},
		Labels:      map[string]string{"rule_id": "r-1"},
	}
	s2 := s1 // 同资源同规则的第二条告警
	if GroupingKey(s1) != GroupingKey(s2) {
		t.Fatal("同资源同规则的信号应聚合到同一 grouping_key")
	}

	// change 类信号(无 rule)应与该资源的 alert 归为同组(便于变更关联)
	change := model.Signal{
		TenantID: "default", ClusterID: "prod-cn-1", SignalType: "change",
		ResourceRef: model.ResourceRef{Namespace: "payment", Kind: "Deployment", Name: "checkout"},
	}
	alert := model.Signal{
		TenantID: "default", ClusterID: "prod-cn-1", SignalType: "alert",
		ResourceRef: model.ResourceRef{Namespace: "payment", Kind: "Deployment", Name: "checkout"},
	}
	if GroupingKey(change) != GroupingKey(alert) {
		t.Fatal("同资源的 change 与 alert 应归为同一 incident 组")
	}

	// 不同资源必须不同组
	other := s1
	other.ResourceRef.Name = "orders"
	if GroupingKey(s1) == GroupingKey(other) {
		t.Fatal("不同资源不应聚合")
	}
}

func TestNormalizeSeverity(t *testing.T) {
	cases := map[string]string{
		"critical": "P1", "CRITICAL": "P1", "fatal": "P1",
		"error": "P2", "high": "P2",
		"warning": "P3", "medium": "P3",
		"info": "P4", "": "P4",
	}
	for in, want := range cases {
		if got := NormalizeSeverity(in); got != want {
			t.Errorf("NormalizeSeverity(%q)=%q want %q", in, got, want)
		}
	}
}

func TestClassifyFault(t *testing.T) {
	cases := []struct {
		sig  model.Signal
		want string
	}{
		{model.Signal{SignalType: "change", Labels: map[string]string{"kind": "deployment"}}, "release_regression"},
		{model.Signal{SignalType: "alert", Labels: map[string]string{"alertname": "PodCrashLoopBackOff"}}, "pod_workload"},
		{model.Signal{SignalType: "alert", Labels: map[string]string{"alertname": "HighCPUThrottling"}}, "resource"},
		{model.Signal{SignalType: "alert", Labels: map[string]string{"alertname": "DependencyTimeout"}}, "dependency"},
	}
	for _, c := range cases {
		if got := ClassifyFault(c.sig); got != c.want {
			t.Errorf("ClassifyFault(%v)=%q want %q", c.sig.Labels, got, c.want)
		}
	}
}
