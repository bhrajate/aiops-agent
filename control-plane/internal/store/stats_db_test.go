package store

// IncidentStatRows 的取件范围契约。
//
// 这里钉住的是一个**静默**缺陷:MTTR 与趋势的 resolved 序列都从这个函数取数,
// 而它此前只按 last_seen 或"仍未闭环"取件。于是"信号早就停了、人过了很久才去
// 解决"这类长尾故障两个条件都不满足 —— 它从样本里消失,不报错、不记日志。
//
// 失效方式:MTTR 只统计到那些"信号还在持续期间就被解决"的故障,系统性偏向短故障,
// 读起来是"我们解决得很快"。而 MTTR 存在的意义恰恰是度量长尾。

import (
	"context"
	"testing"
	"time"
)

func statsCleanup(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.pool.Exec(context.Background(),
		`DELETE FROM incidents WHERE incident_id LIKE 'inc-stats-%'`); err != nil {
		t.Fatalf("清理: %v", err)
	}
}

// seedStatIncident 造一条 incident,时间戳全部显式给定。
// resolvedAt 为 nil 表示仍未闭环。
func seedStatIncident(t *testing.T, st *Store, id, status string,
	firstSeen, lastSeen time.Time, resolvedAt *time.Time) {
	t.Helper()
	_, err := st.pool.Exec(context.Background(),
		`INSERT INTO incidents (incident_id, tenant_id, cluster_id, version, grouping_key,
		   status, severity, title, fault_category, affected_resources, blast_radius,
		   signal_count, first_seen, last_seen, resolved_at)
		 VALUES ($1,'default','prod-cn-1',1,$2,$3,'P2',$4,'pod_workload',
		   '[{"namespace":"payment","kind":"Deployment","name":"checkout"}]'::jsonb,
		   '{}'::jsonb, 3, $5, $6, $7)`,
		id, "gk-"+id, status, "stats "+id, firstSeen, lastSeen, resolvedAt)
	if err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// TestDBIncidentStatRowsIncludesLateResolved 是本文件的核心断言。
//
// 场景:一个 3 小时前爆发的故障,信号在 2.5 小时前就停了(last_seen),
// 但值班人员 10 分钟前才把它标成 resolved。查 1 小时窗口时:
//   - last_seen(2.5h 前)不在窗口内
//   - status 是 resolved,不在 ('open','acknowledged') 里
//
// 修复前它两个条件都不满足 → 不在结果里 → MTTR 拿不到这个样本。
//
// 而它恰恰是 MTTR 最该统计的那种故障:总耗时 2h50m,远超那些"信号还在响就被
// 解决"的短故障。漏掉它会让均值系统性偏低。
func TestDBIncidentStatRowsIncludesLateResolved(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	statsCleanup(t, st)
	t.Cleanup(func() { statsCleanup(t, st) })

	now := time.Now()
	resolved := now.Add(-10 * time.Minute)
	seedStatIncident(t, st, "inc-stats-late", "resolved",
		now.Add(-3*time.Hour),     // first_seen
		now.Add(-150*time.Minute), // last_seen:窗口外
		&resolved,                 // resolved_at:窗口内
	)

	rows, err := st.IncidentStatRows(ctx, 1) // 1 小时窗口
	if err != nil {
		t.Fatalf("IncidentStatRows: %v", err)
	}

	var found bool
	for _, r := range rows {
		if r.IncidentID == "inc-stats-late" {
			found = true
			if r.ResolvedAt == nil {
				t.Error("ResolvedAt 应被取出,MTTR 依赖它算耗时")
			}
		}
	}
	if !found {
		t.Fatal("窗口内被解决但 last_seen 在窗口外的 incident 未被取出 —— " +
			"MTTR 与趋势的 resolved 序列会静默漏掉整类长尾故障")
	}
}

// TestDBIncidentStatRowsKeepsOpenOutsideWindow 保证修复没有破坏原有语义:
// 未闭环的老 incident 必须始终返回,不受窗口约束。
// 一个三天前爆发、至今没人处理的 P1 必须出现在值班总览上。
func TestDBIncidentStatRowsKeepsOpenOutsideWindow(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	statsCleanup(t, st)
	t.Cleanup(func() { statsCleanup(t, st) })

	now := time.Now()
	seedStatIncident(t, st, "inc-stats-stale-open", "open",
		now.Add(-72*time.Hour), now.Add(-72*time.Hour), nil)

	rows, err := st.IncidentStatRows(ctx, 1)
	if err != nil {
		t.Fatalf("IncidentStatRows: %v", err)
	}
	for _, r := range rows {
		if r.IncidentID == "inc-stats-stale-open" {
			return
		}
	}
	t.Fatal("三天前未闭环的 incident 未被取出 —— 值班总览会漏掉最该被看见的故障")
}

// TestDBIncidentStatRowsExcludesOldClosed 保证修复没有把范围放得过宽:
// 窗口外就已经关闭的 incident 不该进来,否则窗口失去意义、5000 上限也更容易撞。
func TestDBIncidentStatRowsExcludesOldClosed(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	statsCleanup(t, st)
	t.Cleanup(func() { statsCleanup(t, st) })

	now := time.Now()
	oldResolved := now.Add(-48 * time.Hour)
	seedStatIncident(t, st, "inc-stats-old-closed", "closed",
		now.Add(-72*time.Hour), now.Add(-50*time.Hour), &oldResolved)

	rows, err := st.IncidentStatRows(ctx, 1)
	if err != nil {
		t.Fatalf("IncidentStatRows: %v", err)
	}
	for _, r := range rows {
		if r.IncidentID == "inc-stats-old-closed" {
			t.Fatal("48 小时前就解决的 incident 不该出现在 1 小时窗口里")
		}
	}
}
