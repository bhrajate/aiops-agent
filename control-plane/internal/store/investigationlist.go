package store

// 跨 incident 的调查队列查询。
//
// 此前只能按 incident 查调查(ListInvestigationsByIncident),于是"现在有哪些调查
// 卡住了"只能靠人挨个点开 incident 看 —— 而卡住的调查恰恰不会有人主动去看,
// 因为它对应的 incident 界面上一切正常。值班台需要一个"所有调查"的视图。

import (
	"context"
	"strings"

	"github.com/aiops/control-plane/internal/model"
)

// InvestigationListItem 是列表行:调查本体 + 所属 incident 的展示与 ABAC 维度。
//
// 内嵌 incident 字段而不是让前端二次查询:一个 20 行的列表会变成 20 个请求,
// 且前端拼装时若某个 incident 查询失败,那一行会**静默显示为空**而非报错。
type InvestigationListItem struct {
	model.Investigation
	ClusterID       string `json:"cluster_id"`
	Namespace       string `json:"namespace,omitempty"`
	IncidentTitle   string `json:"incident_title,omitempty"`
	IncidentSev     string `json:"incident_severity,omitempty"`
	IncidentStatus  string `json:"incident_status,omitempty"`
	FaultCategory   string `json:"fault_category,omitempty"`
	EvidenceCount   int    `json:"evidence_count"`
	HypothesisCount int    `json:"hypothesis_count"`
}

// ListInvestigations 按 phase 过滤列出调查(跨 incident),最新优先。
//
// phase 支持竖线分隔的多值(如 "collecting|planning"),空串表示不过滤。
// active 为 true 时只返回非终态 —— 值班台默认视图,回答"现在有什么在跑"。
func (s *Store) ListInvestigations(ctx context.Context, phase string, active bool,
	limit int) ([]InvestigationListItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	// phase 是枚举,来自查询串。不拼接进 SQL —— 用数组参数 + = ANY,
	// 空数组时靠 cardinality 判断跳过该条件。
	phases := []string{}
	for _, p := range strings.Split(phase, "|") {
		if p = strings.TrimSpace(p); p != "" {
			phases = append(phases, p)
		}
	}
	rows, err := s.pool.Query(ctx,
		`SELECT iv.investigation_id, iv.tenant_id, iv.incident_id, iv.incident_version,
		        COALESCE(iv.workflow_id,''), COALESCE(iv.run_id,''), iv.phase,
		        COALESCE(iv.trigger_reason,''), COALESCE(iv.triggered_by,''),
		        iv.budget, iv.usage, COALESCE(iv.model_version,''),
		        COALESCE(iv.prompt_version,''), COALESCE(iv.policy_version,''),
		        iv.diagnosis, iv.started_at, iv.ended_at,
		        inc.cluster_id,
		        COALESCE(inc.affected_resources->0->>'namespace',''),
		        inc.title, inc.severity, inc.status, COALESCE(inc.fault_category,''),
		        (SELECT count(*) FROM evidence e WHERE e.investigation_id = iv.investigation_id),
		        (SELECT count(*) FROM hypotheses h WHERE h.investigation_id = iv.investigation_id)
		   FROM investigations iv
		   JOIN incidents inc ON inc.incident_id = iv.incident_id
		  WHERE (cardinality($1::text[]) = 0 OR iv.phase = ANY($1::text[]))
		    AND (NOT $2 OR iv.phase NOT IN ('closed','cancelled','concluded','needs_human','triage_published'))
		  ORDER BY iv.started_at DESC
		  LIMIT $3`, phases, active, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]InvestigationListItem, 0, 32)
	for rows.Next() {
		var it InvestigationListItem
		var budget, usage, diagnosis []byte
		if err := rows.Scan(&it.InvestigationID, &it.TenantID, &it.IncidentID,
			&it.IncidentVersion, &it.WorkflowID, &it.RunID, &it.Phase,
			&it.TriggerReason, &it.TriggeredBy, &budget, &usage, &it.ModelVersion,
			&it.PromptVersion, &it.PolicyVersion, &diagnosis, &it.StartedAt, &it.EndedAt,
			&it.ClusterID, &it.Namespace, &it.IncidentTitle, &it.IncidentSev,
			&it.IncidentStatus, &it.FaultCategory,
			&it.EvidenceCount, &it.HypothesisCount); err != nil {
			return nil, err
		}
		unmarshalInvestigationJSON(&it.Investigation, budget, usage, diagnosis)
		out = append(out, it)
	}
	return out, rows.Err()
}
