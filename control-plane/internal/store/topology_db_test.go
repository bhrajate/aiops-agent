package store

// 对真实 PostgreSQL 验证拓扑读写与 incident 关联。

import (
	"context"
	"testing"
	"time"
)

func topoCleanup(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.pool.Exec(context.Background(),
		`DELETE FROM service_topology WHERE cluster_id = 'topo-test'`); err != nil {
		t.Fatalf("清理拓扑: %v", err)
	}
}

func edge(from, to string, conf, rate float64, age time.Duration) TopologyEdge {
	return TopologyEdge{
		TenantID: "default", ClusterID: "topo-test",
		FromService: from, ToService: to, Kind: "call",
		Source: "tempo-service-graph", Confidence: conf, RequestRate: rate,
		LastSeen: time.Now().UTC().Add(-age),
	}
}

// TestDBUpsertTopologyIsIdempotent 同一条边重复同步不产生重复行。
// 同步每 5 分钟一轮,不幂等的话表会无界增长。
func TestDBUpsertTopologyIsIdempotent(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	topoCleanup(t, st)
	t.Cleanup(func() { topoCleanup(t, st) })

	for i := 0; i < 3; i++ {
		if _, err := st.UpsertTopologyEdges(ctx, []TopologyEdge{edge("checkout", "payment-api", 0.9, 5, 0)}); err != nil {
			t.Fatalf("第 %d 次 upsert: %v", i+1, err)
		}
	}
	if n := countRows(t, st,
		`SELECT count(*) FROM service_topology WHERE cluster_id='topo-test'`); n != 1 {
		t.Errorf("重复同步应只有 1 行, got %d", n)
	}
}

// TestDBUpsertTopologyPreservesFirstSeen first_seen 不被覆盖。
// 它回答"这条依赖什么时候出现的" —— 新出现的依赖本身是变更线索
// (某次发布引入了新的下游调用)。
func TestDBUpsertTopologyPreservesFirstSeen(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	topoCleanup(t, st)
	t.Cleanup(func() { topoCleanup(t, st) })

	if _, err := st.UpsertTopologyEdges(ctx, []TopologyEdge{edge("a", "b", 0.9, 1, 0)}); err != nil {
		t.Fatalf("首次: %v", err)
	}
	var first1 time.Time
	if err := st.pool.QueryRow(ctx,
		`SELECT first_seen FROM service_topology WHERE cluster_id='topo-test'`).Scan(&first1); err != nil {
		t.Fatalf("读 first_seen: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := st.UpsertTopologyEdges(ctx, []TopologyEdge{edge("a", "b", 0.9, 9, 0)}); err != nil {
		t.Fatalf("再次: %v", err)
	}
	var first2 time.Time
	var rate float64
	if err := st.pool.QueryRow(ctx,
		`SELECT first_seen, request_rate FROM service_topology WHERE cluster_id='topo-test'`).
		Scan(&first2, &rate); err != nil {
		t.Fatalf("读: %v", err)
	}
	if !first1.Equal(first2) {
		t.Errorf("first_seen 被覆盖了: %v → %v(它是这条依赖的出现时间,是变更线索)", first1, first2)
	}
	if rate != 9 {
		t.Errorf("request_rate 应被更新为 9, got %v", rate)
	}
}

// TestDBUpsertTopologySkipsPartialEdges 残缺边直接丢:关联时匹配不上任何资源。
func TestDBUpsertTopologySkipsPartialEdges(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	topoCleanup(t, st)
	t.Cleanup(func() { topoCleanup(t, st) })

	n, err := st.UpsertTopologyEdges(ctx, []TopologyEdge{
		edge("", "b", 0.9, 1, 0),
		edge("a", "", 0.9, 1, 0),
		edge("a", "b", 0.9, 1, 0),
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if n != 1 {
		t.Errorf("应只写入 1 条(残缺边丢弃), got %d", n)
	}
}

// TestDBNeighborsOfDirection 方向必须正确 —— 它决定"谁更可能是根因"。
func TestDBNeighborsOfDirection(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	topoCleanup(t, st)
	t.Cleanup(func() { topoCleanup(t, st) })

	// gateway → checkout → payment-api
	if _, err := st.UpsertTopologyEdges(ctx, []TopologyEdge{
		edge("gateway", "checkout", 0.9, 10, 0),
		edge("checkout", "payment-api", 0.9, 8, 0),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	up, down, err := st.NeighborsOf(ctx, "default", "topo-test", "checkout", 3600, 0.5)
	if err != nil {
		t.Fatalf("NeighborsOf: %v", err)
	}
	if len(up) != 1 || up[0].FromService != "gateway" {
		t.Errorf("上游应是 gateway(它在调用 checkout), got %+v", up)
	}
	if len(down) != 1 || down[0].ToService != "payment-api" {
		t.Errorf("下游应是 payment-api, got %+v", down)
	}
}

// TestDBNeighborsOfFiltersStale 陈旧边必须被过滤。
//
// 服务下线后边停止刷新。继续用它做关联会产出"疑似与一个早已不存在的服务同源"
// 这种误导结论 —— 值班人员会去查一个不存在的服务。
func TestDBNeighborsOfFiltersStale(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	topoCleanup(t, st)
	t.Cleanup(func() { topoCleanup(t, st) })

	if _, err := st.UpsertTopologyEdges(ctx, []TopologyEdge{
		edge("fresh", "svc", 0.9, 1, 0),
		edge("gone", "svc", 0.9, 1, 2*time.Hour), // 2 小时未刷新
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	up, _, err := st.NeighborsOf(ctx, "default", "topo-test", "svc", 3600, 0.5)
	if err != nil {
		t.Fatalf("NeighborsOf: %v", err)
	}
	if len(up) != 1 || up[0].FromService != "fresh" {
		t.Errorf("陈旧边应被过滤,只留 fresh, got %+v", up)
	}
}

// TestDBNeighborsOfFiltersConfidence 低置信度边被过滤。
func TestDBNeighborsOfFiltersConfidence(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	topoCleanup(t, st)
	t.Cleanup(func() { topoCleanup(t, st) })

	low := edge("selector-only", "svc", 0.3, 1, 0)
	low.Source = "kubernetes-service"
	if _, err := st.UpsertTopologyEdges(ctx, []TopologyEdge{
		edge("real-caller", "svc", 0.9, 1, 0), low,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	up, _, err := st.NeighborsOf(ctx, "default", "topo-test", "svc", 3600, 0.5)
	if err != nil {
		t.Fatalf("NeighborsOf: %v", err)
	}
	if len(up) != 1 || up[0].FromService != "real-caller" {
		t.Errorf("低置信度边应被过滤, got %+v", up)
	}
}

// TestDBNeighborsOfScopedByCluster 集群隔离:不能读到别的集群的拓扑。
// 共享后端下这个错误在诊断结论里看不出来 —— 拓扑图看着完整,只是画的是别人的集群。
func TestDBNeighborsOfScopedByCluster(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	topoCleanup(t, st)
	t.Cleanup(func() {
		topoCleanup(t, st)
		_, _ = st.pool.Exec(ctx, `DELETE FROM service_topology WHERE cluster_id='topo-other'`)
	})

	other := edge("other-caller", "svc", 0.9, 1, 0)
	other.ClusterID = "topo-other"
	if _, err := st.UpsertTopologyEdges(ctx, []TopologyEdge{
		edge("own-caller", "svc", 0.9, 1, 0), other,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	up, _, err := st.NeighborsOf(ctx, "default", "topo-test", "svc", 3600, 0.5)
	if err != nil {
		t.Fatalf("NeighborsOf: %v", err)
	}
	if len(up) != 1 || up[0].FromService != "own-caller" {
		t.Errorf("不该读到其他集群的边, got %+v", up)
	}
}

// TestDBPurgeStaleTopology 清理陈旧边。
func TestDBPurgeStaleTopology(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	topoCleanup(t, st)
	t.Cleanup(func() { topoCleanup(t, st) })

	if _, err := st.UpsertTopologyEdges(ctx, []TopologyEdge{
		edge("fresh", "svc", 0.9, 1, 0),
		edge("ancient", "svc", 0.9, 1, 10*24*time.Hour),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	n, err := st.PurgeStaleTopology(ctx, 7, 100)
	if err != nil {
		t.Fatalf("PurgeStaleTopology: %v", err)
	}
	if n != 1 {
		t.Errorf("应清理 1 条, got %d", n)
	}
	if left := countRows(t, st,
		`SELECT count(*) FROM service_topology WHERE cluster_id='topo-test'`); left != 1 {
		t.Errorf("应剩 1 条, got %d", left)
	}
	// days<=0 表示不清理(与 retention 其他项一致)
	if n, err := st.PurgeStaleTopology(ctx, 0, 100); err != nil || n != 0 {
		t.Errorf("days<=0 应不清理, got n=%d err=%v", n, err)
	}
}
