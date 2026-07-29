package slo

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aiops/control-plane/internal/model"
	"github.com/aiops/control-plane/internal/obsquery"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeQ 按窗口返回预设错误率。
type fakeQ struct {
	byWindow map[string]float64
	err      error
	exprs    []string
}

func (f *fakeQ) InternalInstantQuery(_ context.Context, expr, _ string) ([]obsquery.InstantSample, error) {
	f.exprs = append(f.exprs, expr)
	if f.err != nil {
		return nil, f.err
	}
	for w, v := range f.byWindow {
		if strings.Contains(expr, "["+w+"]") {
			return []obsquery.InstantSample{{Value: v}}, nil
		}
	}
	return nil, nil // 无数据
}
func (f *fakeQ) HasPrometheus() bool { return true }

type fakeSink struct {
	sigs     []model.Signal
	inserted bool
}

func (f *fakeSink) InsertSignalWithOutbox(_ context.Context, s model.Signal) (bool, error) {
	f.sigs = append(f.sigs, s)
	// 模拟 ON CONFLICT:同 signal_id 第二次返回 false
	for _, prev := range f.sigs[:len(f.sigs)-1] {
		if prev.SignalID == s.SignalID {
			return false, nil
		}
	}
	f.inserted = true
	return true, nil
}

func testSLI() SLI {
	return SLI{
		Name: "checkout", Namespace: "payment", Service: "checkout", Objective: 0.999,
		ErrorRatioExpr: `sum(rate(http_requests_total{code=~"5.."}[$WINDOW])) / sum(rate(http_requests_total[$WINDOW]))`,
	}
}

func newW(q Querier, sink SignalSink) *Watcher {
	return NewWatcher(q, sink, []SLI{testSLI()}, "default", "prod-cn-1", time.Minute, testLogger())
}

// TestEvaluate_RequiresBothWindows 多窗口的核心语义:长窗超但短窗没超 → **不触发**。
//
// 这正是 SRE workbook 加短窗口要解决的问题:错误停止后长窗均值要过整个窗口才降
// 下来,单窗口会持续 fire 一小时。短窗是"仍在燃烧"的确认。
func TestEvaluate_RequiresBothWindows(t *testing.T) {
	budget := 1 - 0.999 // 0.001
	overFast := 14.4*budget + 0.001

	t.Run("两窗都超 → 触发", func(t *testing.T) {
		q := &fakeQ{byWindow: map[string]float64{"1h": overFast, "5m": overFast}}
		b, ok := newW(q, &fakeSink{}).evaluate(context.Background(), testSLI())
		if !ok {
			t.Fatal("两个窗口都超阈值应触发")
		}
		if b.Tier.Name != "fast" {
			t.Errorf("应命中 fast 档, got %q", b.Tier.Name)
		}
	})

	t.Run("长窗超短窗未超 → 不触发(燃烧已停止)", func(t *testing.T) {
		q := &fakeQ{byWindow: map[string]float64{"1h": overFast, "5m": 0}}
		// 慢档也要压住:3d 窗口若无数据会返回 no samples,不会误触发
		if _, ok := newW(q, &fakeSink{}).evaluate(context.Background(), testSLI()); ok {
			t.Error("短窗未超说明燃烧已停止,不该触发 —— 这是多窗口的全部意义")
		}
	})

	t.Run("都未超 → 不触发", func(t *testing.T) {
		q := &fakeQ{byWindow: map[string]float64{"1h": 0, "5m": 0, "6h": 0, "30m": 0, "72h": 0}}
		if _, ok := newW(q, &fakeSink{}).evaluate(context.Background(), testSLI()); ok {
			t.Error("未超阈值不该触发")
		}
	})
}

// TestEvaluate_PicksMostSevereTier 一次燃烧同时满足多档时只报最严重的。
//
// 14.4× 必然也满足 1×;全部产出会让同一次故障发出三条 signal ——
// 那正是 workbook 里"三条通知"问题。
func TestEvaluate_PicksMostSevereTier(t *testing.T) {
	huge := 100.0 // 远超所有档位
	q := &fakeQ{byWindow: map[string]float64{
		"1h": huge, "5m": huge, "6h": huge, "30m": huge, "72h": huge, "6h_": huge,
	}}
	b, ok := newW(q, &fakeSink{}).evaluate(context.Background(), testSLI())
	if !ok {
		t.Fatal("应触发")
	}
	if b.Tier.BurnRate != 14.4 {
		t.Errorf("应命中最严重档(14.4×), got %v", b.Tier.BurnRate)
	}
}

// TestEvaluate_NoDataIsNotBreach 无数据不等于越限。
// 服务刚上线或指标名写错都会走到这里,当越限处理会产出假故障。
func TestEvaluate_NoDataIsNotBreach(t *testing.T) {
	q := &fakeQ{byWindow: map[string]float64{}} // 所有窗口都无数据
	if _, ok := newW(q, &fakeSink{}).evaluate(context.Background(), testSLI()); ok {
		t.Error("无数据不该判为越限")
	}
}

// TestEvaluate_QueryErrorIsNotBreach 查询失败同样不该判越限。
func TestEvaluate_QueryErrorIsNotBreach(t *testing.T) {
	q := &fakeQ{err: errors.New("prometheus down")}
	if _, ok := newW(q, &fakeSink{}).evaluate(context.Background(), testSLI()); ok {
		t.Error("查询失败不该判为越限")
	}
}

// TestEvaluate_TakesMaxAcrossSeries 多序列取最大值,不取平均。
// 表达式可能按实例/路径分组;取平均会让局部故障被健康维度稀释掉。
func TestEvaluate_TakesMaxAcrossSeries(t *testing.T) {
	budget := 1 - 0.999
	over := 14.4*budget + 0.01
	q := &multiSeriesQ{values: []float64{0, 0, over}}
	w := NewWatcher(q, &fakeSink{}, []SLI{testSLI()}, "default", "c", time.Minute, testLogger())
	if _, ok := w.evaluate(context.Background(), testSLI()); !ok {
		t.Error("任一序列越限就该触发(取平均会让局部故障被稀释)")
	}
}

type multiSeriesQ struct{ values []float64 }

func (m *multiSeriesQ) InternalInstantQuery(_ context.Context, _ string, _ string) ([]obsquery.InstantSample, error) {
	out := make([]obsquery.InstantSample, 0, len(m.values))
	for _, v := range m.values {
		out = append(out, obsquery.InstantSample{Value: v})
	}
	return out, nil
}
func (m *multiSeriesQ) HasPrometheus() bool { return true }

// TestEmit_SameEpisodeDedupes 持续燃烧只产出一条 signal。
//
// 合成信号的身份由 fingerprint + startsAt 决定(F5)。若每轮用 now(),
// 持续燃烧会每轮一条新 signal,signal_count 暴涨并误触发 signal_burst
// (那个判据正是 F5 修过的坑)。
func TestEmit_SameEpisodeDedupes(t *testing.T) {
	budget := 1 - 0.999
	over := 14.4*budget + 0.001
	q := &fakeQ{byWindow: map[string]float64{"1h": over, "5m": over}}
	sink := &fakeSink{}
	w := newW(q, sink)

	for i := 0; i < 3; i++ {
		w.evaluateAll(context.Background())
	}
	if len(sink.sigs) != 3 {
		t.Fatalf("应尝试写入 3 次, got %d", len(sink.sigs))
	}
	// 三次的 signal_id 必须相同 —— store 的 ON CONFLICT 才能吃掉后两条。
	if sink.sigs[0].SignalID != sink.sigs[1].SignalID ||
		sink.sigs[1].SignalID != sink.sigs[2].SignalID {
		t.Errorf("同一燃烧片段内 signal_id 必须稳定,否则 signal_count 会暴涨: %v",
			[]string{sink.sigs[0].SignalID, sink.sigs[1].SignalID, sink.sigs[2].SignalID})
	}
}

// TestEmit_NewEpisodeAfterRecoveryIsDistinct 恢复后再次燃烧必须是新 signal。
//
// 反向属性:若 startsAt 固定不变,第二次故障会被当成重投递吃掉 —— 丢掉一次真实故障。
func TestEmit_NewEpisodeAfterRecoveryIsDistinct(t *testing.T) {
	budget := 1 - 0.999
	over := 14.4*budget + 0.001
	sink := &fakeSink{}

	burning := &fakeQ{byWindow: map[string]float64{"1h": over, "5m": over}}
	w := newW(burning, sink)
	w.evaluateAll(context.Background())
	first := sink.sigs[0].SignalID

	// 恢复:所有窗口归零 → 片段状态被清掉
	w.q = &fakeQ{byWindow: map[string]float64{"1h": 0, "5m": 0, "6h": 0, "30m": 0, "72h": 0}}
	w.evaluateAll(context.Background())

	// 再次燃烧(时间前进,新片段有新 startsAt)
	time.Sleep(1100 * time.Millisecond) // startsAt 截断到秒,需跨过一秒
	w.q = burning
	w.evaluateAll(context.Background())

	last := sink.sigs[len(sink.sigs)-1].SignalID
	if last == first {
		t.Error("恢复后再次燃烧必须是新 signal,否则第二次故障被当成重投递丢掉")
	}
}

// TestBuildSignal_UsesSharedIDDerivation 合成信号必须用与 webhook 路径同一套
// 幂等规则,否则两类信号的去重行为不一致,而这种不一致只在生产的重复数据里显现。
func TestBuildSignal_UsesSharedIDDerivation(t *testing.T) {
	w := newW(&fakeQ{}, &fakeSink{})
	starts := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	b := Breach{SLI: testSLI(), Tier: DefaultTiers()[0], LongRate: 0.05, ShortRate: 0.06, Threshold: 0.0144}
	sig := w.buildSignal(b, starts)

	want := model.DeriveSignalID(model.SignalIdentity{
		Fingerprint: "slo-default-prod-cn-1-checkout-fast",
		Status:      "firing",
		StartsAt:    starts,
	})
	if sig.SignalID != want {
		t.Errorf("signal_id 应由 model.DeriveSignalID 推导: got %q want %q", sig.SignalID, want)
	}
	if sig.SignalID == "" {
		t.Error("signal_id 不能为空(表上是主键)")
	}
}

// TestBuildSignal_LabelsDriveDownstream 标签必须让下游能正确分类与定位。
func TestBuildSignal_LabelsDriveDownstream(t *testing.T) {
	w := newW(&fakeQ{}, &fakeSink{})
	b := Breach{SLI: testSLI(), Tier: DefaultTiers()[0]}
	sig := w.buildSignal(b, time.Now())

	if sig.Source != SignalSource {
		t.Errorf("source 应为 %q(便于回答'多少故障是主动发现的'), got %q", SignalSource, sig.Source)
	}
	if sig.Labels["severity"] != "critical" {
		t.Errorf("14.4× 档应映射到 critical(→P1), got %q", sig.Labels["severity"])
	}
	if sig.Labels["namespace"] != "payment" {
		t.Error("namespace 标签缺失会让 ABAC 与相关性合并失效")
	}
	if sig.Labels["service"] != "checkout" {
		t.Error("service 标签缺失会让资源引用无法定位")
	}
	if sig.ResourceRef.Namespace != "payment" || sig.ResourceRef.Name != "checkout" {
		t.Errorf("ResourceRef 应指向具体服务: %+v", sig.ResourceRef)
	}
	// burn_rate 进标签,使值班人员在信号层面就能判断严重性
	if sig.Labels["burn_rate"] == "" {
		t.Error("缺少 burn_rate 标签")
	}
}

// TestSeverityMapping_NeverP4 燃尽率能触发说明用户已在受损,不该是"无关紧要"。
func TestSeverityMapping_NeverP4(t *testing.T) {
	for _, tier := range DefaultTiers() {
		if tier.Severity == "info" || tier.Severity == "" {
			t.Errorf("档位 %q 的严重度 %q 会归一化为 P4 —— 燃尽率触发意味着用户在受损",
				tier.Name, tier.Severity)
		}
	}
}

// TestDefaultTiers_MatchesSREWorkbook 参数必须与 SRE workbook 表 5-8 一致,
// 且短窗约为长窗的 1/12(workbook 的经验值)。
func TestDefaultTiers_MatchesSREWorkbook(t *testing.T) {
	want := []struct {
		burn        float64
		long, short time.Duration
	}{
		{14.4, time.Hour, 5 * time.Minute},
		{6, 6 * time.Hour, 30 * time.Minute},
		{1, 72 * time.Hour, 6 * time.Hour},
	}
	tiers := DefaultTiers()
	if len(tiers) != len(want) {
		t.Fatalf("档位数 = %d, want %d", len(tiers), len(want))
	}
	for i, w := range want {
		got := tiers[i]
		if got.BurnRate != w.burn || got.LongWindow != w.long || got.ShortWindow != w.short {
			t.Errorf("档位 %d = %v/%v/%v, want %v/%v/%v", i,
				got.BurnRate, got.LongWindow, got.ShortWindow, w.burn, w.long, w.short)
		}
		// 短窗 ≈ 长窗/12
		ratio := float64(got.LongWindow) / float64(got.ShortWindow)
		if ratio < 10 || ratio > 14 {
			t.Errorf("档位 %d 的长短窗比 %.1f 偏离 workbook 建议的 12", i, ratio)
		}
	}
}
