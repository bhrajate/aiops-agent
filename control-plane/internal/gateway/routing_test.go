package gateway

import (
	"context"
	"testing"

	"github.com/aiops/control-plane/internal/agentclient"
)

type fakeInvoker struct{ name string }

func (f *fakeInvoker) Invoke(_ context.Context, _ string, _ map[string]any, _ agentclient.Scope) (agentclient.ToolResult, error) {
	return agentclient.ToolResult{Source: f.name}, nil
}

func TestInvokerFor_ObservabilityGoesCentral(t *testing.T) {
	clusterAgent := &fakeInvoker{name: "cluster"}
	central := &fakeInvoker{name: "central"}
	g := &Gateway{
		agents: agentclient.NewRegistry(map[string]*agentclient.Client{}, nil),
		obs:    central,
	}
	// registry 空但有 fallback? 这里用 obs 覆盖观测类;K8s 类需要 agent。
	// 为隔离路由逻辑,单独构造带 fallback 的 registry:
	g.agents = agentclient.NewRegistry(nil, nil)
	_ = clusterAgent

	// 观测类 → 中心
	for _, tool := range []string{"query_metrics", "search_logs", "get_traces"} {
		inv, err := g.invokerFor(tool, "prod-cn-1")
		if err != nil || inv != central {
			t.Errorf("%s 应路由到中心 obs agent, err=%v", tool, err)
		}
	}
}

func TestInvokerFor_K8sGoesPerCluster(t *testing.T) {
	central := &fakeInvoker{name: "central"}
	a := agentclient.New("http://a:9100")
	g := &Gateway{
		agents: agentclient.NewRegistry(map[string]*agentclient.Client{"prod-cn-1": a}, nil),
		obs:    central,
	}
	// K8s 类 → 该集群 agent(不是 central)
	for _, tool := range []string{"get_workload_state", "get_kubernetes_events", "inspect_dependencies"} {
		inv, err := g.invokerFor(tool, "prod-cn-1")
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		if inv == ToolInvoker(central) {
			t.Errorf("%s 不应走中心 obs agent", tool)
		}
	}
	// K8s 类打到未配置集群 → 拒绝(不回退)
	if _, err := g.invokerFor("get_workload_state", "unknown"); err == nil {
		t.Error("未配置集群的 K8s 工具应被拒绝")
	}
}

func TestInvokerFor_NoCentralFallsBackToCluster(t *testing.T) {
	// 未配置中心 obs agent 时,观测类回退到集群 agent(每集群自带后端)
	a := agentclient.New("http://a:9100")
	g := &Gateway{
		agents: agentclient.NewRegistry(map[string]*agentclient.Client{"prod-cn-1": a}, nil),
		obs:    nil,
	}
	inv, err := g.invokerFor("query_metrics", "prod-cn-1")
	if err != nil || inv == nil {
		t.Errorf("无中心 agent 时观测类应回退到集群 agent, err=%v", err)
	}
}
