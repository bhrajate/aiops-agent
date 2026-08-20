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
			// BatchTimeout 是 kafka-go 等待"批次填满"的时长。
			//
			// 这里**每次 Publish 只发一条消息**(见下方 Publish),所以那个批次
			// 永远不会被填满 —— BatchTimeout 因此是纯粹的附加延迟,每条消息都要
			// 白等一个完整周期。
			//
			// 原值 50ms 让 outbox relay 的排空速率封顶在 ~20/s。实测(600 条
			// pending,无 HTTP 竞争):50ms → 10 条/s;5ms → 56 条/s。
			// 而 ingress 默认限流是 50/s/副本 —— 也就是说改之前**投递跟不上接收**,
			// 持续告警风暴下 outbox 会无界增长。
			//
			// 那正是 store/queuestats.go 警告的那类静默失败:/v1/signals 照样返
			// 202、signals 计数照涨,而 incidents 不再增长,所有既有信号都指示健康。
			//
			// 降到 5ms 不影响持久性(RequireAll 不变),只是让每次写更早发出。
			// 进一步的优化是在调用侧批量发(一次 WriteMessages 传 N 条),
			// 但那会改变逐条的错误归属 —— DrainOutbox 依赖它做 per-row 重试与
			// dead 标记,所以没在这轮动。
			BatchTimeout: 5 * time.Millisecond,
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

		// 就地有界重试同一条消息。绝不在当前消息"未处理"时 fetch 下一条——
		// kafka-go 累积提交会把 offset 推过未处理消息,导致静默丢失(违反 at-least-once)。
		attempt := 0
		aborted := false
		for {
			if herr := h(ctx, m.Key, m.Value); herr == nil {
				break // 处理成功
			} else {
				attempt++
				if opts.MaxAttempts > 0 && attempt >= opts.MaxAttempts {
					// 超上限:投 DLQ。DLQ 落库失败时**原地无限重试落库**(带退避),
					// 直到成功或 ctx 取消——不 fetch 下一条,避免累积提交跳过本消息。
					if opts.OnDeadLetter != nil {
						dlAttempt := 0
						for {
							if dlErr := opts.OnDeadLetter(ctx, opts.Topic, string(m.Key), m.Value, herr, attempt); dlErr == nil {
								break
							}
							dlAttempt++
							select {
							case <-ctx.Done():
								return nil
							case <-time.After(backoffFor(dlAttempt, backoff, maxBackoff)):
							}
						}
					}
					break // 已 DLQ(或无 DLQ 回调),可提交跳过
				}
				// 退避后重试同一条(ctx 可中断)
				select {
				case <-ctx.Done():
					aborted = true
				case <-time.After(backoffFor(attempt, backoff, maxBackoff)):
				}
				if aborted {
					return nil
				}
			}
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
