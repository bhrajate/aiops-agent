package store

// 对真实 PostgreSQL 验证 QueueStats 的 SQL 与扫描类型。
// 用 DB 前缀,与既有 DB 测试一致(CI 的 db-tests job 跑 -run 'DB|Integration')。

import (
	"context"
	"testing"
	"time"
)

func TestDBQueueStats(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()

	if _, err := st.pool.Exec(ctx,
		`DELETE FROM outbox WHERE topic LIKE 'qs-%';
		 DELETE FROM dead_letters WHERE topic LIKE 'qs-%'`); err != nil {
		t.Fatalf("清理: %v", err)
	}

	t.Run("空队列返回零值而非报错", func(t *testing.T) {
		got, err := st.QueueStats(ctx)
		if err != nil {
			t.Fatalf("空队列不应报错: %v", err)
		}
		// min(created_at) 在空集上是 NULL,若无 COALESCE 会扫描失败。
		if got.OutboxOldestPendingAge < 0 {
			t.Errorf("空队列年龄应为 0,got %v", got.OutboxOldestPendingAge)
		}
	})

	// 造数据:2 条 pending、1 条 failed(应计入待投递)、1 条 published(不计)、
	// 1 条 dead(单独计)。
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO outbox (topic, key, payload, status, created_at) VALUES
		   ('qs-a','k1','{}','pending',   now() - interval '15 minutes'),
		   ('qs-a','k2','{}','pending',   now()),
		   ('qs-b','k3','{}','failed',    now() - interval '2 minutes'),
		   ('qs-a','k4','{}','published', now() - interval '1 hour'),
		   ('qs-b','k5','{}','dead',      now() - interval '1 hour')`); err != nil {
		t.Fatalf("插入 outbox: %v", err)
	}
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO dead_letters (topic, key, payload, error, attempts)
		 VALUES ('qs-a','k9','{}','boom',5), ('qs-a','k10','{}','boom',5)`); err != nil {
		t.Fatalf("插入 dead_letters: %v", err)
	}

	got, err := st.QueueStats(ctx)
	if err != nil {
		t.Fatalf("QueueStats: %v", err)
	}

	pending := map[string]int64{}
	for _, d := range got.OutboxPending {
		pending[d.Topic] = d.Count
	}
	if pending["qs-a"] != 2 {
		t.Errorf("qs-a 待投递应为 2(published 不计),got %d", pending["qs-a"])
	}
	// failed 必须计入:它与 DrainOutbox 的取件条件一致,漏掉会让"卡在重试"
	// 这一最常见的卡住形态在指标上完全不可见。
	if pending["qs-b"] != 1 {
		t.Errorf("qs-b 待投递应为 1(failed 计入),got %d", pending["qs-b"])
	}

	// 最老待投递 15 分钟。这是判断卡住的主指标,必须真的按时间算。
	if got.OutboxOldestPendingAge < 14*time.Minute || got.OutboxOldestPendingAge > 16*time.Minute {
		t.Errorf("最老待投递年龄应约 15 分钟,got %v", got.OutboxOldestPendingAge)
	}

	if got.OutboxDead != 1 {
		t.Errorf("dead 存量应为 1,got %d", got.OutboxDead)
	}

	dl := map[string]int64{}
	for _, d := range got.DeadLetters {
		dl[d.Topic] = d.Count
	}
	if dl["qs-a"] != 2 {
		t.Errorf("死信存量应为 2,got %d", dl["qs-a"])
	}

	if _, err := st.pool.Exec(ctx,
		`DELETE FROM outbox WHERE topic LIKE 'qs-%';
		 DELETE FROM dead_letters WHERE topic LIKE 'qs-%'`); err != nil {
		t.Fatalf("清理: %v", err)
	}
}
