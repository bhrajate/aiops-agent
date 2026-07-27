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
