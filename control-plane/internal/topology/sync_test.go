package topology

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiops/control-plane/internal/obsquery"
)

type fakeQuerier struct {
	samples []obsquery.InstantSample
	err     error
	gotExpr string
	gotCID  string
	calls   int
}

func (f *fakeQuerier) InternalInstantQuery(_ context.Context, expr, clusterID string) ([]obsquery.InstantSample, error) {
	f.calls++
	f.gotExpr, f.gotCID = expr, clusterID
	return f.samples, f.err
}
func (f *fakeQuerier) HasPrometheus() bool { return true }

func newSyncer(q Querier) *Syncer {
	return NewSyncer(nil, q, "default", "prod-cn-1", 0, testLogger())
}

// TestToEdges_MapsServiceGraphLabels service graph 的 client/server 就是边的两端。
func TestToEdges_MapsServiceGraphLabels(t *testing.T) {
	s := newSyncer(&fakeQuerier{})
	edges := s.toEdges([]obsquery.InstantSample{
		{Labels: map[string]string{"client": "checkout", "server": "payment-api"}, Value: 12.5},
	})
	if len(edges) != 1 {
		t.Fatalf("应产出 1 条边, got %d", len(edges))
	}
	e := edges[0]
	if e.FromService != "checkout" || e.ToService != "payment-api" {
		t.Errorf("边方向错误: %s → %s(client 是调用方)", e.FromService, e.ToService)
	}
	if e.RequestRate != 12.5 {
		t.Errorf("请求量 = %v, want 12.5", e.RequestRate)
	}
	if e.Source != SourceTempo {
		t.Errorf("来源 = %q, want %q", e.Source, SourceTempo)
	}
	// 置信度必须高于链接阈值,否则 Tempo 边也无法链接 incident,整个能力形同虚设。
	if e.Confidence < DefaultConfig().MinLinkConfidence {
		t.Errorf("Tempo 边置信度 %v 低于链接阈值 %v:真实调用边必须足以支撑关联",
			e.Confidence, DefaultConfig().MinLinkConfidence)
	}
}

// TestToEdges_DropsSelfLoopsAndPartials 自环与残缺边必须丢掉。
// 残缺边在关联时匹配不上任何资源,留着只是噪声;自环是同服务内部 span。
func TestToEdges_DropsSelfLoopsAndPartials(t *testing.T) {
	s := newSyncer(&fakeQuerier{})
	edges := s.toEdges([]obsquery.InstantSample{
		{Labels: map[string]string{"client": "a", "server": "a"}, Value: 1},   // 自环
		{Labels: map[string]string{"client": "", "server": "b"}, Value: 1},    // 缺 client
		{Labels: map[string]string{"client": "c", "server": ""}, Value: 1},    // 缺 server
		{Labels: map[string]string{"client": " d ", "server": "e"}, Value: 1}, // 带空格,应保留
	})
	if len(edges) != 1 {
		t.Fatalf("只应保留 1 条边, got %d", len(edges))
	}
	if edges[0].FromService != "d" {
		t.Errorf("应 trim 空格, got %q", edges[0].FromService)
	}
}

// TestConnectionKind Tempo 的 connection_type 映射。
// virtual_node 单独标出:对端未插桩、由 Tempo 推断,比真实节点更可能是幻影。
func TestConnectionKind(t *testing.T) {
	cases := map[string]string{
		"":                 "call",
		"unset":            "call",
		"database":         "database",
		"messaging_system": "messaging",
		"virtual_node":     "virtual",
		"DATABASE":         "database", // 大小写无关
		" database ":       "database",
	}
	for in, want := range cases {
		if got := connectionKind(in); got != want {
			t.Errorf("connectionKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestServiceGraphExpr_IsHardcodedAndFilters 表达式必须写死且过滤 rate=0。
//
// 写死是安全要求:InternalInstantQuery 绕过了工具路径的范围注入,
// 一旦允许传表达式就成了后门(见 obsquery/internalquery.go)。
func TestServiceGraphExpr_IsHardcodedAndFilters(t *testing.T) {
	if !strings.Contains(serviceGraphExpr, "traces_service_graph_request_total") {
		t.Error("应查 Tempo service graph 指标")
	}
	if !strings.Contains(serviceGraphExpr, "> 0") {
		t.Error("必须过滤 rate=0:窗口内无调用意味着该依赖此刻不成立")
	}
	if !strings.Contains(serviceGraphExpr, "client") || !strings.Contains(serviceGraphExpr, "server") {
		t.Error("必须按 client/server 聚合,否则拿不到边的两端")
	}
}

// TestSyncOnce_PassesClusterID 集群维度必须传下去。
// 共享后端下不带 cluster 会把别的集群的拓扑混进来,而那个错误在诊断结论里
// 看不出来 —— 拓扑图看着完整、逻辑自洽,只是画的是别人的集群。
func TestSyncOnce_PassesClusterID(t *testing.T) {
	q := &fakeQuerier{}
	s := newSyncer(q)
	s.syncOnce(context.Background())
	if q.gotCID != "prod-cn-1" {
		t.Errorf("clusterID 未传下去: %q", q.gotCID)
	}
	if q.gotExpr != serviceGraphExpr {
		t.Error("表达式应是硬编码常量,不该被改写")
	}
}

// TestSyncOnce_QueryErrorDoesNotPanic 查询失败只记日志,不 panic、不写库。
func TestSyncOnce_QueryErrorDoesNotPanic(t *testing.T) {
	q := &fakeQuerier{err: errors.New("prometheus down")}
	s := newSyncer(q)
	s.syncOnce(context.Background()) // store 为 nil:一旦尝试写库就会 panic
}

// TestSyncOnce_EmptyWarnsOnce "查到 0 条边"的告警只在状态变化时打一次。
//
// 这条告警很重要(未启用 metrics-generator 时拓扑关联完全不生效,而那在别处
// 看不出任何异常),但每周期一条会把日志淹掉。
func TestSyncOnce_EmptyWarnsOnce(t *testing.T) {
	q := &fakeQuerier{}
	s := newSyncer(q)
	if s.warnedEmpty {
		t.Fatal("初始不应已告警")
	}
	s.syncOnce(context.Background())
	if !s.warnedEmpty {
		t.Error("首次查到 0 条边应告警")
	}
	s.syncOnce(context.Background())
	if !s.warnedEmpty {
		t.Error("仍为空时应保持已告警状态(不重复打)")
	}
}

// TestObserveTopologySync_ErrorDoesNotZeroGauge 同步失败不该把边数写成 0。
// 写 0 会与"真的没有边"混淆,而两者的处置完全不同。
func TestObserveTopologySync_ErrorDoesNotZeroGauge(t *testing.T) {
	// 这个属性在 telemetry 包实现,这里只锁住 Syncer 会把 err 传下去。
	q := &fakeQuerier{err: errors.New("boom")}
	rec := &recordingSyncMetrics{}
	s := newSyncer(q).WithMetrics(rec)
	s.syncOnce(context.Background())
	if rec.lastErr == nil {
		t.Error("同步失败应把 error 传给指标记录器,由它决定不更新 gauge")
	}
	if rec.lastEdges != 0 {
		t.Errorf("失败时边数应为 0(记录器据此跳过更新), got %d", rec.lastEdges)
	}
}

type recordingSyncMetrics struct {
	lastEdges int
	lastErr   error
	calls     int
}

func (r *recordingSyncMetrics) ObserveTopologySync(edges int, err error) {
	r.calls++
	r.lastEdges, r.lastErr = edges, err
}
