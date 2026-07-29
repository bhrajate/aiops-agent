package store

// 服务依赖拓扑的读写。
//
// `incidents.topology_refs` 从 000001 起就有这一列,但一直没有写入路径 ——
// 相关性合并只按 tenant|cluster|namespace,调用链上的故障传播识别不了:
// checkout 挂了导致 payment-api 超时,值班人员看到两个互不相关的 incident,
// 而根因只有一个。

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// TopologyEdge 一条服务依赖边。
type TopologyEdge struct {
	TenantID      string
	ClusterID     string
	FromService   string
	ToService     string
	FromNamespace string
	ToNamespace   string
	Kind          string  // call / database / messaging / service-frontend
	Source        string  // tempo-service-graph / kubernetes-service
	Confidence    float64 // 参与关联决策:低置信度不足以链接 incident
	RequestRate   float64 // 观测窗口内请求量,用于在多条边里挑主路径
	LastSeen      time.Time
}

// UpsertTopologyEdges 批量写入拓扑边。同一 (tenant,cluster,from,to,kind,source)
// 更新其 last_seen / confidence / request_rate,first_seen 保持不变。
//
// first_seen 不覆盖是有意的:它回答"这条依赖是什么时候出现的",
// 而新出现的依赖本身就是变更线索(某次发布引入了新的下游调用)。
func (s *Store) UpsertTopologyEdges(ctx context.Context, edges []TopologyEdge) (int, error) {
	if len(edges) == 0 {
		return 0, nil
	}
	var n int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, e := range edges {
			if e.FromService == "" || e.ToService == "" {
				continue // 残缺边直接丢:关联时匹配不上任何资源,留着只是噪声
			}
			if e.Kind == "" {
				e.Kind = "call"
			}
			if e.LastSeen.IsZero() {
				e.LastSeen = time.Now().UTC()
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO service_topology
				   (tenant_id, cluster_id, from_service, to_service,
				    from_namespace, to_namespace, kind, source, confidence,
				    request_rate, first_seen, last_seen)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
				 ON CONFLICT (tenant_id, cluster_id, from_service, to_service, kind, source)
				 DO UPDATE SET
				   confidence    = EXCLUDED.confidence,
				   request_rate  = EXCLUDED.request_rate,
				   from_namespace = COALESCE(NULLIF(EXCLUDED.from_namespace,''), service_topology.from_namespace),
				   to_namespace   = COALESCE(NULLIF(EXCLUDED.to_namespace,''), service_topology.to_namespace),
				   last_seen     = EXCLUDED.last_seen`,
				e.TenantID, e.ClusterID, e.FromService, e.ToService,
				e.FromNamespace, e.ToNamespace, e.Kind, e.Source, e.Confidence,
				e.RequestRate, e.LastSeen); err != nil {
				return fmt.Errorf("upsert topology edge %s→%s: %w", e.FromService, e.ToService, err)
			}
			n++
		}
		return nil
	})
	return n, err
}

// NeighborsOf 返回某服务在拓扑上的直接邻居(双向)。
//
// maxAgeSec 过滤陈旧边:服务下线后边会停止刷新,继续用它做关联会产出
// "疑似与一个早就不存在的服务同源"这种误导结论。
// minConfidence 过滤低置信度边(如仅由 K8s Service selector 推导的入口关系)。
func (s *Store) NeighborsOf(ctx context.Context, tenantID, clusterID, service string,
	maxAgeSec int, minConfidence float64) (upstream, downstream []TopologyEdge, err error) {
	if service == "" {
		return nil, nil, nil
	}
	rows, qerr := s.pool.Query(ctx,
		`SELECT tenant_id, cluster_id, from_service, to_service,
		        from_namespace, to_namespace, kind, source, confidence, request_rate, last_seen
		   FROM service_topology
		  WHERE tenant_id = $1 AND cluster_id = $2
		    AND (from_service = $3 OR to_service = $3)
		    AND confidence >= $4
		    AND last_seen > now() - make_interval(secs => $5::double precision)
		  ORDER BY request_rate DESC, confidence DESC
		  LIMIT 100`,
		tenantID, clusterID, service, minConfidence, maxAgeSec)
	if qerr != nil {
		return nil, nil, qerr
	}
	defer rows.Close()
	for rows.Next() {
		var e TopologyEdge
		if err := rows.Scan(&e.TenantID, &e.ClusterID, &e.FromService, &e.ToService,
			&e.FromNamespace, &e.ToNamespace, &e.Kind, &e.Source,
			&e.Confidence, &e.RequestRate, &e.LastSeen); err != nil {
			return nil, nil, err
		}
		// to_service == service 表示对方在调用我 → 对方是上游。
		if e.ToService == service {
			upstream = append(upstream, e)
		} else {
			downstream = append(downstream, e)
		}
	}
	return upstream, downstream, rows.Err()
}

// PurgeStaleTopology 删除长期未刷新的边(供 retention janitor 调用)。
func (s *Store) PurgeStaleTopology(ctx context.Context, days, batch int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM service_topology
		  WHERE edge_id IN (
		      SELECT edge_id FROM service_topology
		       WHERE last_seen < now() - make_interval(days => $1)
		       LIMIT $2)`, days, batch)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
