package server

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metrics 保存 cluster-agent 的 Prometheus 计数器(架构 §16)。
type metrics struct {
	toolCalls   *prometheus.CounterVec   // 按 tool、status 维度
	toolLatency *prometheus.HistogramVec // 按 tool 维度
	reg         *prometheus.Registry
}

func newMetrics() *metrics {
	reg := prometheus.NewRegistry()
	m := &metrics{
		toolCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aiops_agent_tool_calls_total", Help: "Cluster-agent tool invocations"},
			[]string{"tool", "status"}),
		toolLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "aiops_agent_tool_latency_seconds", Help: "Tool latency",
			Buckets: prometheus.DefBuckets}, []string{"tool"}),
		reg: reg,
	}
	reg.MustRegister(m.toolCalls, m.toolLatency, prometheus.NewGoCollector())
	return m
}

func (m *metrics) observe(tool, status string, seconds float64) {
	m.toolCalls.WithLabelValues(tool, status).Inc()
	m.toolLatency.WithLabelValues(tool).Observe(seconds)
}

func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
