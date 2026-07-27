package store

import (
	"context"

	"github.com/aiops/control-plane/internal/model"
)

// 孤儿调查对账(A2):
// CreateInvestigation(phase=queued)与 Temporal wf.Start 不是同一个原子操作。
// 两者之间进程崩溃/被杀,会留下一条永远停在 queued、没有 run_id 的调查——
// 既不会被 Temporal 推进,也没人重试,值班人员看到"调查已创建"却永无结果。
//
// 这里提供对账所需的查询:找出超过宽限期仍未拿到 run_id 的 queued 调查。

// OrphanInvestigation 一条待补偿的孤儿调查。
type OrphanInvestigation struct {
	InvestigationID string
	IncidentID      string
	IncidentVersion int
	TenantID        string
	WorkflowID      string
	Budget          model.Budget
}

// FindOrphanInvestigations 返回 phase=queued、run_id 为空、且创建超过
// graceSec 秒的调查(宽限期避免误抓"刚创建、正在启动工作流"的调查)。
func (s *Store) FindOrphanInvestigations(ctx context.Context, graceSec, limit int) ([]OrphanInvestigation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT investigation_id, incident_id, incident_version, tenant_id,
		        COALESCE(workflow_id,''), budget
		   FROM investigations
		  WHERE phase = 'queued'
		    AND COALESCE(run_id,'') = ''
		    AND started_at < now() - make_interval(secs => $1::double precision)
		  ORDER BY started_at
		  LIMIT $2`, graceSec, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrphanInvestigation
	for rows.Next() {
		var o OrphanInvestigation
		var budgetRaw []byte
		if err := rows.Scan(&o.InvestigationID, &o.IncidentID, &o.IncidentVersion,
			&o.TenantID, &o.WorkflowID, &budgetRaw); err != nil {
			return nil, err
		}
		_ = jsonUnmarshal(budgetRaw, &o.Budget)
		out = append(out, o)
	}
	return out, rows.Err()
}

// MarkInvestigationFailed 在补偿彻底失败时把调查置为终态,避免永久悬挂。
func (s *Store) MarkInvestigationFailed(ctx context.Context, id, reason string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE investigations
		    SET phase='cancelled', ended_at=COALESCE(ended_at, now())
		  WHERE investigation_id=$1`, id)
	if err != nil {
		return err
	}
	_, _ = s.AppendEvent(ctx, id, "phase_changed",
		map[string]any{"phase": "cancelled", "reason": reason})
	return nil
}
