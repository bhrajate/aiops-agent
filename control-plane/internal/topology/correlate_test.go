package topology

import (
	"io"
	"log/slog"
	"testing"

	"github.com/aiops/control-plane/internal/model"
	"github.com/aiops/control-plane/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestPrimaryService_ReducesPodToWorkload 这是本项最容易静默失效的地方。
//
// 拓扑边存的是裸服务名(Tempo 的 client/server 标签就是裸名),而 incident 的资源
// 可能是 Pod(checkout-7d9f4b8c6d-x2k9p)。不把 Pod 归约到工作负载,
// NeighborsOf 就永远查不到边 —— 拓扑关联静默失效,而**别处看不出任何异常**:
// incident 照常创建、诊断照常产出,只是 topology_refs 永远是空的。
// 与 F3 修过的 blast_radius 同一类坑(口径不一致导致的静默失配)。
func TestPrimaryService_ReducesPodToWorkload(t *testing.T) {
	inc := model.Incident{AffectedResources: []model.ResourceRef{
		{Kind: "Pod", Name: "checkout-7d9f4b8c6d-x2k9p", Namespace: "payment"},
	}}
	if got := primaryService(inc); got != "checkout" {
		t.Errorf("Pod 名应归约到工作负载 checkout, got %q", got)
	}
}

// TestPrimaryService_NoNamespacePrefix 必须是裸名,不能带 namespace 前缀。
//
// model.ServiceKey 返回 "namespace/name",用它去匹配 service_topology 里的裸名
// 会永远匹配不上。这条用例锁住这个区别 —— 它极容易在重构时被"统一用 ServiceKey"
// 的好意改坏。
func TestPrimaryService_NoNamespacePrefix(t *testing.T) {
	inc := model.Incident{AffectedResources: []model.ResourceRef{
		{Kind: "Deployment", Name: "checkout", Namespace: "payment"},
	}}
	got := primaryService(inc)
	if got != "checkout" {
		t.Errorf("应返回裸名 checkout, got %q", got)
	}
	// 显式断言不含 "/":拓扑边里没有带前缀的名字。
	for _, c := range got {
		if c == '/' {
			t.Errorf("服务名不该含 '/'(service_topology 存裸名): %q", got)
		}
	}
}

func TestPrimaryService_EmptyWhenNoResources(t *testing.T) {
	if got := primaryService(model.Incident{}); got != "" {
		t.Errorf("无资源时应为空, got %q", got)
	}
	// 资源存在但没有名字也应为空(否则会用空名去查拓扑,白查一次)
	inc := model.Incident{AffectedResources: []model.ResourceRef{{Namespace: "payment"}}}
	if got := primaryService(inc); got != "" {
		t.Errorf("资源无名字时应为空, got %q", got)
	}
}

func TestPrimaryNamespace(t *testing.T) {
	inc := model.Incident{AffectedResources: []model.ResourceRef{
		{Name: "a"}, // 无 namespace
		{Name: "b", Namespace: "payment"},
	}}
	if got := primaryNamespace(inc); got != "payment" {
		t.Errorf("应跳过无 namespace 的资源, got %q", got)
	}
}

// TestDefaultConfig_LinkStricterThanRefs 链接阈值必须严于回填阈值。
//
// 这是本项的核心取舍:回填 topology_refs 只是给 planner 更多上下文,错了代价小;
// 链接 incident 会出现在值班人员界面上,错了会**误导排查方向**。
// 两个阈值相等就失去了这个分级 —— K8s selector 边(0.7,只表达入口关系)
// 会被用来链接 incident,把"同一 Service 后的两个无关工作负载"判为同源。
func TestDefaultConfig_LinkStricterThanRefs(t *testing.T) {
	c := DefaultConfig()
	if c.MinLinkConfidence <= c.MinConfidence {
		t.Errorf("链接阈值(%v)必须严于回填阈值(%v)", c.MinLinkConfidence, c.MinConfidence)
	}
	// selector 边(0.7)应能进 refs 但不能链接。
	const selectorConf = 0.7
	if selectorConf < c.MinConfidence {
		t.Error("K8s selector 边应能进 topology_refs 供 planner 参考")
	}
	if selectorConf >= c.MinLinkConfidence {
		t.Error("K8s selector 边只表达入口关系,不该足以链接 incident")
	}
	// Tempo 边(0.9)必须能链接,否则整个能力形同虚设。
	if tempoConfidence < c.MinLinkConfidence {
		t.Error("Tempo 真实调用边必须足以链接 incident")
	}
}

// TestNew_ZeroConfigFallsBackToDefaults 零值配置不该产出"边龄 0 秒"这种废配置
// (那会过滤掉所有边,拓扑关联静默失效)。
func TestNew_ZeroConfigFallsBackToDefaults(t *testing.T) {
	c := New(nil, Config{}, testLogger())
	if c.cfg.MaxEdgeAgeSec <= 0 {
		t.Error("零值配置应回落到默认值,否则所有边都会被判为陈旧")
	}
	if c.cfg.MinLinkConfidence <= 0 {
		t.Error("零值链接阈值会让任何边都能链接 incident")
	}
}

// TestEdgeRef_CarriesDirectionAndProvenance topology_refs 会下发给 planner,
// 必须带方向与来源 —— 方向决定"谁更可能是根因",来源决定可信度。
func TestEdgeRef_CarriesDirectionAndProvenance(t *testing.T) {
	e := storeEdge()
	ref := edgeRef(e, "upstream", e.FromService, e.FromNamespace)
	for _, k := range []string{"direction", "service", "kind", "source", "confidence", "request_rate"} {
		if _, ok := ref[k]; !ok {
			t.Errorf("topology_refs 缺少字段 %q", k)
		}
	}
	if ref["direction"] != "upstream" {
		t.Errorf("方向错误: %v", ref["direction"])
	}
	if ref["service"] != "checkout" {
		t.Errorf("服务名错误: %v", ref["service"])
	}
}

// TestEnrich_NoServiceIsNoop 没有可用服务名时应直接返回,不查库。
// store 为 nil,一旦尝试查询就会 panic。
func TestEnrich_NoServiceIsNoop(t *testing.T) {
	c := New(nil, DefaultConfig(), testLogger())
	c.Enrich(nil, model.Incident{}) //nolint:staticcheck // 刻意传 nil ctx:不该走到用它的地方
}

// storeEdge 构造一条测试用边。
func storeEdge() store.TopologyEdge {
	return store.TopologyEdge{
		FromService: "checkout", ToService: "payment-api",
		Kind: "call", Source: SourceTempo, Confidence: tempoConfidence, RequestRate: 3.5,
	}
}
