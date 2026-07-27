package gateway

import (
	"context"
	"testing"

	"github.com/aiops/control-plane/internal/agentclient"
	"github.com/aiops/control-plane/internal/obsquery"
)

// fakeObs 记录被调用的观测工具,验证控制面直连路径。
type fakeObs struct{ called string }

func (f *fakeObs) QueryMetrics(_ context.Context, _ obsquery.Scope, _ map[string]any) (obsquery.Result, error) {
	f.called = "query_metrics"
	return obsquery.Result{Source: "prometheus", Summary: "ok", Raw: map[string]any{"k": 1}}, nil
}

func (f *fakeObs) SearchLogs(_ context.Context, _ obsquery.Scope, _ map[string]any) (obsquery.Result, error) {
	f.called = "search_logs"
	return obsquery.Result{Source: "loki", Summary: "ok", Raw: map[string]any{}}, nil
}

func (f *fakeObs) GetTraces(_ context.Context, _ obsquery.Scope, _ map[string]any) (obsquery.Result, error) {
	f.called = "get_traces"
	return obsquery.Result{Source: "tempo", Summary: "ok", Raw: map[string]any{}}, nil
}

// 观测类工具由控制面直连共享后端处理(不经任何集群 agent)。
func TestObservabilityToolsGoDirect(t *testing.T) {
	for _, tool := range []string{"query_metrics", "search_logs", "get_traces"} {
		fo := &fakeObs{}
		g := &Gateway{obs: fo}
		scope := agentclient.Scope{
			ClusterID: "prod-cn-1",
			Namespace: "payment",
			Resource:  map[string]any{"kind": "Deployment", "name": "checkout"},
			TimeRange: map[string]any{"from": "2026-07-27T00:00:00Z", "to": "2026-07-27T01:00:00Z"},
		}
		res, err := g.invokeObservability(context.Background(), tool, map[string]any{}, scope)
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		if fo.called != tool {
			t.Errorf("期望直连调用 %s,实际 %s", tool, fo.called)
		}
		if res.Summary == "" {
			t.Errorf("%s: 结果应带 summary", tool)
		}
	}
}

// scope 应被完整转换传入 obsquery(集群/命名空间/资源/时间窗)。
type scopeCapture struct{ got obsquery.Scope }

func (s *scopeCapture) QueryMetrics(_ context.Context, sc obsquery.Scope, _ map[string]any) (obsquery.Result, error) {
	s.got = sc
	return obsquery.Result{Raw: map[string]any{}}, nil
}
func (s *scopeCapture) SearchLogs(_ context.Context, sc obsquery.Scope, _ map[string]any) (obsquery.Result, error) {
	s.got = sc
	return obsquery.Result{Raw: map[string]any{}}, nil
}
func (s *scopeCapture) GetTraces(_ context.Context, sc obsquery.Scope, _ map[string]any) (obsquery.Result, error) {
	s.got = sc
	return obsquery.Result{Raw: map[string]any{}}, nil
}

func TestObservabilityScopeIsPropagated(t *testing.T) {
	sc := &scopeCapture{}
	g := &Gateway{obs: sc}
	in := agentclient.Scope{
		ClusterID: "prod-cn-1",
		Namespace: "payment",
		Resource:  map[string]any{"kind": "Deployment", "name": "checkout", "uid": "u-1"},
		TimeRange: map[string]any{"from": "2026-07-27T00:00:00Z", "to": "2026-07-27T01:00:00Z"},
	}
	if _, err := g.invokeObservability(context.Background(), "query_metrics", nil, in); err != nil {
		t.Fatal(err)
	}
	if sc.got.ClusterID != "prod-cn-1" || sc.got.Namespace != "payment" {
		t.Errorf("集群/命名空间未传递: %+v", sc.got)
	}
	if sc.got.Resource.Name != "checkout" || sc.got.Resource.Kind != "Deployment" {
		t.Errorf("资源未传递: %+v", sc.got.Resource)
	}
	if sc.got.TimeRange == nil || sc.got.TimeRange.From == "" {
		t.Errorf("时间窗未传递: %+v", sc.got.TimeRange)
	}
}

func TestObservabilityToolSetIsExactlyThree(t *testing.T) {
	// 观测类 = 共享后端;其余工具仍走集群 agent(需集群内身份)
	want := map[string]bool{"query_metrics": true, "search_logs": true, "get_traces": true}
	for tool := range observabilityTools {
		if !want[tool] {
			t.Errorf("%s 不应被归类为观测类工具", tool)
		}
	}
	for tool := range want {
		if !observabilityTools[tool] {
			t.Errorf("%s 应归类为观测类工具", tool)
		}
	}
	// K8s 类工具不得被误归类
	for _, k8s := range []string{"get_workload_state", "get_kubernetes_events", "inspect_dependencies"} {
		if observabilityTools[k8s] {
			t.Errorf("%s 是 K8s 类工具,必须走集群 agent", k8s)
		}
	}
}

func TestInvokeObservabilityRejectsNonObsTool(t *testing.T) {
	g := &Gateway{obs: &fakeObs{}}
	if _, err := g.invokeObservability(context.Background(), "get_workload_state", nil, agentclient.Scope{}); err == nil {
		t.Error("非观测类工具不应走直连路径")
	}
}
