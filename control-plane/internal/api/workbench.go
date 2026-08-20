package api

// 值班台的三个补齐端点:
//   GET  /v1/investigations           跨 incident 的调查队列
//   GET  /v1/audit                    审计日志(sre/admin)
//   POST /v1/incidents/{id}/status    认领 / 标记已解决
//
// 前两个是"写了但读不到"的补齐:investigations 表此前只能按 incident 查,
// audit_log 此前只能 psql 看。第三个是值班动作的缺口 —— 此前 incident 状态
// 只能由反馈的 close 动作间接改,没有"我在看了"这个中间态的入口,
// 于是同一个 P1 会有三个人同时开始排查。

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/aiops/control-plane/internal/auth"
	"github.com/aiops/control-plane/internal/httpx"
	"github.com/aiops/control-plane/internal/store"
)

// listInvestigations GET /v1/investigations?phase=collecting|planning&active=true&limit=50
func (a *PublicAPI) listInvestigations(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if !p.Can(auth.ActionReadIncident) {
		httpx.Error(w, http.StatusForbidden, "forbidden", "missing read permission")
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	// active 默认 false(列全部)。值班台首屏显式传 active=true。
	active := q.Get("active") == "true" || q.Get("active") == "1"

	items, err := a.store.ListInvestigations(r.Context(), q.Get("phase"), active, limit)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// ABAC:与 listIncidents 同口径逐行过滤。调查行携带了所属 incident 的
	// cluster/namespace,不需要二次查库。
	visible := make([]store.InvestigationListItem, 0, len(items))
	for _, it := range items {
		if auth.EffectiveAccess(p, a.agentScope, it.ClusterID, it.Namespace) {
			visible = append(visible, it)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"investigations": visible, "count": len(visible),
	})
}

// listAudit GET /v1/audit?actor=&action=&result=denied&hours=168&limit=100&before_id=
func (a *PublicAPI) listAudit(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if !p.Can(auth.ActionReadAudit) {
		httpx.Error(w, http.StatusForbidden, "forbidden", "missing permission for action")
		return
	}
	q := r.URL.Query()
	hours, _ := strconv.Atoi(q.Get("hours"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	beforeID, _ := strconv.ParseInt(q.Get("before_id"), 10, 64)

	result := strings.TrimSpace(q.Get("result"))
	switch result {
	case "", "ok", "denied", "error", "allowed":
	default:
		httpx.Error(w, http.StatusBadRequest, "bad_request",
			"result 只能是 ok / denied / error / allowed")
		return
	}

	entries, err := a.store.ListAudit(r.Context(), store.AuditFilter{
		Actor:      q.Get("actor"),
		Action:     q.Get("action"),
		TargetType: q.Get("target_type"),
		TargetID:   q.Get("target_id"),
		Result:     result,
		SinceHours: hours,
		Limit:      limit,
		BeforeID:   beforeID,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// 游标:返回本页最后一条的 id,前端下一页传 before_id。
	// 空页返回 0,前端据此判断"没有更多"。
	var nextCursor int64
	if len(entries) > 0 {
		nextCursor = entries[len(entries)-1].ID
	}
	// 概览条是增强,失败不影响列表。
	var counts []store.AuditActionCount
	if c, e := a.store.AuditActionCounts(r.Context(), hours); e == nil {
		counts = c
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"entries": entries, "count": len(entries),
		"next_cursor": nextCursor, "action_counts": counts,
	})
}

// updateIncidentStatus POST /v1/incidents/{id}/status  {"status":"acknowledged"}
//
// 只允许 acknowledged / resolved 两个目标态:
//   - open 是初始态,由 Signal 写入,不该由人"退回"(会让 first_seen 与
//     状态历史不一致,而值班交接依赖它)
//   - closed 走反馈的 close 动作 —— 关闭意味着"这次调查的结论我认了",
//     必须与反馈一起记录,否则评测集拿不到标注真值。
func (a *PublicAPI) updateIncidentStatus(w http.ResponseWriter, r *http.Request) {
	incidentID := r.PathValue("id")
	var body struct {
		Status string `json:"status"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	target := strings.TrimSpace(body.Status)
	switch target {
	case "acknowledged", "resolved":
	default:
		httpx.Error(w, http.StatusBadRequest, "bad_request",
			"status 只能是 acknowledged / resolved(关闭请通过调查反馈的 close 动作)")
		return
	}

	inc, err := a.store.GetIncident(r.Context(), incidentID)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}
	// RBAC + ABAC(写操作,fail-closed)
	if !a.authorizeIncident(w, r, inc, auth.ActionUpdateIncident) {
		return
	}
	// 已关闭的 incident 不再接受状态变更:closed 是终态,重新打开应当是
	// 一次新的 Signal 聚合(它会带新的 first_seen),而不是把旧记录改回去。
	if inc.Status == "closed" {
		httpx.Error(w, http.StatusConflict, "conflict",
			"incident 已关闭,不能再变更状态")
		return
	}
	if inc.Status == target {
		// 幂等:重复认领不报错,返回当前状态即可。两个值班人员同时点"认领"
		// 是常态,第二个人看到 409 会以为自己操作失败。
		httpx.JSON(w, http.StatusOK, map[string]any{
			"incident_id": incidentID, "status": inc.Status, "changed": false,
		})
		return
	}

	actor := "system"
	if p, ok := auth.FromContext(r.Context()); ok {
		actor = p.Subject
	}
	if err := a.store.SetIncidentStatus(r.Context(), incidentID, target); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	a.store.Audit(r.Context(), inc.TenantID, actor, "incident_status_change",
		"incident", incidentID, "ok",
		map[string]any{"cluster": inc.ClusterID},
		map[string]any{"from": inc.Status, "to": target})
	httpx.JSON(w, http.StatusOK, map[string]any{
		"incident_id": incidentID, "status": target, "changed": true,
	})
}
