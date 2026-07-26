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

var _ = time.Now
