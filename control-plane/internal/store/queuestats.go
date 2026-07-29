package store

// 队列积压快照(P4)——供 Prometheus Collector 在抓取时查询。
//
// 为什么需要:此前导出的指标**全是计数器**,没有任何"当前存量"。于是 outbox
// relay 卡住时:`/v1/signals` 照样返回 202、signals 计数照涨,但 incidents 不再
// 增长,前端看着一切正常;`aiops_dead_letters_total` 也不动——它只在彻底放弃
// 投递时才 +1,而"卡住"恰恰是还没放弃。这是最危险的一类静默失败:
// 所有既有信号都指示健康,唯一的异常是"某些东西不再发生"。

import (
	"context"
	"time"
)

// QueueDepth 是一个队列维度的存量。
type QueueDepth struct {
	Topic string
	Count int64
}

// QueueStats 队列积压快照。
type QueueStats struct {
	// OutboxPending 按 topic 的待投递条数(pending + 可重试的 failed)。
	OutboxPending []QueueDepth
	// OutboxOldestPendingAge 最老待投递记录的年龄。
	//
	// **这是判断"卡住"的主指标,不是条数。** 告警风暴下积压几千条但几秒排空是
	// 正常的;只积压 3 条却卡了 20 分钟才是故障。按条数告警会在风暴时误报、
	// 在真卡住时漏报。
	OutboxOldestPendingAge time.Duration
	// OutboxDead 重试耗尽、已放弃投递的存量(status='dead')。
	OutboxDead int64
	// DeadLetters 死信表存量。用存量而非计数器:计数器进程重启即归零,
	// 回答不了"现在还有多少条待人工处理"。
	DeadLetters []QueueDepth
}

// QueueStats 查询队列积压。
//
// **返回 error 时第一个返回值不可用**(零值)。调用方必须据此**不上报**任何
// 队列指标,而不是上报 0 —— 0 会被读成"队列是空的",恰好掩盖了故障。
// 见 telemetry/queuecollector.go。
func (s *Store) QueueStats(ctx context.Context) (QueueStats, error) {
	var st QueueStats

	// 待投递:与 DrainOutbox 的取件条件保持一致(pending 或 attempts 未耗尽的
	// failed),否则指标会与实际会被投递的集合不符。
	rows, err := s.pool.Query(ctx,
		`SELECT topic, count(*) FROM outbox
		  WHERE status = 'pending' OR status = 'failed'
		  GROUP BY topic`)
	if err != nil {
		return QueueStats{}, err
	}
	for rows.Next() {
		var d QueueDepth
		if err := rows.Scan(&d.Topic, &d.Count); err != nil {
			rows.Close()
			return QueueStats{}, err
		}
		st.OutboxPending = append(st.OutboxPending, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return QueueStats{}, err
	}

	// 最老待投递年龄。EXTRACT(EPOCH ...) 返回 numeric,pgx 不会把它扫进 float64,
	// 故显式转 double precision。COALESCE 让空队列返回 0 而非 NULL。
	var oldestSec float64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(EXTRACT(EPOCH FROM (now() - min(created_at)))::double precision, 0)
		   FROM outbox
		  WHERE status = 'pending' OR status = 'failed'`).Scan(&oldestSec); err != nil {
		return QueueStats{}, err
	}
	st.OutboxOldestPendingAge = time.Duration(oldestSec * float64(time.Second))

	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE status = 'dead'`).Scan(&st.OutboxDead); err != nil {
		return QueueStats{}, err
	}

	drows, err := s.pool.Query(ctx,
		`SELECT topic, count(*) FROM dead_letters GROUP BY topic`)
	if err != nil {
		return QueueStats{}, err
	}
	for drows.Next() {
		var d QueueDepth
		if err := drows.Scan(&d.Topic, &d.Count); err != nil {
			drows.Close()
			return QueueStats{}, err
		}
		st.DeadLetters = append(st.DeadLetters, d)
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return QueueStats{}, err
	}

	return st, nil
}
