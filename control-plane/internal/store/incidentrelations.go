package store

// incident 之间的"疑似同源"链接。
//
// 刻意**不合并** incident。合并的诱惑很大(值班人员只看一个),但风险不对称:
// correlation_key 上有部分唯一索引,跨 namespace 合并会破坏它;更要紧的是
// 一条误判的拓扑边会把两次无关故障焊死成一个 incident,而**拆分比合并难得多**
// —— 已写入的 signal 与证据没法回滚归属,时间线也无法还原。
//
// 链接给出同样的信息(值班人员看到"疑似与 inc-x 同源,它在调用链上游"),
// 但可以随时撤销,且两个 incident 各自保留独立的时间线与影响面。

import (
	"context"
	"encoding/json"
	"time"
)

// IncidentRelation 一条 incident 关联。
type IncidentRelation struct {
	IncidentID        string         `json:"incident_id"`
	RelatedIncidentID string         `json:"related_incident_id"`
	Relation          string         `json:"relation"` // upstream / downstream
	ViaEdge           map[string]any `json:"via_edge"`
	Confidence        float64        `json:"confidence"`
	CreatedAt         time.Time      `json:"created_at"`
}

// LinkIncidents 建立双向关联(A upstream B 同时意味着 B downstream A)。
//
// 两条都写是为了让任一侧的查询都只需一次单向查表 —— 值班人员从任意一个
// incident 进来都要能看到对面。幂等:重复调用不产生重复行。
func (s *Store) LinkIncidents(ctx context.Context, tenantID, incidentID, relatedID,
	relation string, viaEdge map[string]any, confidence float64) error {
	if incidentID == "" || relatedID == "" || incidentID == relatedID {
		return nil
	}
	inverse := "downstream"
	if relation == "downstream" {
		inverse = "upstream"
	}
	edge, _ := json.Marshal(viaEdge)
	if edge == nil {
		edge = []byte("{}")
	}
	for _, pair := range []struct {
		a, b, rel string
	}{
		{incidentID, relatedID, relation},
		{relatedID, incidentID, inverse},
	} {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO incident_relations
			   (tenant_id, incident_id, related_incident_id, relation, via_edge, confidence)
			 VALUES ($1,$2,$3,$4,$5,$6)
			 ON CONFLICT (incident_id, related_incident_id, relation) DO UPDATE SET
			   confidence = GREATEST(incident_relations.confidence, EXCLUDED.confidence),
			   via_edge   = EXCLUDED.via_edge`,
			tenantID, pair.a, pair.b, pair.rel, edge, confidence); err != nil {
			return err
		}
	}
	return nil
}

// RelationsOf 返回某 incident 的关联(供 API 展示与 RCA 上下文)。
func (s *Store) RelationsOf(ctx context.Context, incidentID string) ([]IncidentRelation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT incident_id, related_incident_id, relation, via_edge, confidence, created_at
		   FROM incident_relations
		  WHERE incident_id = $1
		  ORDER BY confidence DESC, created_at`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]IncidentRelation, 0, 4)
	for rows.Next() {
		var r IncidentRelation
		var edge []byte
		if err := rows.Scan(&r.IncidentID, &r.RelatedIncidentID, &r.Relation,
			&edge, &r.Confidence, &r.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(edge, &r.ViaEdge)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetIncidentTopologyRefs 回填 incident 的拓扑上下文。
//
// 这一列此前恒为 '[]'。它会经 getContext 下发给 planner ——
// 有了它,规划器才知道"这个服务的上游是谁",从而把查询指向调用链而不是只看自己。
func (s *Store) SetIncidentTopologyRefs(ctx context.Context, incidentID string, refs []any) error {
	if refs == nil {
		refs = []any{}
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE incidents SET topology_refs = $1, updated_at = now() WHERE incident_id = $2`,
		mustJSON(refs), incidentID)
	return err
}

// ActiveIncidentByService 找出影响指定服务的活跃 incident。
//
// 用 alert_groups 而非 incidents.affected_resources 定位:group 是去重单元,
// 一个 group 精确对应一个资源,而 affected_resources 是聚合结果(可能含多个)。
// 从 group 反查更准,也天然复用了 idx_alert_groups_scope 索引。
func (s *Store) ActiveIncidentByService(ctx context.Context, tenantID, clusterID,
	namespace, service string) (string, bool, error) {
	if service == "" {
		return "", false, nil
	}
	var incidentID string
	err := s.pool.QueryRow(ctx,
		`SELECT g.incident_id
		   FROM alert_groups g
		   JOIN incidents c ON c.incident_id = g.incident_id
		  WHERE g.tenant_id = $1 AND g.cluster_id = $2
		    AND g.status = 'open'
		    AND c.status IN ('open','acknowledged')
		    AND g.resource_ref->>'name' = $3
		    AND ($4 = '' OR g.namespace = $4)
		  ORDER BY g.last_seen DESC
		  LIMIT 1`, tenantID, clusterID, service, namespace).Scan(&incidentID)
	if err != nil {
		return "", false, nil // 无匹配不是错误:大多数邻居服务是健康的
	}
	return incidentID, incidentID != "", nil
}
