package telemetry

import (
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestObserveUsage_ExportsCostAndTokens F10 的基本契约:usage 必须真的出现在指标上。
// 此前这些数据只落库,Prometheus 上完全看不到。
func TestObserveUsage_ExportsCostAndTokens(t *testing.T) {
	m := New()
	m.ObserveUsage(12000, 0.42, 30, 7, 2)

	if got := testutil.ToFloat64(m.outcome.TokensTotal); got != 12000 {
		t.Errorf("tokens 累计 = %v, want 12000", got)
	}
	if got := testutil.ToFloat64(m.outcome.CostUSDTotal); got != 0.42 {
		t.Errorf("费用累计 = %v, want 0.42", got)
	}
	if got := testutil.ToFloat64(m.outcome.UngroundedDowngrades); got != 2 {
		t.Errorf("无证据降级数 = %v, want 2", got)
	}
	if n := testutil.CollectAndCount(m.outcome.TokensPerInv); n != 1 {
		t.Errorf("每次调查 token 直方图应有 1 个 series, got %d", n)
	}
}

// TestObserveUsage_SkipsZeros 零值不该被观测。
//
// tokens/cost 为 0 通常是中途上报或降级路径(模型未被调用)。计入会把分布拉向 0、
// 压低 P99 —— 于是"某次调查异常昂贵"这件事在指标上被稀释掉,
// 而那正是这个直方图唯一的用途。
func TestObserveUsage_SkipsZeros(t *testing.T) {
	m := New()
	for i := 0; i < 50; i++ {
		m.ObserveUsage(0, 0, 0, 0, 0) // 降级路径:模型未被调用
	}
	m.ObserveUsage(100000, 1.8, 120, 20, 0) // 一次昂贵的调查

	// 直方图里应只有那一次昂贵调查,不被 50 个 0 稀释。
	out := dumpHistogram(t, m, "aiops_investigation_cost_usd")
	if !strings.Contains(out, "count:1") {
		t.Errorf("零值应被跳过,直方图样本数应为 1,got:\n%s", out)
	}
	// 累计值也不该被 0 影响
	if got := testutil.ToFloat64(m.outcome.CostUSDTotal); got != 1.8 {
		t.Errorf("费用累计 = %v, want 1.8", got)
	}
}

// TestObserveDiagnosis_SkipsZeroLatency 时延为 0(拿不到起始时间)时只计数不观测。
// 观测 0 会把 P99 拉低,让"诊断变慢"在指标上消失。
func TestObserveDiagnosis_SkipsZeroLatency(t *testing.T) {
	m := New()
	m.ObserveDiagnosis("root_cause_identified", 0) // 拿不到起始时间
	m.ObserveDiagnosis("root_cause_identified", 45)

	// 计数两次
	if got := testutil.ToFloat64(m.outcome.DiagnosisPublished.WithLabelValues("root_cause_identified")); got != 2 {
		t.Errorf("发布计数 = %v, want 2", got)
	}
	// 时延只观测一次
	out := dumpHistogram(t, m, "aiops_diagnosis_latency_seconds")
	if !strings.Contains(out, "count:1") {
		t.Errorf("零时延应被跳过,样本数应为 1,got:\n%s", out)
	}
}

// TestObserveDiagnosis_EmptyStatusLabelled 空 status 不该产出空标签值。
// 空标签在 Grafana 里显示为空白行,分不清是"没有数据"还是"状态未知"。
func TestObserveDiagnosis_EmptyStatusLabelled(t *testing.T) {
	m := New()
	m.ObserveDiagnosis("", 10)
	if got := testutil.ToFloat64(m.outcome.DiagnosisPublished.WithLabelValues("unknown")); got != 1 {
		t.Errorf("空 status 应记为 unknown, got %v", got)
	}
}

// TestIncHumanFeedback_ByAction 采纳率的分子分母都要在。
func TestIncHumanFeedback_ByAction(t *testing.T) {
	m := New()
	m.IncHumanFeedback("confirm")
	m.IncHumanFeedback("confirm")
	m.IncHumanFeedback("reject")
	m.IncHumanFeedback("correct")
	m.IncHumanFeedback("") // 兜底

	for action, want := range map[string]float64{
		"confirm": 2, "reject": 1, "correct": 1, "unknown": 1,
	} {
		if got := testutil.ToFloat64(m.outcome.HumanFeedback.WithLabelValues(action)); got != want {
			t.Errorf("action=%s 计数 = %v, want %v", action, got, want)
		}
	}
}

// TestOutcome_NilSafe 降级路径不应 panic。
func TestOutcome_NilSafe(t *testing.T) {
	var m *Metrics
	m.ObserveUsage(1, 1, 1, 1, 1)
	m.ObserveDiagnosis("x", 1)
	m.IncHumanFeedback("confirm")
}

// TestOutcome_AllSeriesRegistered 九个指标都要在注册表里,否则永远抓不到。
func TestOutcome_AllSeriesRegistered(t *testing.T) {
	m := New()
	// 先各打一次,让带 label 的 series 出现。
	m.ObserveUsage(1000, 0.1, 10, 3, 1)
	m.ObserveDiagnosis("root_cause_identified", 20)
	m.IncHumanFeedback("confirm")

	mfs, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	have := map[string]bool{}
	for _, mf := range mfs {
		have[mf.GetName()] = true
	}
	for _, name := range []string{
		"aiops_model_tokens_total",
		"aiops_model_cost_usd_total",
		"aiops_investigation_tokens",
		"aiops_investigation_cost_usd",
		"aiops_investigation_tool_calls",
		"aiops_diagnosis_latency_seconds",
		"aiops_diagnosis_published_total",
		"aiops_human_feedback_total",
		"aiops_ungrounded_downgrades_total",
	} {
		if !have[name] {
			t.Errorf("%s 未注册:告警规则引用它会永不触发", name)
		}
	}
}

// dumpHistogram 渲染指定直方图的原始文本,便于断言样本数。
func dumpHistogram(t *testing.T, m *Metrics, name string) string {
	t.Helper()
	mfs, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		var sb strings.Builder
		for _, mm := range mf.GetMetric() {
			if h := mm.GetHistogram(); h != nil {
				sb.WriteString("count:")
				sb.WriteString(strconv.FormatUint(h.GetSampleCount(), 10))
				sb.WriteString(" sum:")
				sb.WriteString(strconv.FormatFloat(h.GetSampleSum(), 'f', -1, 64))
			}
		}
		return sb.String()
	}
	return "(未找到 " + name + ")"
}
