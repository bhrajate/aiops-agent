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
	store *store.Store
	pub   *bus.Publisher
	log   *slog.Logger
}

func New(s *store.Store, pub *bus.Publisher, log *slog.Logger) *Publisher {
	return &Publisher{store: s, pub: pub, log: log}
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
	rows, err := p.store.FetchPendingOutbox(ctx, 100)
	if err != nil {
		p.log.Warn("fetch outbox failed", "err", err)
		return
	}
	for _, r := range rows {
		if err := p.pub.Publish(ctx, r.Topic, r.Key, r.Payload); err != nil {
			p.log.Warn("publish outbox failed", "id", r.ID, "topic", r.Topic, "err", err)
			_ = p.store.MarkOutboxFailed(ctx, r.ID)
			continue
		}
		if err := p.store.MarkOutboxPublished(ctx, r.ID); err != nil {
			p.log.Warn("mark published failed", "id", r.ID, "err", err)
		}
	}
}
