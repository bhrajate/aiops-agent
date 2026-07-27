// Package retention 实现数据保留清理(Janitor)。
//
// 为什么放在应用侧而不是 SQL 定时任务:数据库不一定装 pg_cron;而分批、限速、
// 指标、审计这些都更适合放在应用里。多副本下靠 PG advisory lock 互斥,
// 保证同一时刻只有一个实例在清理。
//
// 安全约束(见 store/retention.go):只删终态数据,活跃 incident 与未结束调查
// 的上下文永不触碰。
package retention

import (
	"context"
	"log/slog"
	"time"
)

// Store 抽象清理所需的存储操作(便于测试替身)。
type Store interface {
	TryRetentionLock(ctx context.Context) (func(), bool, error)
	PurgeOlderThan(ctx context.Context, table, timeCol string, days, batch int) (int64, error)
	PurgePublishedOutbox(ctx context.Context, days, batch int) (int64, error)
	PurgeOrphanSignals(ctx context.Context, days, batch int) (int64, error)
	PurgeClosedCases(ctx context.Context, days, batch int) (int64, int64, error)
}

// Metrics 抽象清理指标(nil-safe)。
type Metrics interface {
	ObserveRetentionPurge(target string, rows int)
}

// Config 保留期配置(天;<=0 表示不清理该项)。
type Config struct {
	SignalDays      int
	EventDays       int
	AuditDays       int
	OutboxDays      int
	DeadLetterDays  int
	IdempotencyDays int
	CaseDays        int

	IntervalSec int
	BatchSize   int
}

// maxBatchesPerTarget 单轮每个目标最多删几批。
// 目的是让一轮清理的时长可预期:即使积压很久,也不会有一轮跑几十分钟、
// 长时间占着 advisory lock 并持续给数据库压力。剩余部分下一轮继续。
const maxBatchesPerTarget = 20

type Janitor struct {
	store   Store
	cfg     Config
	metrics Metrics
	log     *slog.Logger
}

func New(s Store, cfg Config, metrics Metrics, log *slog.Logger) *Janitor {
	if cfg.IntervalSec <= 0 {
		cfg.IntervalSec = 3600
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 5000
	}
	return &Janitor{store: s, cfg: cfg, metrics: metrics, log: log}
}

// Run 循环清理直到 ctx 取消。首轮延迟一个间隔,避免启动风暴期叠加清理压力。
func (j *Janitor) Run(ctx context.Context) {
	t := time.NewTicker(time.Duration(j.cfg.IntervalSec) * time.Second)
	defer t.Stop()
	j.log.Info("retention janitor started",
		"interval_sec", j.cfg.IntervalSec, "batch", j.cfg.BatchSize,
		"signal_days", j.cfg.SignalDays, "event_days", j.cfg.EventDays,
		"audit_days", j.cfg.AuditDays, "case_days", j.cfg.CaseDays)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := j.RunOnce(ctx); err != nil {
				j.log.Warn("retention sweep failed", "err", err)
			}
		}
	}
}

// RunOnce 执行一轮清理。未拿到 advisory lock 时直接返回(别的副本在跑)。
func (j *Janitor) RunOnce(ctx context.Context) error {
	release, ok, err := j.store.TryRetentionLock(ctx)
	if err != nil {
		return err
	}
	if !ok {
		j.log.Debug("retention skipped: another replica holds the lock")
		return nil
	}
	defer release()

	start := time.Now()
	total := 0

	// 运营数据:按时间清理。
	type target struct {
		name  string
		days  int
		purge func(context.Context, int, int) (int64, error)
	}
	targets := []target{
		{"investigation_events", j.cfg.EventDays, func(c context.Context, d, b int) (int64, error) {
			return j.store.PurgeOlderThan(c, "investigation_events", "created_at", d, b)
		}},
		{"audit_log", j.cfg.AuditDays, func(c context.Context, d, b int) (int64, error) {
			return j.store.PurgeOlderThan(c, "audit_log", "created_at", d, b)
		}},
		{"dead_letters", j.cfg.DeadLetterDays, func(c context.Context, d, b int) (int64, error) {
			return j.store.PurgeOlderThan(c, "dead_letters", "created_at", d, b)
		}},
		{"idempotency_keys", j.cfg.IdempotencyDays, func(c context.Context, d, b int) (int64, error) {
			return j.store.PurgeOlderThan(c, "idempotency_keys", "created_at", d, b)
		}},
		{"outbox", j.cfg.OutboxDays, j.store.PurgePublishedOutbox},
		{"signals_orphan", j.cfg.SignalDays, j.store.PurgeOrphanSignals},
	}

	for _, tg := range targets {
		if tg.days <= 0 {
			continue
		}
		n, err := j.drain(ctx, tg.name, tg.days, tg.purge)
		total += n
		if err != nil {
			return err // 本轮中断,下一轮继续(清理是幂等可重入的)
		}
	}

	// 案例数据:整案清理(终态且过期)。
	if j.cfg.CaseDays > 0 {
		cases, rows := 0, 0
		for i := 0; i < maxBatchesPerTarget; i++ {
			if ctx.Err() != nil {
				break
			}
			c, r, err := j.store.PurgeClosedCases(ctx, j.cfg.CaseDays, j.cfg.BatchSize)
			if err != nil {
				return err
			}
			cases += int(c)
			rows += int(r)
			if c == 0 {
				break
			}
		}
		if cases > 0 {
			j.observe("closed_cases", cases)
			total += rows
			j.log.Info("retention purged closed cases", "incidents", cases, "rows", rows)
		}
	}

	if total > 0 {
		j.log.Info("retention sweep done", "rows", total, "elapsed_ms", time.Since(start).Milliseconds())
	}
	return nil
}

// drain 对一个目标反复删除,直到删空、达到批次上限或 ctx 取消。
func (j *Janitor) drain(
	ctx context.Context, name string, days int,
	purge func(context.Context, int, int) (int64, error),
) (int, error) {
	total := 0
	for i := 0; i < maxBatchesPerTarget; i++ {
		if ctx.Err() != nil {
			return total, nil
		}
		n, err := purge(ctx, days, j.cfg.BatchSize)
		if err != nil {
			return total, err
		}
		total += int(n)
		if int(n) < j.cfg.BatchSize {
			break // 已删空
		}
	}
	if total > 0 {
		j.observe(name, total)
		j.log.Info("retention purged", "target", name, "rows", total, "retain_days", days)
	}
	return total, nil
}

func (j *Janitor) observe(target string, rows int) {
	if j.metrics != nil {
		j.metrics.ObserveRetentionPurge(target, rows)
	}
}
