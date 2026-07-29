package telemetry

// 成效与成本指标(F10)。
//
// 原问题:`investigations.usage` 里一直有 tokens / cost_usd / elapsed_sec /
// tool_calls / ungrounded_downgrades,但**从未导出到 Prometheus**。于是:
//   - 成本不可见:模型花了多少钱只能查库逐条累加,没有告警、没有趋势;
//   - 成效不可见:诊断结论有多少被人工采纳、多少被否决,数据在 human_feedback
//     表里却没有任何聚合视图。
// 队列可观测性(P4)解决了"系统是否在工作",这一项解决"工作得值不值"。
//
// 几条设计取舍:
//
//  1. **成本同时用 Counter 与 Histogram。** 二者回答不同问题:Counter 答"这个月
//     花了多少"(可 rate() 看烧钱速度),Histogram 答"是否有单次调查异常昂贵"
//     (P99 突增通常意味着某类故障让 RCA 循环收不住)。只有 Counter 时,
//     一次失控的调查会被整体均值稀释掉。
//
//  2. **人工反馈按 action 分维度,不预先算成比率。** 采纳率 =
//     confirm / (confirm + correct + reject),用 PromQL 现算即可。把比率固化成
//     指标会丢掉分子分母,而"否决了多少"本身就是要看的数 ——
//     且低采纳率与低反馈量是完全不同的问题,合成一个数就分不开了。
//
//  3. **不导出 MTTR。** MTTR 混合了系统性能与人的响应速度(值班多久看到告警、
//     多久动手),把它当系统指标会得出错误结论。这里导出的是
//     `diagnosis_latency_seconds`:从调查开始到首次给出结论,纯系统耗时。
//     真正的 MTTR 应由事件管理侧统计,那里才有人工响应的时间戳。
//
//  4. **ungrounded_downgrades 单独导出。** 它是模型质量信号(模型声称已确认却
//     拿不出实时证据,被确定性守卫降级),不是成本维度。它上升意味着
//     prompt/模型退化,应在放开自动化范围前先看它。

import "github.com/prometheus/client_golang/prometheus"

// Outcome 汇集成效与成本指标。
type Outcome struct {
	// 成本
	TokensTotal      prometheus.Counter
	CostUSDTotal     prometheus.Counter
	TokensPerInv     prometheus.Histogram
	CostPerInv       prometheus.Histogram
	ToolCallsPerInv  prometheus.Histogram
	DiagnosisLatency prometheus.Histogram

	// 成效
	DiagnosisPublished   *prometheus.CounterVec // 按 status
	HumanFeedback        *prometheus.CounterVec // 按 action
	UngroundedDowngrades prometheus.Counter
}

func newOutcome() *Outcome {
	return &Outcome{
		TokensTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "aiops_model_tokens_total",
			Help: "Cumulative model tokens consumed by investigations"}),
		CostUSDTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "aiops_model_cost_usd_total",
			Help: "Cumulative model cost in USD"}),
		TokensPerInv: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "aiops_investigation_tokens",
			Help: "Model tokens per investigation",
			// 上界按 Budget 默认 max_tokens(200k)的量级选,越界落入 +Inf 桶
			// 仍可见 —— 那本身就是"预算被打满"的信号。
			Buckets: []float64{1e3, 5e3, 1e4, 5e4, 1e5, 2e5, 5e5}}),
		CostPerInv: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "aiops_investigation_cost_usd",
			Help: "Model cost in USD per investigation",
			// 按 Budget 默认 max_cost_usd(2 美元)展开,兼顾更贵的离群值。
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10}}),
		ToolCallsPerInv: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "aiops_investigation_tool_calls",
			Help:    "Tool invocations per investigation",
			Buckets: []float64{1, 3, 5, 10, 20, 50}}),
		DiagnosisLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "aiops_diagnosis_latency_seconds",
			Help: "Seconds from investigation start to first published diagnosis (system time only, not MTTR)",
			// 目标是分钟级;上界到 30 分钟,更久的落 +Inf(那已是"太慢了")。
			Buckets: []float64{5, 15, 30, 60, 120, 300, 600, 1800}}),
		DiagnosisPublished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_diagnosis_published_total",
			Help: "Diagnoses published by status"}, []string{"status"}),
		HumanFeedback: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_human_feedback_total",
			Help: "Human feedback by action; adoption rate = confirm / sum(confirm,correct,reject)"},
			[]string{"action"}),
		UngroundedDowngrades: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "aiops_ungrounded_downgrades_total",
			Help: "Hypotheses downgraded for lacking real-time evidence (model quality signal)"}),
	}
}

func (o *Outcome) collectors() []prometheus.Collector {
	return []prometheus.Collector{
		o.TokensTotal, o.CostUSDTotal, o.TokensPerInv, o.CostPerInv,
		o.ToolCallsPerInv, o.DiagnosisLatency, o.DiagnosisPublished,
		o.HumanFeedback, o.UngroundedDowngrades,
	}
}

// ObserveUsage 记录一次调查的用量。由 AI Worker 回写 usage 时调用。
//
// 只在**有意义**时观测:tokens/cost 为 0 通常是中途上报或降级路径,
// 计入会把分布拉向 0 并压低 P99,掩盖真正昂贵的调查。
func (m *Metrics) ObserveUsage(tokens int, costUSD, elapsedSec float64, toolCalls, ungroundedDowngrades int) {
	if m == nil || m.outcome == nil {
		return
	}
	o := m.outcome
	if tokens > 0 {
		o.TokensTotal.Add(float64(tokens))
		o.TokensPerInv.Observe(float64(tokens))
	}
	if costUSD > 0 {
		o.CostUSDTotal.Add(costUSD)
		o.CostPerInv.Observe(costUSD)
	}
	if toolCalls > 0 {
		o.ToolCallsPerInv.Observe(float64(toolCalls))
	}
	if ungroundedDowngrades > 0 {
		o.UngroundedDowngrades.Add(float64(ungroundedDowngrades))
	}
}

// ObserveDiagnosis 记录一次诊断发布及其时延。
// latencySec <= 0 表示拿不到起始时间,此时只计数不观测时延 ——
// 观测 0 会把 P99 拉低,让"诊断变慢"这件事在指标上消失。
func (m *Metrics) ObserveDiagnosis(status string, latencySec float64) {
	if m == nil || m.outcome == nil {
		return
	}
	if status == "" {
		status = "unknown"
	}
	m.outcome.DiagnosisPublished.WithLabelValues(status).Inc()
	if latencySec > 0 {
		m.outcome.DiagnosisLatency.Observe(latencySec)
	}
}

// IncHumanFeedback 记录一次人工反馈。
func (m *Metrics) IncHumanFeedback(action string) {
	if m == nil || m.outcome == nil {
		return
	}
	if action == "" {
		action = "unknown"
	}
	m.outcome.HumanFeedback.WithLabelValues(action).Inc()
}
