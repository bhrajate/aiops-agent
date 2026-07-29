package store

// 对真实 PostgreSQL 验证 InsertSignalWithOutbox 的去重契约(F5)。

import (
	"context"
	"testing"
	"time"

	"github.com/aiops/control-plane/internal/model"
)

func f5Signal(id string) model.Signal {
	now := time.Now().UTC()
	return model.Signal{
		SignalID:    id,
		TenantID:    "default",
		ClusterID:   "prod-cn-1",
		Source:      "alertmanager",
		SignalType:  "alert",
		Severity:    "warning",
		ResourceRef: model.ResourceRef{Namespace: "f5-store", Kind: "Deployment", Name: "checkout"},
		Labels:      map[string]string{"namespace": "f5-store", "alertname": "F5StoreDup"},
		PayloadHash: "sha256:deadbeef",
		ReceivedAt:  now,
	}
}

func f5Cleanup(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.pool.Exec(context.Background(),
		`DELETE FROM outbox WHERE key LIKE 'sig-f5store%';
		 DELETE FROM signals WHERE signal_id LIKE 'sig-f5store%'`); err != nil {
		t.Fatalf("清理: %v", err)
	}
}

// TestDBInsertSignalDedupesAndSkipsOutbox 是 F5 的存储层契约。
//
// 关键在**第二个断言**:ON CONFLICT DO NOTHING 只保证 signals 表不重复,
// 若无条件写 outbox,重复投递仍会各自发布一次事件,Incident Manager 每收一次
// 就把 signal_count +1 —— 表里 1 行、计数却是 N。而 signal_count 喂给触发策略
// (signal_count>=3 判为信号突发),于是一条告警重投三次就被当成影响面扩大。
// 实测就是这样:去掉随机后缀后 signals 表已去重到 1 行,signal_count 仍是 5。
func TestDBInsertSignalDedupesAndSkipsOutbox(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	f5Cleanup(t, st)
	t.Cleanup(func() { f5Cleanup(t, st) })

	sig := f5Signal("sig-f5store-001")

	inserted, err := st.InsertSignalWithOutbox(ctx, sig)
	if err != nil {
		t.Fatalf("首次插入: %v", err)
	}
	if !inserted {
		t.Fatal("首次插入应返回 inserted=true")
	}

	// 重投 4 次
	for i := 0; i < 4; i++ {
		inserted, err := st.InsertSignalWithOutbox(ctx, sig)
		if err != nil {
			t.Fatalf("重投第 %d 次: %v", i+1, err)
		}
		if inserted {
			t.Errorf("重投第 %d 次应返回 inserted=false", i+1)
		}
	}

	if n := countRows(t, st,
		`SELECT count(*) FROM signals WHERE signal_id='sig-f5store-001'`); n != 1 {
		t.Errorf("signals 表应只有 1 行,got %d", n)
	}
	// 这一条是重点:outbox 也必须只有 1 条,否则 signal_count 会虚增。
	if n := countRows(t, st,
		`SELECT count(*) FROM outbox WHERE topic='signals' AND key='sig-f5store-001'`); n != 1 {
		t.Errorf("outbox 应只有 1 条事件(否则 signal_count 随重投递虚增),got %d", n)
	}
}

// TestDBInsertSignalDistinctIDsBothPublish 不同信号各自发布,去重不能过度。
func TestDBInsertSignalDistinctIDsBothPublish(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	f5Cleanup(t, st)
	t.Cleanup(func() { f5Cleanup(t, st) })

	for _, id := range []string{"sig-f5store-a", "sig-f5store-b"} {
		inserted, err := st.InsertSignalWithOutbox(ctx, f5Signal(id))
		if err != nil {
			t.Fatalf("插入 %s: %v", id, err)
		}
		if !inserted {
			t.Errorf("%s 是新信号,应返回 inserted=true", id)
		}
	}
	if n := countRows(t, st,
		`SELECT count(*) FROM outbox WHERE topic='signals' AND key LIKE 'sig-f5store-%'`); n != 2 {
		t.Errorf("两条不同信号应各发布一次事件,got %d", n)
	}
}
