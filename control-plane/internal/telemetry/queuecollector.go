package telemetry

// 队列积压指标(P4)——用 Prometheus Collector 在**抓取时**查库。
//
// 为什么是 Collector 而不是后台轮询 + Gauge.Set():
// 轮询失败时 Gauge 里会**留着上一次的成功值**,数据库挂了、查询一直失败,
// 而仪表盘上显示的还是昨天那个健康数字 —— 恰好在最需要告警时给出虚假的正常。
// Collector 在抓取时查,查不到就不产出该 series,故障立刻可见。
//
// 核心契约:**查询失败时不上报任何队列 gauge,只上报 scrape_failed=1。**
// 上报 0 会被读成"队列是空的";缺失才能让告警规则的 absent() 生效,
// 把"监控本身坏了"表达为一个独立于"队列健康"的状态。这两者必须能区分,
// 否则 P4 想解决的静默失败会换个形式回来。

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// QueueDepth 一个队列维度的存量。
//
// 这里重新声明而不复用 store 的类型:store 已经依赖 telemetry 的指标接口,
// telemetry 再反向 import store 会构成循环依赖。QueueStatsSource 用结构化接口
// 接受 *store.Store,不需要 import。
type QueueDepth struct {
	Topic string
	Count int64
}

// QueueStats 队列积压快照(字段与 store.QueueStats 对应)。
type QueueStats struct {
	OutboxPending          []QueueDepth
	OutboxOldestPendingAge time.Duration
	OutboxDead             int64
	DeadLetters            []QueueDepth
}

// QueueStatsSource 提供队列积压快照。*store.Store 结构化满足它。
type QueueStatsSource interface {
	QueueStats(ctx context.Context) (QueueStats, error)
}

// QueueCollector 在每次 /metrics 抓取时查询队列积压。
type QueueCollector struct {
	src     QueueStatsSource
	log     *slog.Logger
	timeout time.Duration

	pending      *prometheus.Desc
	oldestAge    *prometheus.Desc
	dead         *prometheus.Desc
	deadLetters  *prometheus.Desc
	scrapeFailed *prometheus.Desc
}

// NewQueueCollector 构造 Collector。timeout <= 0 时取 5s。
func NewQueueCollector(src QueueStatsSource, log *slog.Logger, timeout time.Duration) *QueueCollector {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &QueueCollector{
		src: src, log: log, timeout: timeout,
		pending: prometheus.NewDesc("aiops_outbox_pending",
			"Outbox records awaiting delivery (pending + retryable failed)", []string{"topic"}, nil),
		oldestAge: prometheus.NewDesc("aiops_outbox_oldest_pending_age_seconds",
			"Age of the oldest undelivered outbox record; the primary signal for a stuck relay", nil, nil),
		dead: prometheus.NewDesc("aiops_outbox_dead",
			"Outbox records abandoned after exhausting retries", nil, nil),
		deadLetters: prometheus.NewDesc("aiops_dead_letters_pending",
			"Dead-letter records awaiting manual handling", []string{"topic"}, nil),
		scrapeFailed: prometheus.NewDesc("aiops_queue_scrape_failed",
			"1 when the last queue-depth scrape failed; queue gauges are absent in that case", nil, nil),
	}
}

// Describe 实现 prometheus.Collector。
func (c *QueueCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.pending
	ch <- c.oldestAge
	ch <- c.dead
	ch <- c.deadLetters
	ch <- c.scrapeFailed
}

// Collect 实现 prometheus.Collector。
//
// 查询失败时**只**产出 scrape_failed=1,不产出任何队列 gauge(理由见文件头)。
func (c *QueueCollector) Collect(ch chan<- prometheus.Metric) {
	if c.src == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	st, err := c.src.QueueStats(ctx)
	if err != nil {
		if c.log != nil {
			c.log.Warn("queue depth scrape failed; queue gauges omitted (not reported as 0)", "err", err)
		}
		ch <- prometheus.MustNewConstMetric(c.scrapeFailed, prometheus.GaugeValue, 1)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.scrapeFailed, prometheus.GaugeValue, 0)

	for _, d := range st.OutboxPending {
		ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, float64(d.Count), d.Topic)
	}
	ch <- prometheus.MustNewConstMetric(c.oldestAge, prometheus.GaugeValue, st.OutboxOldestPendingAge.Seconds())
	ch <- prometheus.MustNewConstMetric(c.dead, prometheus.GaugeValue, float64(st.OutboxDead))
	for _, d := range st.DeadLetters {
		ch <- prometheus.MustNewConstMetric(c.deadLetters, prometheus.GaugeValue, float64(d.Count), d.Topic)
	}
}

// RegisterQueue 把队列 Collector 注册到指标注册表。src 为 nil 时空操作。
func (m *Metrics) RegisterQueue(src QueueStatsSource, log *slog.Logger) {
	if m == nil || src == nil {
		return
	}
	m.reg.MustRegister(NewQueueCollector(src, log, 5*time.Second))
}
