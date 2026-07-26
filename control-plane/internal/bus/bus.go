// Package bus 封装 Kafka(Redpanda)事件总线。至少一次投递。
package bus

import (
	"context"
	"errors"
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

// Run 持续消费直至 ctx 取消。处理失败不提交(至少一次,下次重投)。
func (c *Consumer) Run(ctx context.Context, h Handler) error {
	for {
		m, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			// 短暂退避后重试
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
				continue
			}
		}
		if err := h(ctx, m.Key, m.Value); err != nil {
			// 处理失败:不提交,退避后由后续 fetch 重投
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err := c.reader.CommitMessages(ctx, m); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
		}
	}
}

func (c *Consumer) Close() error { return c.reader.Close() }
