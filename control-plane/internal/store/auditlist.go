package store

// 审计日志的读取端。
//
// audit_log 从 000001 起就在写(权限拒绝、工具调用、反馈、用例审核),但**没有任何
// 读取入口** —— 只能登进数据库 psql。这让审计事实上不可用:出了越权访问,值班人员
// 没有任何界面能回答"谁在什么时候动了什么"。写而不读的审计等于没有审计。

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// AuditEntry 一条审计记录。
type AuditEntry struct {
	ID         int64          `json:"id"`
	TenantID   string         `json:"tenant_id"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type,omitempty"`
	TargetID   string         `json:"target_id,omitempty"`
	Scope      map[string]any `json:"scope,omitempty"`
	Result     string         `json:"result,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// AuditFilter 审计查询条件。零值表示不过滤。
type AuditFilter struct {
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	Result     string // ok / denied / error
	SinceHours int
	Limit      int
	// BeforeID 游标翻页:返回 id < BeforeID 的记录。
	// 用 id 而非 OFFSET —— 审计表持续写入,OFFSET 翻页会在新记录插入时
	// 漏掉或重复行,而这是问责依据,不能有"翻页时少了一条"。
	BeforeID int64
}

// ListAudit 按条件查询审计日志,最新优先。
func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	since := f.SinceHours
	if since <= 0 || since > 24*365 {
		since = 24 * 7
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, tenant_id, actor, action, COALESCE(target_type,''), COALESCE(target_id,''),
		        scope, COALESCE(result,''), detail, created_at
		   FROM audit_log
		  WHERE ($1='' OR actor = $1)
		    AND ($2='' OR action = $2)
		    AND ($3='' OR target_type = $3)
		    AND ($4='' OR target_id = $4)
		    AND ($5='' OR result = $5)
		    AND created_at >= now() - make_interval(hours => $6)
		    AND ($7 = 0 OR id < $7)
		  ORDER BY id DESC
		  LIMIT $8`,
		strings.TrimSpace(f.Actor), strings.TrimSpace(f.Action),
		strings.TrimSpace(f.TargetType), strings.TrimSpace(f.TargetID),
		strings.TrimSpace(f.Result), since, f.BeforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditEntry, 0, 32)
	for rows.Next() {
		var e AuditEntry
		var scope, detail []byte
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Actor, &e.Action, &e.TargetType,
			&e.TargetID, &scope, &e.Result, &detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		if len(scope) > 0 {
			_ = json.Unmarshal(scope, &e.Scope)
		}
		if len(detail) > 0 {
			_ = json.Unmarshal(detail, &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AuditActionCount 按动作聚合的审计计数(供审计页的概览条)。
type AuditActionCount struct {
	Action string `json:"action"`
	Result string `json:"result"`
	Count  int    `json:"count"`
}

// AuditActionCounts 按 (action, result) 聚合。
//
// 带上 result 维度:`denied` 的数量单独可见才有意义 —— "有 40 次权限拒绝"
// 是安全信号,混在 action 总数里就看不见了。
func (s *Store) AuditActionCounts(ctx context.Context, sinceHours int) ([]AuditActionCount, error) {
	if sinceHours <= 0 || sinceHours > 24*365 {
		sinceHours = 24 * 7
	}
	rows, err := s.pool.Query(ctx,
		`SELECT action, COALESCE(result,''), count(*)
		   FROM audit_log
		  WHERE created_at >= now() - make_interval(hours => $1)
		  GROUP BY action, result
		  ORDER BY count(*) DESC
		  LIMIT 50`, sinceHours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditActionCount, 0, 16)
	for rows.Next() {
		var c AuditActionCount
		if err := rows.Scan(&c.Action, &c.Result, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
