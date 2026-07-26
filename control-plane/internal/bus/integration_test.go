package bus

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

// 集成测试:需运行中的 Redpanda(AIOPS_KAFKA_BROKERS)。
// 验证 at-least-once 语义与 DLQ:
//   - handler 前几次失败、后成功 → 消息最终被成功处理(就地重试,不跳过)
//   - handler 恒失败 → 超 MaxAttempts 后进入 DLQ 回调
//
// 无 broker 时自动跳过。
func brokers(t *testing.T) []string {
	b := os.Getenv("AIOPS_KAFKA_BROKERS")
	if b == "" {
		t.Skip("AIOPS_KAFKA_BROKERS 未设置,跳过 Kafka 集成测试")
	}
	return []string{b}
}

func uniqueTopic(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func produce(t *testing.T, bs []string, topic string, msgs int) {
	w := &kafka.Writer{Addr: kafka.TCP(bs...), Topic: topic, Balancer: &kafka.LeastBytes{}, AllowAutoTopicCreation: true}
	defer w.Close()
	ms := make([]kafka.Message, msgs)
	for i := 0; i < msgs; i++ {
		ms[i] = kafka.Message{Key: []byte(fmt.Sprintf("k%d", i)), Value: []byte(fmt.Sprintf("v%d", i))}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.WriteMessages(ctx, ms...); err != nil {
		t.Fatalf("produce: %v", err)
	}
}

func TestIntegration_RetryThenSucceed_NoSkip(t *testing.T) {
	bs := brokers(t)
	topic := uniqueTopic("test-retry")
	produce(t, bs, topic, 1)

	c := NewConsumer(bs, topic, "test-retry-grp")
	defer c.Close()

	var calls int32
	handler := func(ctx context.Context, key, value []byte) error {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 { // 前两次失败,第三次成功
			return fmt.Errorf("transient failure %d", n)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go c.RunWithOptions(ctx, handler, RunOptions{Topic: topic, MaxAttempts: 5})

	// 等待处理成功(就地重试到第 3 次)
	deadline := time.After(25 * time.Second)
	for atomic.LoadInt32(&calls) < 3 {
		select {
		case <-deadline:
			t.Fatalf("消息未被就地重试到成功,calls=%d(说明失败消息被跳过了)", atomic.LoadInt32(&calls))
		case <-time.After(200 * time.Millisecond):
		}
	}
	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Fatalf("期望至少重试到 3 次,got %d", got)
	}
}

func TestIntegration_ExhaustToDeadLetter(t *testing.T) {
	bs := brokers(t)
	topic := uniqueTopic("test-dlq")
	produce(t, bs, topic, 1)

	c := NewConsumer(bs, topic, "test-dlq-grp")
	defer c.Close()

	var dead int32
	handler := func(ctx context.Context, key, value []byte) error {
		return fmt.Errorf("always fail") // 恒失败
	}
	dlq := func(ctx context.Context, tp, key string, v []byte, lastErr error, attempts int) error {
		atomic.AddInt32(&dead, 1)
		return nil // DLQ 成功
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go c.RunWithOptions(ctx, handler, RunOptions{Topic: topic, MaxAttempts: 3, OnDeadLetter: dlq})

	deadline := time.After(25 * time.Second)
	for atomic.LoadInt32(&dead) < 1 {
		select {
		case <-deadline:
			t.Fatal("恒失败消息未进入 DLQ(说明 DLQ 从不触发)")
		case <-time.After(200 * time.Millisecond):
		}
	}
}
