package store

// 单租户护栏对真实库的契约。
//
// 这个护栏防的是一类**没有任何症状**的配置错误:两个租户指向同一个库时,
// 系统照常跑,而 GET /v1/incidents 会把两边的 incident 一起返回。
// ABAC 拦不住(它按 cluster/namespace 过滤,而不同租户可能用同名 namespace),
// 审计里每一条也都是"某个合法用户读了某个存在的 incident"。

import (
	"context"
	"errors"
	"testing"
)

func tenantCleanup(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.pool.Exec(context.Background(),
		`DELETE FROM incidents WHERE incident_id LIKE 'inc-tg-%'`); err != nil {
		t.Fatalf("清理: %v", err)
	}
}

func seedTenantIncident(t *testing.T, st *Store, id, tenant string) {
	t.Helper()
	_, err := st.pool.Exec(context.Background(),
		`INSERT INTO incidents (incident_id, tenant_id, cluster_id, version, grouping_key,
		   status, severity, title, affected_resources, blast_radius, signal_count)
		 VALUES ($1,$2,'prod-cn-1',1,$3,'open','P3','tg test','[]'::jsonb,'{}'::jsonb,1)`,
		id, tenant, "gk-"+id)
	if err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func TestDBSingleTenantPassesOnEmptyDB(t *testing.T) {
	st := openStoreDB(t)
	tenantCleanup(t, st)
	t.Cleanup(func() { tenantCleanup(t, st) })
	// 空库(或只有别的测试留下的 default 数据)不该报错 —— 首次部署必须能起来。
	if err := st.CheckSingleTenant(context.Background(), "default"); err != nil {
		var mm *TenantMismatch
		if errors.As(err, &mm) {
			t.Fatalf("空库/单租户库不该判为不一致: %v", err)
		}
		t.Fatalf("意外错误: %v", err)
	}
}

func TestDBSingleTenantPassesWhenOnlyConfiguredTenant(t *testing.T) {
	st := openStoreDB(t)
	tenantCleanup(t, st)
	t.Cleanup(func() { tenantCleanup(t, st) })

	seedTenantIncident(t, st, "inc-tg-a", "default")
	seedTenantIncident(t, st, "inc-tg-b", "default")

	if err := st.CheckSingleTenant(context.Background(), "default"); err != nil {
		t.Fatalf("只有配置租户的数据不该报错: %v", err)
	}
}

func TestDBSingleTenantRejectsForeignTenant(t *testing.T) {
	st := openStoreDB(t)
	tenantCleanup(t, st)
	t.Cleanup(func() { tenantCleanup(t, st) })

	seedTenantIncident(t, st, "inc-tg-own", "default")
	seedTenantIncident(t, st, "inc-tg-other", "acme-corp")

	err := st.CheckSingleTenant(context.Background(), "default")
	var mm *TenantMismatch
	if !errors.As(err, &mm) {
		t.Fatalf("库里有其他租户的数据必须被拒,得到 %v", err)
	}
	if mm.Configured != "default" {
		t.Errorf("Configured = %q, want default", mm.Configured)
	}
	// 错误信息必须把实际发现的租户列出来 —— 否则运维只知道"有问题"
	// 而不知道是哪个租户的数据混进来了,无从下手。
	var hasOther bool
	for _, f := range mm.Found {
		if f == "acme-corp" {
			hasOther = true
		}
	}
	if !hasOther {
		t.Errorf("Found = %v,应含混入的 acme-corp", mm.Found)
	}
	if msg := mm.Error(); msg == "" {
		t.Error("错误信息不能为空")
	}
}

func TestDBSingleTenantRejectsWhenConfiguredTenantAbsent(t *testing.T) {
	st := openStoreDB(t)
	tenantCleanup(t, st)
	t.Cleanup(func() { tenantCleanup(t, st) })

	// 库里只有 acme-corp,而本进程配的是 default。
	// 这通常意味着**连错了库** —— 比"多租户混住"更危险:
	// 后续写入会把两个租户的数据真正混进同一张表。
	seedTenantIncident(t, st, "inc-tg-x", "acme-corp")

	var mm *TenantMismatch
	if err := st.CheckSingleTenant(context.Background(), "default"); !errors.As(err, &mm) {
		t.Fatalf("配置租户与库里租户完全不同时必须被拒(很可能连错了库),得到 %v", err)
	}
}
