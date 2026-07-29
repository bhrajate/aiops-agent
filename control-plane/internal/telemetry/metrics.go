// Package telemetry 提供 Prometheus 指标与 OpenTelemetry 追踪(架构第 16 节)。
package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics 汇集控制面核心指标(架构第 16 节)。
type Metrics struct {
	SignalsIngested       *prometheus.CounterVec // 按 source 维度
	IncidentsCreated      *prometheus.CounterVec // 按 severity、fault_category 维度
	InvestigationsStarted prometheus.Counter
	ToolInvokes           *prometheus.CounterVec   // 按 tool、result 维度
	ToolLatency           *prometheus.HistogramVec // 按 tool 维度
	DeadLetters           *prometheus.CounterVec   // 按 topic 维度
	AuthDenials           *prometheus.CounterVec   // 按 reason 维度
	RetentionPurged       *prometheus.CounterVec   // 按 target 维度
	IngressThrottled      *prometheus.CounterVec   // 按 tenant 维度
	TriggerDecisions      *prometheus.CounterVec   // 按 triggered、reason 维度
	SLOEvaluations        *prometheus.CounterVec   // 按 sli、breached 维度
	TopologyEdges         prometheus.Gauge         // 最近一次同步的边数
	TopologySyncErrors    prometheus.Counter       // 同步失败次数
	outcome               *Outcome                 // 成效与成本(F10),见 outcome.go
	reg                   *prometheus.Registry
}

// New 创建并注册指标。
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		SignalsIngested: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_signals_ingested_total", Help: "Signals accepted by ingress"}, []string{"source"}),
		IncidentsCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_incidents_created_total", Help: "Incidents created"}, []string{"severity", "fault_category"}),
		InvestigationsStarted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "aiops_investigations_started_total", Help: "Investigations started"}),
		ToolInvokes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_tool_invocations_total", Help: "Tool Gateway invocations"}, []string{"tool", "result"}),
		ToolLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "aiops_tool_latency_seconds", Help: "Tool invocation latency",
			Buckets: prometheus.DefBuckets}, []string{"tool"}),
		DeadLetters: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_dead_letters_total", Help: "Messages dead-lettered"}, []string{"topic"}),
		AuthDenials: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_auth_denials_total", Help: "Authz denials"}, []string{"reason"}),
		RetentionPurged: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_retention_purged_rows_total",
			Help: "Rows deleted by the retention janitor"}, []string{"target"}),
		IngressThrottled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_ingress_throttled_total",
			Help: "Signal ingress requests rejected by rate limiting"}, []string{"tenant"}),
		// 自动触发决策(F7)。按 reason 分维度是关键:只看总量无法回答
		// "跳过的都是些什么",而那正是调阈值时唯一需要的信息。
		TriggerDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_trigger_decisions_total",
			Help: "Auto-trigger policy decisions by outcome and reason"},
			[]string{"triggered", "reason"}),
		// SLO 评估。按 breached 分维度:只有"评估了多少次"没用,
		// 要能回答"有多少次真的越限"——那是主动检测产出价值的直接度量。
		SLOEvaluations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_slo_evaluations_total",
			Help: "SLO burn-rate evaluations by SLI and outcome"}, []string{"sli", "breached"}),
		// 拓扑同步可观测性。边数用 Gauge:它是存量而非增量,
		// 且"突然掉到 0"正是 metrics-generator 挂了的信号。
		TopologyEdges: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "aiops_topology_edges",
			Help: "Service dependency edges from the last successful sync"}),
		TopologySyncErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "aiops_topology_sync_errors_total",
			Help: "Topology sync failures"}),
		outcome: newOutcome(),
		reg:     reg,
	}
	reg.MustRegister(m.SignalsIngested, m.IncidentsCreated, m.InvestigationsStarted,
		m.ToolInvokes, m.ToolLatency, m.DeadLetters, m.AuthDenials,
		m.RetentionPurged, m.IngressThrottled, m.TriggerDecisions,
		m.TopologyEdges, m.TopologySyncErrors, m.SLOEvaluations)
	reg.MustRegister(m.outcome.collectors()...)
	// Go runtime + process 指标
	reg.MustRegister(prometheus.NewGoCollector())
	return m
}

// Handler 返回 /metrics 处理器。
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Nil-safe 包装:Metrics 为 nil 时各方法空操作,便于降级。
func (m *Metrics) IncSignal(source string) {
	if m != nil {
		m.SignalsIngested.WithLabelValues(source).Inc()
	}
}
func (m *Metrics) IncIncident(severity, category string) {
	if m != nil {
		m.IncidentsCreated.WithLabelValues(severity, category).Inc()
	}
}
func (m *Metrics) IncInvestigation() {
	if m != nil {
		m.InvestigationsStarted.Inc()
	}
}
func (m *Metrics) ObserveTool(tool, result string, seconds float64) {
	if m != nil {
		m.ToolInvokes.WithLabelValues(tool, result).Inc()
		m.ToolLatency.WithLabelValues(tool).Observe(seconds)
	}
}
func (m *Metrics) IncDeadLetter(topic string) {
	if m != nil {
		m.DeadLetters.WithLabelValues(topic).Inc()
	}
}
func (m *Metrics) IncAuthDenial(reason string) {
	if m != nil {
		m.AuthDenials.WithLabelValues(reason).Inc()
	}
}
func (m *Metrics) ObserveRetentionPurge(target string, rows int) {
	if m != nil && rows > 0 {
		m.RetentionPurged.WithLabelValues(target).Add(float64(rows))
	}
}
func (m *Metrics) IncIngressThrottled(tenant string) {
	if m != nil {
		m.IngressThrottled.WithLabelValues(tenant).Inc()
	}
}

// ObserveSLOEvaluation 记录一次 SLO 评估。
func (m *Metrics) ObserveSLOEvaluation(sli string, breached bool) {
	if m == nil {
		return
	}
	flag := "false"
	if breached {
		flag = "true"
	}
	m.SLOEvaluations.WithLabelValues(sli, flag).Inc()
}

// ObserveTopologySync 记录一次拓扑同步结果。
func (m *Metrics) ObserveTopologySync(edges int, err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.TopologySyncErrors.Inc()
		return // 失败时**不更新**边数:写 0 会与"真的没有边"混淆
	}
	m.TopologyEdges.Set(float64(edges))
}

// IncTriggerDecision 记录一次自动触发决策(F7)。
func (m *Metrics) IncTriggerDecision(triggered bool, reason string) {
	if m != nil {
		flag := "false"
		if triggered {
			flag = "true"
		}
		m.TriggerDecisions.WithLabelValues(flag, reason).Inc()
	}
}
