package telemetry

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type fakeSource struct {
	st    QueueStats
	err   error
	calls int
}

func (f *fakeSource) QueueStats(context.Context) (QueueStats, error) {
	f.calls++
	if f.err != nil {
		// 与 store.QueueStats 的契约一致:出错时第一个返回值是零值。
		// 正是这个零值不能被上报成 0。
		return QueueStats{}, f.err
	}
	return f.st, nil
}

// gather 采集 collector 并渲染为可断言的文本(每行 "name{label=value} value")。
func gather(t *testing.T, c prometheus.Collector) string {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("注册 collector: %v", err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var lines []string
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			var sb strings.Builder
			sb.WriteString(mf.GetName())
			for _, l := range m.GetLabel() {
				sb.WriteString("{" + l.GetName() + "=" + l.GetValue() + "}")
			}
			if g := m.GetGauge(); g != nil {
				sb.WriteString(" " + strconv.FormatFloat(g.GetValue(), 'f', -1, 64))
			}
			lines = append(lines, sb.String())
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// TestQueueCollector_QueryFailureOmitsGauges 是 P4 的核心契约。
//
// 查询失败时**不能**上报队列 gauge 为 0:0 会被读成"队列是空的",恰好在
// 最需要告警时给出虚假的正常。必须让 series 缺失,好让告警规则的 absent()
// 把"监控本身坏了"表达为独立状态。
func TestQueueCollector_QueryFailureOmitsGauges(t *testing.T) {
	c := NewQueueCollector(&fakeSource{err: errors.New("connection refused")}, nil, time.Second)
	out := gather(t, c)

	if !strings.Contains(out, "aiops_queue_scrape_failed 1") {
		t.Errorf("查询失败应上报 scrape_failed=1, got:\n%s", out)
	}
	// 这四个 series 一个都不能出现。
	for _, name := range []string{
		"aiops_outbox_pending",
		"aiops_outbox_oldest_pending_age_seconds",
		"aiops_outbox_dead",
		"aiops_dead_letters_pending",
	} {
		if strings.Contains(out, name) {
			t.Errorf("查询失败时 %s 必须缺失而非上报 0(0 会被读成队列是空的), got:\n%s", name, out)
		}
	}
}

// TestQueueCollector_ReportsDepths 正常路径。
func TestQueueCollector_ReportsDepths(t *testing.T) {
	c := NewQueueCollector(&fakeSource{st: QueueStats{
		OutboxPending:          []QueueDepth{{Topic: "signals", Count: 3}, {Topic: "incidents", Count: 7}},
		OutboxOldestPendingAge: 900 * time.Second,
		OutboxDead:             2,
		DeadLetters:            []QueueDepth{{Topic: "signals", Count: 5}},
	}}, nil, time.Second)
	out := gather(t, c)

	for _, want := range []string{
		"aiops_queue_scrape_failed 0",
		"aiops_outbox_pending{topic=signals} 3",
		"aiops_outbox_pending{topic=incidents} 7",
		"aiops_outbox_oldest_pending_age_seconds 900",
		"aiops_outbox_dead 2",
		"aiops_dead_letters_pending{topic=signals} 5",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少 %q, got:\n%s", want, out)
		}
	}
}

// TestQueueCollector_EmptyQueueReportsZero 空队列**要**上报 0。
//
// 与查询失败必须区分:空队列是已知事实(查成功了,结果是 0),
// 查询失败是未知(不知道队列多深)。前者上报 0,后者缺失。
func TestQueueCollector_EmptyQueueReportsZero(t *testing.T) {
	c := NewQueueCollector(&fakeSource{st: QueueStats{}}, nil, time.Second)
	out := gather(t, c)
	if !strings.Contains(out, "aiops_outbox_oldest_pending_age_seconds 0") {
		t.Errorf("空队列应上报 0(它是已知事实,不同于查询失败), got:\n%s", out)
	}
	if !strings.Contains(out, "aiops_queue_scrape_failed 0") {
		t.Errorf("空队列不是抓取失败, got:\n%s", out)
	}
}

// TestQueueCollector_QueriesOnEveryScrape 确认是抓取时查库,不是缓存值。
//
// 后台轮询 + Gauge.Set() 的问题是失败时 Gauge 里留着上一次的成功值:
// 数据库挂了,仪表盘还显示昨天那个健康数字。
func TestQueueCollector_QueriesOnEveryScrape(t *testing.T) {
	src := &fakeSource{st: QueueStats{OutboxDead: 1}}
	c := NewQueueCollector(src, nil, time.Second)
	gather(t, c)
	gather(t, c)
	if src.calls < 2 {
		t.Errorf("每次抓取都应查库(否则故障时会显示陈旧的健康值), calls=%d", src.calls)
	}
}

// TestQueueCollector_NilSourceSafe 降级路径:未装配数据源时不应 panic。
func TestQueueCollector_NilSourceSafe(t *testing.T) {
	c := NewQueueCollector(nil, nil, time.Second)
	ch := make(chan prometheus.Metric, 8)
	c.Collect(ch)
	close(ch)
	if len(ch) != 0 {
		t.Error("无数据源时不应产出任何指标")
	}
}

// TestRegisterQueue_NilSafe RegisterQueue 对 nil 应为空操作。
func TestRegisterQueue_NilSafe(t *testing.T) {
	m := New()
	m.RegisterQueue(nil, nil) // 不应 panic
	var nilm *Metrics
	nilm.RegisterQueue(&fakeSource{}, nil)
}

// TestRegisterQueue_ExposesSeries 注册后指标出现在 /metrics 上。
func TestRegisterQueue_ExposesSeries(t *testing.T) {
	m := New()
	m.RegisterQueue(&fakeSource{st: QueueStats{OutboxDead: 4}}, nil)
	mfs, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() == "aiops_outbox_dead" {
			found = true
		}
	}
	if !found {
		t.Error("RegisterQueue 后 aiops_outbox_dead 应出现在注册表中")
	}
}
