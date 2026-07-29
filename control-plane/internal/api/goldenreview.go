package api

// 评测用例的审核端点(反馈闭环的第二步)。
//
// 通路是:人工反馈(confirm/correct)→ 自动提升为 **pending** 用例 → 人工审核
// → approved 后进入评测集(evaluation/store.py 按 approved 过滤)。
//
// 为什么中间要有人:评测集决定发布质量门槛。一条错误标注的用例会让门槛失真,
// 而这种失真极难发现 —— 门槛照常通过或照常失败,只是标准错了。
// 自动提升省掉的是"从头写一条用例"的工作量,不是"确认它对不对"的责任。

import (
	"net/http"
	"strconv"

	"github.com/aiops/control-plane/internal/auth"
	"github.com/aiops/control-plane/internal/httpx"
)

// listGoldenCases GET /v1/golden-cases?status=pending&limit=50
//
// 默认只列 pending:这个端点的主要用途是"待审队列有什么"。
// 要看已批准集传 status=approved,传空字符串列全部。
func (a *PublicAPI) listGoldenCases(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if !p.Can(auth.ActionReviewGolden) {
		httpx.Error(w, http.StatusForbidden, "forbidden", "missing permission for action")
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	if status == "all" {
		status = "" // store 层用空串表示不过滤
	}
	switch status {
	case "", "pending", "approved", "rejected":
	default:
		httpx.Error(w, http.StatusBadRequest, "bad_request",
			"status 只能是 pending / approved / rejected / all")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	cases, err := a.store.ListGoldenCases(r.Context(), a.tenant, status, limit)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"golden_cases": cases, "status": status, "count": len(cases),
	})
}

// reviewGoldenCase POST /v1/golden-cases/{id}/review  {"status":"approved","note":"..."}
func (a *PublicAPI) reviewGoldenCase(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if !p.Can(auth.ActionReviewGolden) {
		a.store.Audit(r.Context(), a.tenant, p.Subject, "review_golden_case",
			"golden_case", r.PathValue("id"), "denied", nil,
			map[string]any{"reason": "missing_permission"})
		httpx.Error(w, http.StatusForbidden, "forbidden", "missing permission for action")
		return
	}
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	caseID := r.PathValue("id")
	// reviewer 以认证身份为准,不信任 body —— 审核记录是问责依据。
	if err := a.store.ReviewGoldenCase(r.Context(), caseID, body.Status, p.Subject, body.Note); err != nil {
		// 状态非法与"已审核过"都是调用方问题,返 400 而非 500。
		httpx.Error(w, http.StatusBadRequest, "invalid_review", err.Error())
		return
	}
	a.store.Audit(r.Context(), a.tenant, p.Subject, "review_golden_case",
		"golden_case", caseID, "ok", nil,
		map[string]any{"status": body.Status, "note": body.Note})
	a.log.Info("golden case reviewed", "case_id", caseID,
		"status", body.Status, "reviewer", p.Subject)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"case_id": caseID, "review_status": body.Status, "reviewed_by": p.Subject,
	})
}
