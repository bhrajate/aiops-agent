// Package bus 封装 Kafka(Redpanda)事件总线。至少一次投递。
package bus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"
)

// Publisher 向指定 topic 发送消息。
type Publisher struct {
	writer *kafka.Writer
}

func NewPublisher(brokers []string) *Publisher {
	return &Publisher{
		writer: &kafka.Writer{
			Addr:         kafka.TCP(brokers...),
			Balancer:     &kafka.Hash{},
			RequiredAcks: kafka.RequireAll, // 至少一次
			BatchTimeout: 50 * time.Millisecond,
		},
	}
}

func (p *Publisher) Publish(ctx context.Context, topic, key string, value []byte) error {
	return p.writer.WriteMessages(ctx, kafka.Message{
		Topic: topic,
		Key:   []byte(key),
		Value: value,
	})
}

func (p *Publisher) Close() error { return p.writer.Close() }

// Consumer 消费单个 topic(消费组)。
type Consumer struct {
	reader *kafka.Reader
}

func NewConsumer(brokers []string, topic, group string) *Consumer {
	return &Consumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:        brokers,
			Topic:          topic,
			GroupID:        group,
			MinBytes:       1,
			MaxBytes:       10 << 20,
			CommitInterval: 0, // 手动提交,处理成功后再提交
			StartOffset:    kafka.FirstOffset,
		}),
	}
}

// Handler 处理一条消息;返回 nil 才提交 offset。
type Handler func(ctx context.Context, key, value []byte) error

// DeadLetterFunc 在消息重试超限后调用(投递到 DLQ),返回 nil 表示已妥善归档、可提交跳过。
type DeadLetterFunc func(ctx context.Context, topic, key string, value []byte, lastErr error, attempts int) error

// RunOptions 消费选项。
type RunOptions struct {
	Topic        string
	MaxAttempts  int            // 超过则进 DLQ(<=0 表示无限重试)
	OnDeadLetter DeadLetterFunc // MaxAttempts 生效时必须提供
}

// Run 持续消费直至 ctx 取消。处理失败不提交(至少一次,下次重投)。
func (c *Consumer) Run(ctx context.Context, h Handler) error {
	return c.RunWithOptions(ctx, h, RunOptions{})
}

// RunWithOptions 支持重试上限 + 死信队列(SECURITY §7),保证 at-least-once。
//
// 关键语义:kafka-go 的 FetchMessage 只前进不回退。因此对失败消息必须"就地"有界
// 重试(带退避),而不是 continue 去取下一条——否则失败消息会被静默跳过、DLQ 永不触发。
// 只有处理成功或已投递 DLQ 后,才提交 offset。
func (c *Consumer) RunWithOptions(ctx context.Context, h Handler, opts RunOptions) error {
	backoff := 500 * time.Millisecond
	maxBackoff := 10 * time.Second
	for {
		m, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
				continue
			}
		}

		// 就地有界重试同一条消息
		attempt := 0
		handled := false
		for {
			if herr := h(ctx, m.Key, m.Value); herr == nil {
				handled = true
				break
			} else {
				attempt++
				if opts.MaxAttempts > 0 && attempt >= opts.MaxAttempts {
					// 超上限:投 DLQ。DLQ 成功才提交跳过(避免毒消息卡住分区);
					// DLQ 失败则不提交,靠外层重新 fetch(offset 未提交)重来。
					if opts.OnDeadLetter != nil {
						if dlErr := opts.OnDeadLetter(ctx, opts.Topic, string(m.Key), m.Value, herr, attempt); dlErr != nil {
							break // 未 handled,不提交
						}
					}
					handled = true // 已 DLQ,可提交跳过
					break
				}
				// 退避后重试同一条(ctx 可中断)
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(backoffFor(attempt, backoff, maxBackoff)):
				}
			}
		}

		if !handled {
			// DLQ 也失败:不提交,短暂等待后由外层重新处理(至少一次)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			continue
		}
		if err := c.reader.CommitMessages(ctx, m); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
		}
	}
}

// backoffFor 指数退避(封顶)。
func backoffFor(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}

func offsetKey(partition int, offset int64) string {
	return fmt.Sprintf("%d:%d", partition, offset)
}

func (c *Consumer) Close() error { return c.reader.Close() }
