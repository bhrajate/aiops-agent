// Package outbox 实现 Outbox Pattern 投递循环:
// 轮询业务库 pending 记录 → 发布到 Kafka → 标记 published。
// 保证"业务状态已提交但事件未发布"不会发生。
package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/aiops/control-plane/internal/bus"
	"github.com/aiops/control-plane/internal/store"
)

type Publisher struct {
	store       *store.Store
	pub         *bus.Publisher
	log         *slog.Logger
	maxAttempts int
}

func New(s *store.Store, pub *bus.Publisher, maxAttempts int, log *slog.Logger) *Publisher {
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	return &Publisher{store: s, pub: pub, log: log, maxAttempts: maxAttempts}
}

// Run 每 interval 轮询一次,直至 ctx 取消。
func (p *Publisher) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.drain(ctx)
		}
	}
}

func (p *Publisher) drain(ctx context.Context) {
	// 事务内 取(FOR UPDATE SKIP LOCKED)→ 发布 → 标记:多副本并发时同一行只由一个副本处理,
	// 消除跨副本重复投递(A1);失败重试、超限进 dead 均在同事务内完成。
	logf := func(msg string, kv ...any) { p.log.Warn(msg, kv...) }
	if _, err := p.store.DrainOutbox(ctx, 100, p.maxAttempts, p.pub.Publish, logf); err != nil {
		p.log.Warn("drain outbox failed", "err", err)
	}
}
