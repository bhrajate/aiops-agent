package store

// 反馈闭环:人工反馈 → 待审 Golden Case → 审核入评测集。
//
// human_feedback 表从 000001 起就在收反馈,注释也写明"先进入审核队列,审核后才能
// 成为 Golden Case",但**从来没有实现那条通路**。反馈躺在表里,既不回流为评测用例,
// 也不改进 runbook —— 系统学不到任何东西。读取端(evaluation/store.py)一直按
// review_status='approved' 过滤,消费方早就准备好了,只缺生产方。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aiops/control-plane/internal/model"
)

// SourceHumanFeedback 标记由人工反馈自动提升而来的用例。
const SourceHumanFeedback = "human_feedback"

// GoldenCase 一条评测用例(仅含 API 需要的字段)。
type GoldenCase struct {
	CaseID          string         `json:"case_id"`
	TenantID        string         `json:"tenant_id"`
	IncidentID      string         `json:"incident_id,omitempty"`
	InvestigationID string         `json:"investigation_id,omitempty"`
	FaultCategory   string         `json:"fault_category"`
	RootCause       string         `json:"root_cause"`
	AffectedComp    string         `json:"affected_component,omitempty"`
	ExpectedCauses  []string       `json:"expected_top_causes"`
	SignalFixture   map[string]any `json:"signal_fixture,omitempty"`
	ReviewStatus    string         `json:"review_status"`
	Source          string         `json:"source"`
	PromotedBy      string         `json:"promoted_by,omitempty"`
	ReviewedBy      string         `json:"reviewed_by,omitempty"`
	ReviewedAt      *time.Time     `json:"reviewed_at,omitempty"`
	ReviewNote      string         `json:"review_note,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

// PromoteInvestigationToGoldenCase 把一次调查提升为**待审**评测用例。
//
// 只在人工反馈确认或纠正了根因时调用 —— 那一刻我们第一次拥有了"标注真值":
// 人说了根因是什么。这正是 Golden Case 需要的东西。
//
// 返回 (caseID, created)。created=false 表示该调查已提升过(幂等):
// 反馈可以来多次(先 correct 再 confirm),但它们描述的是同一次故障,
// 不该在评测集里占多个席位 —— 那等于给这次故障加权,而评测集的意义在于
// 覆盖多样的故障类型,不是重复计票。
//
// **一律写成 pending**,绝不直接 approved:评测集决定发布质量门槛,
// 一条错误标注的用例会让门槛失真,且这种失真很难发现(门槛照常通过或照常失败,
// 只是标准错了)。必须有人看过。
func (s *Store) PromoteInvestigationToGoldenCase(ctx context.Context,
	inv model.Investigation, inc model.Incident, rootCause, promotedBy string) (string, bool, error) {
	rootCause = strings.TrimSpace(rootCause)
	if rootCause == "" {
		// 没有标注真值就没有用例。confirm 动作若未填 confirmed_root_cause,
		// 退回用诊断结论的摘要 —— 由调用方决定,这里只拒绝空值。
		return "", false, fmt.Errorf("root cause 为空,无法作为评测用例的标注真值")
	}

	// signal_fixture 是回放输入:incident stub + 触发它的信号。
	// 不存整个 incident:它含 updated_at 等易变字段,会让回放结果不可复现。
	fixture := map[string]any{
		"incident": map[string]any{
			"incident_id":        inc.IncidentID,
			"version":            inc.Version,
			"status":             inc.Status,
			"severity":           inc.Severity,
			"title":              inc.Title,
			"fault_category":     inc.FaultCategory,
			"affected_resources": inc.AffectedResources,
			"blast_radius":       inc.BlastRadius,
			"topology_refs":      inc.TopologyRefs,
			"change_refs":        inc.ChangeRefs,
		},
	}
	sigs, err := s.signalStubsForIncident(ctx, inc.IncidentID)
	if err != nil {
		return "", false, fmt.Errorf("load signal fixture: %w", err)
	}
	fixture["signals"] = sigs

	affected := ""
	if len(inc.AffectedResources) > 0 {
		affected = model.WorkloadName(inc.AffectedResources[0])
	}
	// expected_top_causes 从标注根因切出关键词。刻意**不**用模型抽取:
	// 评测集的期望值必须是确定性的,否则评测结果会随抽取模型的版本漂移。
	expected := keywordsFrom(rootCause)

	var caseID string
	err = s.pool.QueryRow(ctx,
		`INSERT INTO golden_cases
		   (tenant_id, incident_id, investigation_id, fault_category, root_cause,
		    affected_component, signal_fixture, expected_top_causes,
		    review_status, source, promoted_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10)
		 ON CONFLICT (investigation_id) WHERE investigation_id IS NOT NULL
		 DO NOTHING
		 RETURNING case_id`,
		inv.TenantID, inc.IncidentID, inv.InvestigationID,
		faultCategoryOrUnknown(inc.FaultCategory), rootCause, affected,
		mustJSON(fixture), mustJSON(expected), SourceHumanFeedback, promotedBy).Scan(&caseID)
	if err != nil {
		// ON CONFLICT DO NOTHING 时 RETURNING 无行 —— 说明已提升过。
		var existing string
		if e := s.pool.QueryRow(ctx,
			`SELECT case_id FROM golden_cases WHERE investigation_id = $1`,
			inv.InvestigationID).Scan(&existing); e == nil {
			return existing, false, nil
		}
		return "", false, err
	}
	return caseID, true, nil
}

// signalStubsForIncident 取该 incident 下的信号存根(供回放)。
//
// 只取回放必需的字段,且限制条数:一个告警风暴产生的 incident 可能有上千条信号,
// 全存进 fixture 会让评测用例膨胀到无法处理,而回放只需要代表性的输入。
func (s *Store) signalStubsForIncident(ctx context.Context, incidentID string) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT signal_id, cluster_id, source, signal_type, severity, labels
		   FROM signals WHERE incident_id = $1
		  ORDER BY received_at LIMIT 20`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0, 8)
	for rows.Next() {
		var id, cluster, src, styp string
		var sev *string
		var labels []byte
		if err := rows.Scan(&id, &cluster, &src, &styp, &sev, &labels); err != nil {
			return nil, err
		}
		var lm map[string]string
		_ = json.Unmarshal(labels, &lm)
		stub := map[string]any{
			"signal_id": id, "cluster_id": cluster,
			"source": src, "signal_type": styp, "labels": lm,
		}
		if sev != nil {
			stub["severity"] = *sev
		}
		out = append(out, stub)
	}
	return out, rows.Err()
}

// ListGoldenCases 按审核状态列出用例(待审队列 / 已批准集)。
func (s *Store) ListGoldenCases(ctx context.Context, tenantID, reviewStatus string, limit int) ([]GoldenCase, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT case_id, tenant_id, COALESCE(incident_id,''), COALESCE(investigation_id,''),
		        fault_category, root_cause, COALESCE(affected_component,''),
		        expected_top_causes, review_status, source,
		        COALESCE(promoted_by,''), COALESCE(reviewed_by,''), reviewed_at,
		        COALESCE(review_note,''), created_at
		   FROM golden_cases
		  WHERE tenant_id = $1 AND ($2 = '' OR review_status = $2)
		  ORDER BY created_at DESC LIMIT $3`, tenantID, reviewStatus, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]GoldenCase, 0, limit)
	for rows.Next() {
		var c GoldenCase
		var expected []byte
		if err := rows.Scan(&c.CaseID, &c.TenantID, &c.IncidentID, &c.InvestigationID,
			&c.FaultCategory, &c.RootCause, &c.AffectedComp, &expected,
			&c.ReviewStatus, &c.Source, &c.PromotedBy, &c.ReviewedBy, &c.ReviewedAt,
			&c.ReviewNote, &c.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(expected, &c.ExpectedCauses)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReviewGoldenCase 审核一条用例。status 只接受 approved / rejected。
//
// 不允许把已审核的改回 pending:审核是一次决定,反复翻转会让"评测集当前包含什么"
// 变得不可知。要改判就 reject 后新建。
func (s *Store) ReviewGoldenCase(ctx context.Context, caseID, status, reviewer, note string) error {
	switch status {
	case "approved", "rejected":
	default:
		return fmt.Errorf("review status 只能是 approved 或 rejected,得到 %q", status)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE golden_cases
		    SET review_status = $1, reviewed_by = $2, reviewed_at = now(), review_note = $3
		  WHERE case_id = $4 AND review_status = 'pending'`,
		status, reviewer, note, caseID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("用例不存在或已审核过(审核是一次决定,不可翻转)")
	}
	return nil
}

// CountGoldenCases 按状态计数(供指标)。
func (s *Store) CountGoldenCases(ctx context.Context, reviewStatus string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM golden_cases WHERE review_status = $1`, reviewStatus).Scan(&n)
	return n, err
}

// keywordsFrom 从标注根因切出期望关键词。
//
// 确定性切分,不用模型:评测集的期望值必须稳定,否则评测结果会随抽取模型的版本
// 漂移 —— 那会让"这次发布是否退化"这个问题失去意义(分不清是模型退化还是标准变了)。
//
// 规则简单但够用:按常见分隔符切,去掉过短的片段(单字/虚词),最多取 5 个。
// 审核环节本来就要人看一遍,可以顺手改。
func keywordsFrom(rootCause string) []string {
	// 分隔符集合。中文全角标点用 Unicode 码点写死,不写字面量 ——
	// 字面量在编辑/传输链路上可能被规范化成半角,与前面的半角 case 撞成
	// "duplicate case"(编译期就抓到过一次)。
	seps := func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ',', ';', ':', '(', ')', '/':
			return true
		case '\u3002', // 。 句号
			'\uff0c', // ,全角逗号
			'\uff1b', // ;全角分号
			'\uff1a', // :全角冒号
			'\u3001', // 、顿号
			'\uff08', // (全角左括号
			'\uff09': // )全角右括号
			return true
		}
		return false
	}
	seen := map[string]bool{}
	out := make([]string, 0, 5)
	for _, tok := range strings.FieldsFunc(rootCause, seps) {
		t := strings.TrimSpace(tok)
		// 长度阈值按 rune 数算:中文根因("连接池耗尽")按字节会误判为够长,
		// 而英文虚词("of")按字节又刚好卡在边界上。
		if len([]rune(t)) < 2 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) == 5 {
			break
		}
	}
	if len(out) == 0 {
		// 兜底:整条根因作为唯一关键词。空的 expected_top_causes 会让该用例
		// 在评测里恒判命中(没有期望就无法不命中),那比没有用例更糟。
		out = append(out, strings.TrimSpace(rootCause))
	}
	return out
}

// faultCategoryOrUnknown 保证 fault_category 非空(表上是 NOT NULL)。
// 用独立名字而非复用 store.orDefault:后者是单参数的空串兜底,语义不同。
func faultCategoryOrUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
