package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aiops/control-plane/internal/model"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateInvestigation(ctx context.Context, inv model.Investigation) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO investigations
			 (investigation_id, tenant_id, incident_id, incident_version, workflow_id,
			  phase, trigger_reason, triggered_by, budget, usage, policy_version)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			inv.InvestigationID, inv.TenantID, inv.IncidentID, inv.IncidentVersion,
			inv.WorkflowID, inv.Phase, inv.TriggerReason, inv.TriggeredBy,
			mustJSON(inv.Budget), mustJSON(inv.Usage), inv.PolicyVersion)
		if err != nil {
			return err
		}
		return EnqueueOutboxTx(ctx, tx, "investigations", inv.InvestigationID, inv)
	})
}

const investigationSelect = `SELECT investigation_id, tenant_id, incident_id, incident_version,
	COALESCE(workflow_id,''), COALESCE(run_id,''), phase, COALESCE(trigger_reason,''),
	COALESCE(triggered_by,''), budget, usage, COALESCE(model_version,''),
	COALESCE(prompt_version,''), COALESCE(policy_version,''), diagnosis, started_at, ended_at
	FROM investigations`

func scanInvestigation(row rowScanner) (model.Investigation, error) {
	var inv model.Investigation
	var budget, usage, diagnosis []byte
	err := row.Scan(&inv.InvestigationID, &inv.TenantID, &inv.IncidentID, &inv.IncidentVersion,
		&inv.WorkflowID, &inv.RunID, &inv.Phase, &inv.TriggerReason, &inv.TriggeredBy,
		&budget, &usage, &inv.ModelVersion, &inv.PromptVersion, &inv.PolicyVersion,
		&diagnosis, &inv.StartedAt, &inv.EndedAt)
	if err != nil {
		return inv, err
	}
	unmarshalInvestigationJSON(&inv, budget, usage, diagnosis)
	return inv, nil
}

// unmarshalInvestigationJSON 解开 investigation 的三个 JSONB 列。
// 抽出来供 ListInvestigations 复用 —— 它的 SELECT 多带了 incident 字段,
// 没法走 scanInvestigation,但 diagnosis 的 "null" 处理必须与这里一致:
// 漏掉那个判断会把 JSON null 解成零值 DiagnosisResult,前端看到的是
// "status 为空的诊断"而不是"还没有诊断"。
func unmarshalInvestigationJSON(inv *model.Investigation, budget, usage, diagnosis []byte) {
	_ = json.Unmarshal(budget, &inv.Budget)
	_ = json.Unmarshal(usage, &inv.Usage)
	if len(diagnosis) > 0 && string(diagnosis) != "null" {
		var d model.DiagnosisResult
		if json.Unmarshal(diagnosis, &d) == nil {
			inv.Diagnosis = &d
		}
	}
}

func (s *Store) GetInvestigation(ctx context.Context, id string) (model.Investigation, error) {
	row := s.pool.QueryRow(ctx, investigationSelect+` WHERE investigation_id=$1`, id)
	inv, err := scanInvestigation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return inv, ErrNotFound
	}
	return inv, err
}

func (s *Store) ListInvestigationsByIncident(ctx context.Context, incidentID string) ([]model.Investigation, error) {
	rows, err := s.pool.Query(ctx, investigationSelect+` WHERE incident_id=$1 ORDER BY started_at DESC`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Investigation
	for rows.Next() {
		inv, err := scanInvestigation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *Store) SetInvestigationWorkflow(ctx context.Context, id, workflowID, runID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE investigations SET workflow_id=$1, run_id=$2 WHERE investigation_id=$3`,
		workflowID, runID, id)
	return err
}

func (s *Store) SetInvestigationPhase(ctx context.Context, id, phase string) error {
	terminal := phase == "closed" || phase == "cancelled" || phase == "concluded" ||
		phase == "triage_published" || phase == "needs_human"
	q := `UPDATE investigations SET phase=$1`
	if terminal {
		q += `, ended_at=COALESCE(ended_at, now())`
	}
	q += ` WHERE investigation_id=$2`
	_, err := s.pool.Exec(ctx, q, phase, id)
	return err
}

func (s *Store) SetInvestigationUsage(ctx context.Context, id string, usage model.Usage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE investigations SET usage=$1 WHERE investigation_id=$2`, mustJSON(usage), id)
	return err
}

func (s *Store) SetInvestigationDiagnosis(ctx context.Context, id string, d model.DiagnosisResult, phase string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE investigations SET diagnosis=$1, phase=$2, ended_at=COALESCE(ended_at, now())
		 WHERE investigation_id=$3`,
		mustJSON(d), phase, id)
	return err
}

func (s *Store) SetInvestigationModelMeta(ctx context.Context, id, modelVersion, promptVersion string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE investigations SET model_version=$1, prompt_version=$2 WHERE investigation_id=$3`,
		modelVersion, promptVersion, id)
	return err
}
