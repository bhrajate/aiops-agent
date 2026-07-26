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
	// 取 pending 与"失败但未超重试上限"的记录(后者可重投,消除瞬时抖动导致的永久丢事件)。
	rows, err := p.store.FetchPendingOutbox(ctx, 100, p.maxAttempts)
	if err != nil {
		p.log.Warn("fetch outbox failed", "err", err)
		return
	}
	for _, r := range rows {
		if err := p.pub.Publish(ctx, r.Topic, r.Key, r.Payload); err != nil {
			// 递增 attempts;超上限则标记 dead(不再重投),并告警——防止毒记录无限重试。
			attempts, mErr := p.store.MarkOutboxFailed(ctx, r.ID)
			if mErr != nil {
				p.log.Warn("mark failed err", "id", r.ID, "err", mErr)
			}
			if attempts >= p.maxAttempts {
				_ = p.store.MarkOutboxDead(ctx, r.ID)
				p.log.Error("outbox record dead after max attempts", "id", r.ID, "topic", r.Topic, "attempts", attempts, "err", err)
			} else {
				p.log.Warn("publish outbox failed (will retry)", "id", r.ID, "topic", r.Topic, "attempts", attempts, "err", err)
			}
			continue
		}
		if err := p.store.MarkOutboxPublished(ctx, r.ID); err != nil {
			p.log.Warn("mark published failed", "id", r.ID, "err", err)
		}
	}
}
