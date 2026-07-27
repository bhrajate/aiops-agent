package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aiops/control-plane/internal/model"
	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("not found")

// UpsertIncidentWithOutbox 按 grouping_key 去重聚合:
//   - 不存在则创建(version=1);
//   - 已存在则递增 version、更新 last_seen、signal_count、severity(取更高)。
//
// 并在同事务写 incidents outbox。返回聚合后的 Incident 与是否为新建。
func (s *Store) UpsertIncidentWithOutbox(ctx context.Context, inc model.Incident) (model.Incident, bool, error) {
	var result model.Incident
	var created bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var existingID string
		var version, signalCount int
		var status string
		row := tx.QueryRow(ctx,
			`SELECT incident_id, version, signal_count, status FROM incidents
			 WHERE grouping_key=$1 FOR UPDATE`, inc.GroupingKey)
		err := row.Scan(&existingID, &version, &signalCount, &status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			created = true
			_, err = tx.Exec(ctx,
				`INSERT INTO incidents
				 (incident_id, tenant_id, cluster_id, version, grouping_key, status, severity,
				  title, fault_category, affected_resources, blast_radius, topology_refs,
				  change_refs, signal_count, first_seen, last_seen)
				 VALUES ($1,$2,$3,1,$4,'open',$5,$6,$7,$8,$9,$10,$11,1,$12,$12)`,
				inc.IncidentID, inc.TenantID, inc.ClusterID, inc.GroupingKey, inc.Severity,
				inc.Title, inc.FaultCategory, mustJSON(inc.AffectedResources),
				mustJSON(inc.BlastRadius), mustJSON(inc.TopologyRefs), mustJSON(inc.ChangeRefs),
				inc.LastSeen)
			if err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			// 已存在:若已 resolved/closed 则重新打开
			newStatus := status
			if status == "resolved" || status == "closed" {
				newStatus = "open"
			}
			_, err = tx.Exec(ctx,
				`UPDATE incidents SET version=version+1, signal_count=signal_count+1,
				   last_seen=$1, status=$2, updated_at=now(),
				   severity=LEAST(severity, $3)
				 WHERE incident_id=$4`,
				inc.LastSeen, newStatus, inc.Severity, existingID)
			if err != nil {
				return err
			}
			inc.IncidentID = existingID
		}
		// 读回聚合后完整记录
		loaded, lerr := loadIncidentTx(ctx, tx, inc.IncidentID)
		if lerr != nil {
			return lerr
		}
		result = loaded
		return EnqueueOutboxTx(ctx, tx, "incidents", result.IncidentID, result)
	})
	return result, created, err
}

func loadIncidentTx(ctx context.Context, tx pgx.Tx, id string) (model.Incident, error) {
	row := tx.QueryRow(ctx, incidentSelect+` WHERE incident_id=$1`, id)
	return scanIncident(row)
}

const incidentSelect = `SELECT incident_id, tenant_id, cluster_id, version, grouping_key, status,
	severity, title, COALESCE(fault_category,''), affected_resources, blast_radius,
	topology_refs, change_refs, signal_count, first_seen, last_seen, resolved_at, closed_at,
	created_at, updated_at FROM incidents`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanIncident(row rowScanner) (model.Incident, error) {
	var inc model.Incident
	var affected, blast, topo, changes []byte
	err := row.Scan(&inc.IncidentID, &inc.TenantID, &inc.ClusterID, &inc.Version,
		&inc.GroupingKey, &inc.Status, &inc.Severity, &inc.Title, &inc.FaultCategory,
		&affected, &blast, &topo, &changes, &inc.SignalCount, &inc.FirstSeen, &inc.LastSeen,
		&inc.ResolvedAt, &inc.ClosedAt, &inc.CreatedAt, &inc.UpdatedAt)
	if err != nil {
		return inc, err
	}
	_ = json.Unmarshal(affected, &inc.AffectedResources)
	_ = json.Unmarshal(blast, &inc.BlastRadius)
	_ = json.Unmarshal(topo, &inc.TopologyRefs)
	_ = json.Unmarshal(changes, &inc.ChangeRefs)
	return inc, nil
}

func (s *Store) GetIncident(ctx context.Context, id string) (model.Incident, error) {
	row := s.pool.QueryRow(ctx, incidentSelect+` WHERE incident_id=$1`, id)
	inc, err := scanIncident(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return inc, ErrNotFound
	}
	return inc, err
}

// ListIncidents 支持按 status/severity 过滤。
func (s *Store) ListIncidents(ctx context.Context, status, severity string, limit int) ([]model.Incident, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := incidentSelect + ` WHERE ($1='' OR status=$1) AND ($2='' OR severity=$2)
		ORDER BY last_seen DESC LIMIT $3`
	rows, err := s.pool.Query(ctx, q, status, severity, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

// SetIncidentStatus 更新 Incident 状态(resolved/closed 记录时间)。
func (s *Store) SetIncidentStatus(ctx context.Context, id, status string) error {
	var tsCol string
	switch status {
	case "resolved":
		tsCol = ", resolved_at=now()"
	case "closed":
		tsCol = ", closed_at=now()"
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE incidents SET status=$1, updated_at=now()`+tsCol+` WHERE incident_id=$2`,
		status, id)
	return err
}

// HasActiveInvestigation 判断某 incident 是否已有进行中的同版本调查(硬停止条件)。
func (s *Store) HasActiveInvestigation(ctx context.Context, incidentID string, version int) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM investigations
		 WHERE incident_id=$1 AND incident_version=$2
		   AND phase NOT IN ('closed','cancelled')`,
		incidentID, version).Scan(&n)
	return n > 0, err
}

// SecondsSinceLastInvestigation 返回该 incident 上一次调查启动距今的秒数。
// 无历史调查时返回 (0, false)。用于冷却期判断(文档 6.3)。
func (s *Store) SecondsSinceLastInvestigation(ctx context.Context, incidentID string) (float64, bool, error) {
	var secs float64
	err := s.pool.QueryRow(ctx,
		`SELECT EXTRACT(EPOCH FROM (now() - max(started_at)))
		 FROM investigations WHERE incident_id=$1`, incidentID).Scan(&secs)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		// max() over 空集返回 NULL → Scan 到 float64 报错;视为无历史
		return 0, false, nil
	}
	return secs, true, nil
}

// BlastRadius 表示一次故障的影响面(文档 6.2 correlation)。
type BlastRadius struct {
	Services   int                 `json:"services"`   // 同 tenant/cluster/namespace 时间窗内活跃 incident 涉及的不同资源数
	Namespaces int                 `json:"namespaces"` // 同 tenant/cluster 时间窗内活跃 incident 涉及的不同 namespace 数
	Incidents  int                 `json:"incidents"`  // 关联的活跃 incident 数
	Resources  []model.ResourceRef `json:"-"`          // 关联组内的资源(用于累积 affected_resources)
}

// ComputeCorrelatedBlastRadius 基于"关联层"计算影响面:
// 同一 tenant/cluster 下,时间窗(windowSec)内仍活跃(open/acknowledged)的 incident,
// 按 namespace 聚合。services = 同 namespace 内不同资源数;namespaces = 同 cluster 内不同 namespace 数。
// 这是 grouping_key(单资源去重)之上的相关性聚合,使"影响面扩大"可被 policy 闸门捕获。
// 注:纯时间+namespace 相关,不等于因果;拓扑关联为后续增强(文档 11.1)。
func (s *Store) ComputeCorrelatedBlastRadius(ctx context.Context, tenant, cluster, namespace string, windowSec int) (BlastRadius, error) {
	var br BlastRadius
	// namespaces:同 cluster 时间窗内活跃 incident 的不同 namespace 数
	err := s.pool.QueryRow(ctx,
		`SELECT count(DISTINCT (affected_resources->0->>'namespace'))
		 FROM incidents
		 WHERE tenant_id=$1 AND cluster_id=$2
		   AND status IN ('open','acknowledged')
		   AND last_seen > now() - ($3 || ' seconds')::interval`,
		tenant, cluster, windowSec).Scan(&br.Namespaces)
	if err != nil {
		return br, err
	}
	// services + 关联 incident 数:同 namespace 内不同资源
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT
		    affected_resources->0->>'kind'  AS kind,
		    affected_resources->0->>'name'  AS name,
		    affected_resources->0->>'namespace' AS ns
		 FROM incidents
		 WHERE tenant_id=$1 AND cluster_id=$2
		   AND (affected_resources->0->>'namespace') = $3
		   AND status IN ('open','acknowledged')
		   AND last_seen > now() - ($4 || ' seconds')::interval`,
		tenant, cluster, namespace, windowSec)
	if err != nil {
		return br, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, name, ns string
		if err := rows.Scan(&kind, &name, &ns); err != nil {
			return br, err
		}
		br.Resources = append(br.Resources, model.ResourceRef{Kind: kind, Name: name, Namespace: ns})
	}
	if err := rows.Err(); err != nil {
		return br, err
	}
	br.Services = len(br.Resources)
	br.Incidents = br.Services
	if br.Namespaces < 1 {
		br.Namespaces = 1
	}
	if br.Services < 1 {
		br.Services = 1
	}
	return br, nil
}

// SetIncidentBlastRadius 更新 incident 的 blast_radius 与 affected_resources(相关性聚合结果)。
func (s *Store) SetIncidentBlastRadius(ctx context.Context, id string, br BlastRadius) error {
	blast := map[string]any{"services": br.Services, "namespaces": br.Namespaces, "incidents": br.Incidents}
	_, err := s.pool.Exec(ctx,
		`UPDATE incidents SET blast_radius=$1, updated_at=now() WHERE incident_id=$2`,
		mustJSON(blast), id)
	return err
}

// CountActiveInvestigations 返回某租户当前活跃(非终态)调查数,用于并发上限(文档 6.3)。
func (s *Store) CountActiveInvestigations(ctx context.Context, tenant string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM investigations
		 WHERE tenant_id=$1 AND phase NOT IN ('closed','cancelled','concluded','needs_human','triage_published')`,
		tenant).Scan(&n)
	return n, err
}

var _ = time.Now
