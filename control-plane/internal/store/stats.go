package store

// 值班总览的聚合读取。
//
// 为什么单独一层而不是让前端拿 incident 列表现算:列表接口有 limit(默认 50、
// 上限 500),前端算出的"P1 有几个"其实是"最近 50 条里 P1 有几个"。故障风暴时
// 这两个数会差一个量级,而**看起来完全正常** —— 这是最难发现的一类错:
// 数字在那里、也在变,只是答的不是你问的问题。
//
// 聚合在 SQL 里做,但 ABAC 过滤不能在 SQL 里做(scope 是 Principal 上的集合,
// 不是列)。所以这里返回**带 cluster/namespace 的明细行**,由 API 层按
// EffectiveAccess 逐行过滤后再累加。牺牲一点带宽换取"总览与列表口径一致":
// 若两处过滤逻辑不同,值班人员会看到总览说有 3 个 P1、点进去只有 1 个。

import (
	"context"
	"time"
)

// IncidentStatRow 是参与统计的 incident 精简行(只含分组维度与 ABAC 维度)。
type IncidentStatRow struct {
	IncidentID    string
	ClusterID     string
	Namespace     string // 取 affected_resources[0].namespace,与 API 层 ABAC 口径一致
	Status        string
	Severity      string
	FaultCategory string
	FirstSeen     time.Time
	LastSeen      time.Time
	ResolvedAt    *time.Time
	ClosedAt      *time.Time
	SignalCount   int
}

// IncidentStatRows 拉取用于统计的 incident 明细。
//
// sinceHours 只约束**趋势与耗时**统计的时间窗,不约束"当前未闭环"计数 ——
// 一个 3 天前爆发、至今没人处理的 P1 必须出现在值班总览上。把它按 24h 窗口
// 过滤掉,恰好隐藏了最该被看见的那类故障。故这里返回两部分的并集:
// 窗口内有活动的 + 任何时间点仍未闭环的。
func (s *Store) IncidentStatRows(ctx context.Context, sinceHours int) ([]IncidentStatRow, error) {
	if sinceHours <= 0 || sinceHours > 24*90 {
		sinceHours = 24
	}
	rows, err := s.pool.Query(ctx,
		`SELECT incident_id, cluster_id,
		        COALESCE(affected_resources->0->>'namespace', '') AS namespace,
		        status, severity, COALESCE(fault_category,''),
		        first_seen, last_seen, resolved_at, closed_at, signal_count
		   FROM incidents
		  WHERE last_seen >= now() - make_interval(hours => $1)
		     OR status IN ('open','acknowledged')
		     -- 第三个条件不是冗余的:SetIncidentStatus 写 resolved_at/closed_at 时
		     -- **不动 last_seen**。于是"信号早就停了、人过了很久才去解决"的长尾故障
		     -- 前两个条件都不满足 —— 它从 MTTR 样本与趋势的 resolved 序列里消失,
		     -- 不报错也不记日志,读起来只是"我们解决得很快"。
		     -- 而 MTTR 存在的意义恰恰是度量长尾。见 stats_db_test.go。
		     OR COALESCE(resolved_at, closed_at) >= now() - make_interval(hours => $1)
		  ORDER BY last_seen DESC
		  LIMIT 5000`, sinceHours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]IncidentStatRow, 0, 128)
	for rows.Next() {
		var r IncidentStatRow
		if err := rows.Scan(&r.IncidentID, &r.ClusterID, &r.Namespace, &r.Status,
			&r.Severity, &r.FaultCategory, &r.FirstSeen, &r.LastSeen,
			&r.ResolvedAt, &r.ClosedAt, &r.SignalCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InvestigationStatRow 是参与统计的调查精简行,带所属 incident 的 ABAC 维度。
type InvestigationStatRow struct {
	InvestigationID string
	IncidentID      string
	ClusterID       string
	Namespace       string
	Phase           string
	TriggerReason   string
	TriggeredBy     string
	DiagnosisStatus string // diagnosis->>'status',无诊断为空串
	CostUSD         float64
	Tokens          int
	ToolCalls       int
	ElapsedSec      float64
	StartedAt       time.Time
	EndedAt         *time.Time
}

// InvestigationStatRows 拉取用于统计的调查明细(JOIN incident 取 ABAC 维度)。
//
// 与 IncidentStatRows 同理:窗口内启动的 + 任何时间点仍在进行的。
// 一次卡在 collecting 两天的调查是**故障信号**,不该因为超出窗口而消失。
func (s *Store) InvestigationStatRows(ctx context.Context, sinceHours int) ([]InvestigationStatRow, error) {
	if sinceHours <= 0 || sinceHours > 24*90 {
		sinceHours = 24
	}
	rows, err := s.pool.Query(ctx,
		`SELECT iv.investigation_id, iv.incident_id, inc.cluster_id,
		        COALESCE(inc.affected_resources->0->>'namespace', '') AS namespace,
		        iv.phase, COALESCE(iv.trigger_reason,''), COALESCE(iv.triggered_by,''),
		        COALESCE(iv.diagnosis->>'status',''),
		        COALESCE((iv.usage->>'cost_usd')::double precision, 0),
		        COALESCE((iv.usage->>'tokens')::int, 0),
		        COALESCE((iv.usage->>'tool_calls')::int, 0),
		        COALESCE((iv.usage->>'elapsed_sec')::double precision, 0),
		        iv.started_at, iv.ended_at
		   FROM investigations iv
		   JOIN incidents inc ON inc.incident_id = iv.incident_id
		  WHERE iv.started_at >= now() - make_interval(hours => $1)
		     OR iv.phase NOT IN ('closed','cancelled','concluded','needs_human','triage_published')
		  ORDER BY iv.started_at DESC
		  LIMIT 5000`, sinceHours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]InvestigationStatRow, 0, 128)
	for rows.Next() {
		var r InvestigationStatRow
		if err := rows.Scan(&r.InvestigationID, &r.IncidentID, &r.ClusterID, &r.Namespace,
			&r.Phase, &r.TriggerReason, &r.TriggeredBy, &r.DiagnosisStatus,
			&r.CostUSD, &r.Tokens, &r.ToolCalls, &r.ElapsedSec,
			&r.StartedAt, &r.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FeedbackActionCount 一种反馈动作的计数。
type FeedbackActionCount struct {
	Action string `json:"action"`
	Count  int    `json:"count"`
}

// FeedbackActionCounts 按动作统计人工反馈(F10 采纳率的分子分母)。
//
// 不返回比率:采纳率 = confirm / sum(confirm,correct,reject),固化成一个数会丢掉
// 分子分母,而"低采纳率"与"根本没人给反馈"是完全不同的问题 —— 前者是模型不准,
// 后者是流程没跑起来,处置方式相反。
func (s *Store) FeedbackActionCounts(ctx context.Context, sinceHours int) ([]FeedbackActionCount, error) {
	if sinceHours <= 0 || sinceHours > 24*365 {
		sinceHours = 24 * 7
	}
	rows, err := s.pool.Query(ctx,
		`SELECT action, count(*) FROM human_feedback
		  WHERE created_at >= now() - make_interval(hours => $1)
		  GROUP BY action ORDER BY count(*) DESC`, sinceHours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]FeedbackActionCount, 0, 4)
	for rows.Next() {
		var c FeedbackActionCount
		if err := rows.Scan(&c.Action, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EvidenceTypeCount 按证据类型的计数。
type EvidenceTypeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// EvidenceTypeCounts 统计证据类型分布(看 Agent 实际用了哪些数据源)。
func (s *Store) EvidenceTypeCounts(ctx context.Context, sinceHours int) ([]EvidenceTypeCount, error) {
	if sinceHours <= 0 || sinceHours > 24*365 {
		sinceHours = 24 * 7
	}
	rows, err := s.pool.Query(ctx,
		`SELECT type, count(*) FROM evidence
		  WHERE created_at >= now() - make_interval(hours => $1)
		  GROUP BY type ORDER BY count(*) DESC`, sinceHours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]EvidenceTypeCount, 0, 6)
	for rows.Next() {
		var c EvidenceTypeCount
		if err := rows.Scan(&c.Type, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
