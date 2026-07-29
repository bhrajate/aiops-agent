package main

// queueStatsAdapter 把 store.QueueStats 适配为 telemetry.QueueStatsSource。
//
// 为什么需要适配而不是让两边共用一个类型:store 已经依赖 telemetry 的指标接口
// (Metrics 被注入到 store 的调用方),telemetry 再反向 import store 会构成循环
// 依赖。两边各自声明结构相同的类型、在装配层(main)做一次转换,是代价最小的解法
// —— 转换逻辑只有这一处,且编译期就能发现字段漂移。

import (
	"context"

	"github.com/aiops/control-plane/internal/store"
	"github.com/aiops/control-plane/internal/telemetry"
)

type queueStatsAdapter struct{ st *store.Store }

func (a queueStatsAdapter) QueueStats(ctx context.Context) (telemetry.QueueStats, error) {
	s, err := a.st.QueueStats(ctx)
	if err != nil {
		// 保持 store 的契约:出错时返回零值,由 Collector 决定"不上报"而非上报 0。
		return telemetry.QueueStats{}, err
	}
	out := telemetry.QueueStats{
		OutboxOldestPendingAge: s.OutboxOldestPendingAge,
		OutboxDead:             s.OutboxDead,
	}
	for _, d := range s.OutboxPending {
		out.OutboxPending = append(out.OutboxPending, telemetry.QueueDepth{Topic: d.Topic, Count: d.Count})
	}
	for _, d := range s.DeadLetters {
		out.DeadLetters = append(out.DeadLetters, telemetry.QueueDepth{Topic: d.Topic, Count: d.Count})
	}
	return out, nil
}
