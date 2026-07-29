package retention

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type call struct {
	target string
	days   int
	batch  int
}

type fakeStore struct {
	locked      bool // false => 别的副本持有锁
	lockErr     error
	calls       []call
	remaining   map[string]int // target -> 还剩多少行可删
	casesLeft   int
	purgeErr    error
	released    int
	lockAttempt int
}

func newFake() *fakeStore {
	return &fakeStore{locked: true, remaining: map[string]int{}}
}

func (f *fakeStore) TryRetentionLock(context.Context) (func(), bool, error) {
	f.lockAttempt++
	if f.lockErr != nil {
		return nil, false, f.lockErr
	}
	if !f.locked {
		return nil, false, nil
	}
	return func() { f.released++ }, true, nil
}

func (f *fakeStore) take(target string, batch int) int64 {
	left, ok := f.remaining[target]
	if !ok {
		return 0
	}
	n := batch
	if left < n {
		n = left
	}
	f.remaining[target] = left - n
	return int64(n)
}

func (f *fakeStore) PurgeOlderThan(_ context.Context, table, _ string, days, batch int) (int64, error) {
	f.calls = append(f.calls, call{table, days, batch})
	if f.purgeErr != nil {
		return 0, f.purgeErr
	}
	return f.take(table, batch), nil
}

func (f *fakeStore) PurgePublishedOutbox(_ context.Context, days, batch int) (int64, error) {
	f.calls = append(f.calls, call{"outbox", days, batch})
	return f.take("outbox", batch), nil
}

func (f *fakeStore) PurgeOrphanSignals(_ context.Context, days, batch int) (int64, error) {
	f.calls = append(f.calls, call{"signals_orphan", days, batch})
	return f.take("signals_orphan", batch), nil
}

func (f *fakeStore) PurgeStaleTopology(_ context.Context, days, batch int) (int64, error) {
	f.calls = append(f.calls, call{"service_topology", days, batch})
	return f.take("service_topology", batch), nil
}

func (f *fakeStore) PurgeClosedCases(_ context.Context, days, batch int) (int64, int64, error) {
	f.calls = append(f.calls, call{"closed_cases", days, batch})
	if f.casesLeft <= 0 {
		return 0, 0, nil
	}
	n := batch
	if f.casesLeft < n {
		n = f.casesLeft
	}
	f.casesLeft -= n
	return int64(n), int64(n * 4), nil
}

type fakeMetrics struct{ observed map[string]int }

func (m *fakeMetrics) ObserveRetentionPurge(target string, rows int) {
	if m.observed == nil {
		m.observed = map[string]int{}
	}
	m.observed[target] += rows
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func fullConfig() Config {
	return Config{
		SignalDays: 30, EventDays: 90, AuditDays: 365, OutboxDays: 7,
		DeadLetterDays: 30, IdempotencyDays: 7, CaseDays: 180,
		IntervalSec: 3600, BatchSize: 100,
	}
}

func targetsCalled(f *fakeStore) map[string]int {
	out := map[string]int{}
	for _, c := range f.calls {
		out[c.target]++
	}
	return out
}

// 未拿到锁时必须完全不动手(多副本互斥)。
func TestRunOnceSkipsWithoutLock(t *testing.T) {
	f := newFake()
	f.locked = false
	j := New(f, fullConfig(), nil, quietLog())
	if err := j.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("未持锁却执行了清理: %v", f.calls)
	}
	if f.released != 0 {
		t.Error("未获取锁却调用了 release")
	}
}

// 拿到锁必须释放,即使中途出错。
func TestLockIsAlwaysReleased(t *testing.T) {
	f := newFake()
	f.purgeErr = errors.New("boom")
	j := New(f, fullConfig(), nil, quietLog())
	if err := j.RunOnce(context.Background()); err == nil {
		t.Fatal("期望返回错误")
	}
	if f.released != 1 {
		t.Errorf("release 调用 %d 次,应为 1", f.released)
	}
}

// days<=0 的目标必须完全跳过(表示"不清理该表")。
func TestZeroDaysDisablesTarget(t *testing.T) {
	f := newFake()
	cfg := fullConfig()
	cfg.AuditDays = 0 // 合规要求永久保留审计
	cfg.CaseDays = 0
	j := New(f, cfg, nil, quietLog())
	if err := j.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	called := targetsCalled(f)
	if called["audit_log"] != 0 {
		t.Error("AuditDays=0 时仍清理了 audit_log")
	}
	if called["closed_cases"] != 0 {
		t.Error("CaseDays=0 时仍清理了案例")
	}
	if called["investigation_events"] == 0 {
		t.Error("其他目标应照常清理")
	}
}

// 每个目标都要用自己的保留天数,不能串。
func TestEachTargetUsesItsOwnRetentionDays(t *testing.T) {
	f := newFake()
	j := New(f, fullConfig(), nil, quietLog())
	if err := j.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"investigation_events": 90,
		"audit_log":            365,
		"dead_letters":         30,
		"idempotency_keys":     7,
		"outbox":               7,
		"signals_orphan":       30,
		"closed_cases":         180,
	}
	seen := map[string]int{}
	for _, c := range f.calls {
		seen[c.target] = c.days
	}
	for target, days := range want {
		if seen[target] != days {
			t.Errorf("%s 用了 %d 天,应为 %d", target, seen[target], days)
		}
	}
}

// 积压时分批删除,直到删空。
func TestDrainsUntilEmpty(t *testing.T) {
	f := newFake()
	f.remaining["audit_log"] = 250 // batch=100 -> 需要 3 批
	cfg := fullConfig()
	m := &fakeMetrics{}
	j := New(f, cfg, m, quietLog())
	if err := j.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := targetsCalled(f)["audit_log"]; got != 3 {
		t.Errorf("audit_log 调用 %d 批,应为 3", got)
	}
	if m.observed["audit_log"] != 250 {
		t.Errorf("指标记录 %d 行,应为 250", m.observed["audit_log"])
	}
}

// 单轮批次有上限:积压再多也不会一轮跑到底、长时间占锁。
func TestBatchesPerRoundAreBounded(t *testing.T) {
	f := newFake()
	f.remaining["audit_log"] = 100 * (maxBatchesPerTarget + 50)
	j := New(f, fullConfig(), nil, quietLog())
	if err := j.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := targetsCalled(f)["audit_log"]; got != maxBatchesPerTarget {
		t.Errorf("单轮跑了 %d 批,上限应为 %d", got, maxBatchesPerTarget)
	}
	if f.remaining["audit_log"] == 0 {
		t.Error("应留有剩余给下一轮")
	}
}

// 案例清理同样分批,且删空即停(不空转)。
func TestClosedCasesDrainAndStop(t *testing.T) {
	f := newFake()
	f.casesLeft = 150
	m := &fakeMetrics{}
	j := New(f, fullConfig(), m, quietLog())
	if err := j.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 100 + 50 + 一次返回 0 的探测 = 3 次
	if got := targetsCalled(f)["closed_cases"]; got != 3 {
		t.Errorf("closed_cases 调用 %d 次,应为 3", got)
	}
	if m.observed["closed_cases"] != 150 {
		t.Errorf("案例指标 %d,应为 150", m.observed["closed_cases"])
	}
}

// 无事可做时不产生指标噪声。
func TestNoopSweepEmitsNoMetrics(t *testing.T) {
	f := newFake()
	m := &fakeMetrics{}
	j := New(f, fullConfig(), m, quietLog())
	if err := j.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(m.observed) != 0 {
		t.Errorf("空轮次不应上报指标: %v", m.observed)
	}
}

// 缺省值兜底:间隔/批量为 0 时用安全默认,避免忙循环或 LIMIT 0 空转。
func TestDefaultsApplied(t *testing.T) {
	j := New(newFake(), Config{}, nil, quietLog())
	if j.cfg.IntervalSec <= 0 || j.cfg.BatchSize <= 0 {
		t.Fatalf("缺省值未生效: interval=%d batch=%d", j.cfg.IntervalSec, j.cfg.BatchSize)
	}
}

// ctx 取消后应尽快停止,不再继续删。
func TestRespectsCancelledContext(t *testing.T) {
	f := newFake()
	f.remaining["audit_log"] = 100 * maxBatchesPerTarget
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j := New(f, fullConfig(), nil, quietLog())
	if err := j.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := targetsCalled(f)["audit_log"]; got > 1 {
		t.Errorf("ctx 已取消仍跑了 %d 批", got)
	}
}

// nil metrics 必须安全(降级路径)。
func TestNilMetricsSafe(t *testing.T) {
	f := newFake()
	f.remaining["audit_log"] = 10
	j := New(f, fullConfig(), nil, quietLog())
	if err := j.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}
