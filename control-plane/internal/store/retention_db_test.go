package store

import (
	"context"
	"os"
	"testing"
)

// 这些用例跑真实 Postgres:保留清理的风险全在 SQL 里(外键顺序、安全谓词),
// 用替身测不出来。未设 AIOPS_DB_DSN 时跳过。
// 名字带 DB 以便 CI 用 -run 'DB|Integration' 精确执行。
func openStoreDB(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("AIOPS_DB_DSN")
	if dsn == "" {
		t.Skip("AIOPS_DB_DSN 未设置,跳过 DB 用例")
	}
	st, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// seedCase 造一个完整案例:incident + alert_group + signal + investigation +
// evidence + hypothesis + event。ageDays>0 时把时间戳往前推。
func seedCase(t *testing.T, st *Store, id string, status string, phase string, ageDays int) {
	t.Helper()
	ctx := context.Background()
	incID := "inc-test-" + id
	invID := "inv-test-" + id

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := st.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	exec(`INSERT INTO incidents (incident_id, tenant_id, cluster_id, grouping_key,
	        correlation_key, status, severity, title, fault_category, updated_at)
	      VALUES ($1,'retention-test','c1',$2,$3,$4,'P3','t','pod_workload',
	              now() - make_interval(days => $5::int))`,
		incID, "gk-"+id, "ck-"+id, status, ageDays)
	exec(`INSERT INTO alert_groups (group_id, tenant_id, cluster_id, grouping_key,
	        namespace, resource_ref, severity, fault_category, title, status, incident_id)
	      VALUES ($1,'retention-test','c1',$2,'ns','{}','P3','pod_workload','t','resolved',$3)`,
		"grp-test-"+id, "agk-"+id, incID)
	exec(`INSERT INTO signals (signal_id, tenant_id, cluster_id, source, signal_type, incident_id)
	      VALUES ($1,'retention-test','c1','alertmanager','alert',$2)`, "sig-test-"+id, incID)
	exec(`INSERT INTO investigations (investigation_id, tenant_id, incident_id,
	        incident_version, phase, started_at)
	      VALUES ($1,'retention-test',$2,1,$3, now() - make_interval(days => $4::int))`,
		invID, incID, phase, ageDays)
	exec(`INSERT INTO evidence (evidence_id, tenant_id, investigation_id, type, source,
	        summary, content_hash)
	      VALUES ($1,'retention-test',$2,'metric','prometheus','s','sha256:test')`,
		"ev-test-"+id, invID)
	exec(`INSERT INTO hypotheses (hypothesis_id, investigation_id, rank, statement, confidence)
	      VALUES ($1,$2,1,'s',0.5)`, "hyp-test-"+id, invID)
	exec(`INSERT INTO investigation_events (investigation_id, seq, event_type)
	      VALUES ($1,1,'phase_changed')`, invID)
}

func cleanupTestRows(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	for _, sql := range []string{
		`DELETE FROM investigation_events WHERE investigation_id LIKE 'inv-test-%'`,
		`DELETE FROM human_feedback WHERE investigation_id LIKE 'inv-test-%'`,
		`DELETE FROM hypotheses WHERE investigation_id LIKE 'inv-test-%'`,
		`DELETE FROM evidence WHERE investigation_id LIKE 'inv-test-%'`,
		`DELETE FROM investigations WHERE tenant_id='retention-test'`,
		`DELETE FROM alert_groups WHERE tenant_id='retention-test'`,
		`DELETE FROM signals WHERE tenant_id='retention-test'`,
		`DELETE FROM incidents WHERE tenant_id='retention-test'`,
		`DELETE FROM audit_log WHERE tenant_id='retention-test'`,
	} {
		if _, err := st.pool.Exec(ctx, sql); err != nil {
			t.Logf("cleanup(%s): %v", sql, err)
		}
	}
}

func countRows(t *testing.T, st *Store, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := st.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// 核心安全属性 1:活跃 incident 永不被清理,即使很旧。
func TestDBPurgeClosedCasesSkipsActiveIncident(t *testing.T) {
	st := openStoreDB(t)
	cleanupTestRows(t, st)
	t.Cleanup(func() { cleanupTestRows(t, st) })

	seedCase(t, st, "active", "open", "closed", 999)

	if _, _, err := st.PurgeClosedCases(context.Background(), 30, 100); err != nil {
		t.Fatalf("PurgeClosedCases: %v", err)
	}
	if n := countRows(t, st, `SELECT count(*) FROM incidents WHERE incident_id='inc-test-active'`); n != 1 {
		t.Error("活跃 incident 被清理了(严重:会删掉正在处理的故障上下文)")
	}
}

// 核心安全属性 2:incident 已终态但仍有未结束调查 → 不清理。
func TestDBPurgeClosedCasesSkipsRunningInvestigation(t *testing.T) {
	st := openStoreDB(t)
	cleanupTestRows(t, st)
	t.Cleanup(func() { cleanupTestRows(t, st) })

	seedCase(t, st, "running", "resolved", "collecting", 999)

	if _, _, err := st.PurgeClosedCases(context.Background(), 30, 100); err != nil {
		t.Fatalf("PurgeClosedCases: %v", err)
	}
	if n := countRows(t, st, `SELECT count(*) FROM incidents WHERE incident_id='inc-test-running'`); n != 1 {
		t.Error("仍有进行中调查的 incident 被清理(会让运行中的 RCA 丢上下文)")
	}
}

// 未过期的终态案例不动。
func TestDBPurgeClosedCasesRespectsRetentionWindow(t *testing.T) {
	st := openStoreDB(t)
	cleanupTestRows(t, st)
	t.Cleanup(func() { cleanupTestRows(t, st) })

	seedCase(t, st, "fresh", "closed", "closed", 1)

	if _, _, err := st.PurgeClosedCases(context.Background(), 30, 100); err != nil {
		t.Fatalf("PurgeClosedCases: %v", err)
	}
	if n := countRows(t, st, `SELECT count(*) FROM incidents WHERE incident_id='inc-test-fresh'`); n != 1 {
		t.Error("1 天前的案例在 30 天保留期内却被清理")
	}
}

// 过期终态案例被整案清理:所有子表都要跟着删干净(外键顺序正确)。
func TestDBPurgeClosedCasesCascades(t *testing.T) {
	st := openStoreDB(t)
	cleanupTestRows(t, st)
	t.Cleanup(func() { cleanupTestRows(t, st) })

	seedCase(t, st, "old", "closed", "closed", 999)

	cases, rows, err := st.PurgeClosedCases(context.Background(), 30, 100)
	if err != nil {
		t.Fatalf("PurgeClosedCases: %v", err)
	}
	if cases != 1 {
		t.Fatalf("清理了 %d 个案例,应为 1", cases)
	}
	if rows < 7 {
		t.Errorf("只删了 %d 行,子表可能没删干净", rows)
	}
	for _, c := range []struct {
		name string
		sql  string
	}{
		{"incidents", `SELECT count(*) FROM incidents WHERE incident_id='inc-test-old'`},
		{"investigations", `SELECT count(*) FROM investigations WHERE investigation_id='inv-test-old'`},
		{"evidence", `SELECT count(*) FROM evidence WHERE investigation_id='inv-test-old'`},
		{"hypotheses", `SELECT count(*) FROM hypotheses WHERE investigation_id='inv-test-old'`},
		{"events", `SELECT count(*) FROM investigation_events WHERE investigation_id='inv-test-old'`},
		{"alert_groups", `SELECT count(*) FROM alert_groups WHERE group_id='grp-test-old'`},
		{"signals", `SELECT count(*) FROM signals WHERE signal_id='sig-test-old'`},
	} {
		if n := countRows(t, st, c.sql); n != 0 {
			t.Errorf("%s 残留 %d 行", c.name, n)
		}
	}
}

// 按时间清理:days=0 表示不清理(合规场景常用),必须真的不删。
func TestDBPurgeOlderThanDisabledByZeroDays(t *testing.T) {
	st := openStoreDB(t)
	cleanupTestRows(t, st)
	t.Cleanup(func() { cleanupTestRows(t, st) })
	ctx := context.Background()

	if _, err := st.pool.Exec(ctx,
		`INSERT INTO audit_log (tenant_id, actor, action, created_at)
		 VALUES ('retention-test','system','test', now() - interval '999 days')`); err != nil {
		t.Fatal(err)
	}
	n, err := st.PurgeOlderThan(ctx, "audit_log", "created_at", 0, 100)
	if err != nil {
		t.Fatalf("PurgeOlderThan: %v", err)
	}
	if n != 0 {
		t.Errorf("days=0 却删了 %d 行", n)
	}
	if got := countRows(t, st, `SELECT count(*) FROM audit_log WHERE tenant_id='retention-test'`); got != 1 {
		t.Errorf("审计行被删了(days=0 应完全禁用清理)")
	}

	// days>0 时才真的删。
	if _, err := st.PurgeOlderThan(ctx, "audit_log", "created_at", 30, 100); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, st, `SELECT count(*) FROM audit_log WHERE tenant_id='retention-test'`); got != 0 {
		t.Errorf("999 天前的审计行未被清理")
	}
}

// outbox:只删已投递;pending/failed 是待重试事件,删掉等于丢事件。
func TestDBPurgePublishedOutboxKeepsPending(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	old := `now() - interval '999 days'`
	for _, status := range []string{"published", "pending", "failed"} {
		if _, err := st.pool.Exec(ctx,
			`INSERT INTO outbox (topic, key, payload, status, created_at, published_at)
			 VALUES ('retention-test',$1,'{}',$2, `+old+`, `+old+`)`,
			"k-"+status, status); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM outbox WHERE topic='retention-test'`)
	})

	if _, err := st.PurgePublishedOutbox(ctx, 30, 100); err != nil {
		t.Fatalf("PurgePublishedOutbox: %v", err)
	}
	if n := countRows(t, st, `SELECT count(*) FROM outbox WHERE topic='retention-test' AND status='published'`); n != 0 {
		t.Errorf("已投递记录残留 %d 行", n)
	}
	if n := countRows(t, st, `SELECT count(*) FROM outbox WHERE topic='retention-test' AND status IN ('pending','failed')`); n != 2 {
		t.Errorf("待重试事件被删了(会丢事件),剩 %d 行应为 2", n)
	}
}

// 未归属 incident 的孤儿信号按时间清理;已归属的留给整案清理。
func TestDBPurgeOrphanSignalsKeepsAttached(t *testing.T) {
	st := openStoreDB(t)
	cleanupTestRows(t, st)
	t.Cleanup(func() { cleanupTestRows(t, st) })
	ctx := context.Background()

	seedCase(t, st, "attached", "open", "closed", 999)
	if _, err := st.pool.Exec(ctx,
		`UPDATE signals SET received_at = now() - interval '999 days' WHERE tenant_id='retention-test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO signals (signal_id, tenant_id, cluster_id, source, signal_type, received_at)
		 VALUES ('sig-test-orphan','retention-test','c1','alertmanager','alert', now() - interval '999 days')`); err != nil {
		t.Fatal(err)
	}

	if _, err := st.PurgeOrphanSignals(ctx, 30, 100); err != nil {
		t.Fatalf("PurgeOrphanSignals: %v", err)
	}
	if n := countRows(t, st, `SELECT count(*) FROM signals WHERE signal_id='sig-test-orphan'`); n != 0 {
		t.Error("孤儿信号未被清理")
	}
	if n := countRows(t, st, `SELECT count(*) FROM signals WHERE signal_id='sig-test-attached'`); n != 1 {
		t.Error("已归属 incident 的信号被清理了(应随整案清理,保持可回溯)")
	}
}

// advisory lock:同一时刻只有一个持有者(多副本互斥的基础)。
func TestDBRetentionLockIsExclusive(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()

	release, ok, err := st.TryRetentionLock(ctx)
	if err != nil {
		t.Fatalf("TryRetentionLock: %v", err)
	}
	if !ok {
		t.Fatal("首次获取锁失败")
	}
	_, ok2, err := st.TryRetentionLock(ctx)
	if err != nil {
		t.Fatalf("第二次尝试报错: %v", err)
	}
	if ok2 {
		release()
		t.Fatal("锁未互斥:两个副本会同时清理")
	}
	release()

	// 释放后必须可再获取。
	release3, ok3, err := st.TryRetentionLock(ctx)
	if err != nil || !ok3 {
		t.Fatalf("释放后无法重新获取锁: ok=%v err=%v", ok3, err)
	}
	release3()
}
