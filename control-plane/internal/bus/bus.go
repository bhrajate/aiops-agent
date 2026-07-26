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

// RunWithOptions 支持重试上限 + 死信队列(SECURITY §7)。
func (c *Consumer) RunWithOptions(ctx context.Context, h Handler, opts RunOptions) error {
	attempts := make(map[string]int) // offset 维度的重试计数
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
		offKey := offsetKey(m.Partition, m.Offset)
		if herr := h(ctx, m.Key, m.Value); herr != nil {
			attempts[offKey]++
			if opts.MaxAttempts > 0 && attempts[offKey] >= opts.MaxAttempts {
				// 超限:投 DLQ,然后提交跳过,避免毒消息卡住分区
				if opts.OnDeadLetter != nil {
					if dlErr := opts.OnDeadLetter(ctx, opts.Topic, string(m.Key), m.Value, herr, attempts[offKey]); dlErr != nil {
						// DLQ 也失败:继续重试,不提交
						time.Sleep(500 * time.Millisecond)
						continue
					}
				}
				delete(attempts, offKey)
				_ = c.reader.CommitMessages(ctx, m)
				continue
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		delete(attempts, offKey)
		if err := c.reader.CommitMessages(ctx, m); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
		}
	}
}

func offsetKey(partition int, offset int64) string {
	return fmt.Sprintf("%d:%d", partition, offset)
}

func (c *Consumer) Close() error { return c.reader.Close() }
