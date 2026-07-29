package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aiops/control-plane/internal/auth"
	"github.com/aiops/control-plane/internal/httpx"
	"github.com/aiops/control-plane/internal/model"
)

// streamEvents 通过 SSE 推送调查时间线(文档 17.2)。数据库是事实源,SSE 只做增量推送。
func (a *PublicAPI) streamEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inv, err := a.store.GetInvestigation(r.Context(), id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "investigation not found")
		return
	}
	// 授权(修复 IDOR):SSE 时间线含证据/假设/诊断,必须与 getInvestigation 一致做
	// RBAC + ABAC。fail-closed:incident 读取失败则拒绝,不开流。
	inc, e := a.store.GetIncident(r.Context(), inv.IncidentID)
	if e != nil {
		httpx.Error(w, http.StatusForbidden, "forbidden", "cannot resolve incident scope")
		return
	}
	if !a.authorizeIncident(w, r, inc, auth.ActionReadIncident) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "no_stream", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ctx := r.Context()
	lastSeq := 0
	// 首帧:立即回放已有事件
	send := func(evs []model.InvestigationEvent) {
		for _, e := range evs {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.EventType, b)
			lastSeq = e.Seq
		}
		flusher.Flush()
	}
	if evs, err := a.store.EventsSince(ctx, id, 0); err == nil {
		send(evs)
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case <-ticker.C:
			evs, err := a.store.EventsSince(ctx, id, lastSeq)
			if err != nil {
				continue
			}
			if len(evs) > 0 {
				send(evs)
			}
			// 若调查已终态且无新事件,发送 done 并结束
			inv, err := a.store.GetInvestigation(ctx, id)
			if err == nil && isTerminal(inv.Phase) && len(evs) == 0 {
				fmt.Fprintf(w, "event: done\ndata: {\"phase\":%q}\n\n", inv.Phase)
				flusher.Flush()
				return
			}
		}
	}
}

func isTerminal(phase string) bool {
	switch phase {
	case "closed", "cancelled", "concluded", "triage_published", "needs_human":
		return true
	}
	return false
}

// cancelInvestigation 取消调查:向 Temporal 发 Cancel,并更新 DB(事实源)。
func (a *PublicAPI) cancelInvestigation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inv, err := a.store.GetInvestigation(r.Context(), id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "investigation not found")
		return
	}
	// fail-closed:无法解析 incident 范围则拒绝(取消是写操作,不可 fail-open)
	inc, e := a.store.GetIncident(r.Context(), inv.IncidentID)
	if e != nil {
		httpx.Error(w, http.StatusForbidden, "forbidden", "cannot resolve incident scope")
		return
	}
	if !a.authorizeIncident(w, r, inc, auth.ActionCancelInvestig) {
		return
	}
	if a.tempo != nil && inv.WorkflowID != "" {
		if err := a.tempo.Cancel(r.Context(), inv.WorkflowID); err != nil {
			a.log.Warn("temporal cancel failed", "workflow", inv.WorkflowID, "err", err)
		}
	}
	if err := a.store.SetInvestigationPhase(r.Context(), id, "cancelled"); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	actor := "system"
	if p, ok := auth.FromContext(r.Context()); ok {
		actor = p.Subject
	}
	_, _ = a.store.AppendEvent(r.Context(), id, "phase_changed", map[string]any{"phase": "cancelled", "by": actor})
	a.store.Audit(r.Context(), inv.TenantID, actor, "investigation_cancel", "investigation", id, "ok", nil, nil)
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// postFeedback 记录人工反馈,并向工作流发 HumanFeedback 信号。close 动作同时关闭 Incident。
func (a *PublicAPI) postFeedback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Author             string `json:"author"`
		Action             string `json:"action"`
		ConfirmedRootCause string `json:"confirmed_root_cause"`
		Comment            string `json:"comment"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	if p, ok := auth.FromContext(r.Context()); ok {
		body.Author = p.Subject // 反馈作者以认证身份为准,不信任 body
	}
	inv, err := a.store.GetInvestigation(r.Context(), id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "investigation not found")
		return
	}
	// fail-closed:反馈可 close incident,是写操作,不可 fail-open
	inc, e := a.store.GetIncident(r.Context(), inv.IncidentID)
	if e != nil {
		httpx.Error(w, http.StatusForbidden, "forbidden", "cannot resolve incident scope")
		return
	}
	if !a.authorizeIncident(w, r, inc, auth.ActionFeedback) {
		return
	}
	fb, err := a.store.InsertFeedback(r.Context(), model.Feedback{
		InvestigationID:    id,
		Author:             body.Author,
		Action:             body.Action,
		ConfirmedRootCause: body.ConfirmedRootCause,
		Comment:            body.Comment,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	_, _ = a.store.AppendEvent(r.Context(), id, "human_feedback",
		map[string]any{"author": body.Author, "action": body.Action, "comment": body.Comment})

	// 通知工作流(若在等待反馈)
	if a.tempo != nil && inv.WorkflowID != "" {
		_ = a.tempo.Signal(r.Context(), inv.WorkflowID, "HumanFeedback", map[string]any{
			"author": body.Author, "action": body.Action,
			"confirmed_root_cause": body.ConfirmedRootCause, "comment": body.Comment,
		})
	}

	// 反馈闭环:confirm/correct 意味着人给出了**标注真值**(根因是什么),
	// 那正是 Golden Case 需要的东西。提升为 pending 用例,等人审核后入评测集。
	//
	// 只在这两种动作时提升:reject 表示结论错但没说对的是什么(没有真值),
	// close 只是流程动作。confirm 未填 confirmed_root_cause 时退回诊断摘要 ——
	// 人点了"确认"就意味着认可那个结论。
	if body.Action == "confirm" || body.Action == "correct" {
		a.promoteGoldenCase(r, inv, inc, body.ConfirmedRootCause, body.Author)
	}

	// close:关闭调查与 Incident
	if body.Action == "close" {
		_ = a.store.SetInvestigationPhase(r.Context(), id, "closed")
		_ = a.store.SetIncidentStatus(r.Context(), inv.IncidentID, "closed")
		_, _ = a.store.AppendEvent(r.Context(), id, "phase_changed", map[string]any{"phase": "closed"})
	}
	a.store.Audit(r.Context(), inv.TenantID, body.Author, "human_feedback", "investigation", id, "ok",
		nil, map[string]any{"action": body.Action})
	// F10 采纳率:按 action 分维度,不预先算比率 ——
	// 采纳率 = confirm / sum(confirm,correct,reject),用 PromQL 现算即可。
	// 固化成比率会丢掉分子分母,而"低采纳率"与"没人给反馈"是完全不同的问题。
	if a.feedbackMet != nil {
		a.feedbackMet.IncHumanFeedback(body.Action)
	}
	httpx.JSON(w, http.StatusOK, fb)
}

var _ = context.Background

// promoteGoldenCase 把调查提升为待审评测用例。失败只记日志:
// 反馈本身已经落库,提升是**增强** —— 它失败不该让反馈提交失败。
func (a *PublicAPI) promoteGoldenCase(r *http.Request, inv model.Investigation,
	inc model.Incident, confirmedRootCause, author string) {
	rootCause := strings.TrimSpace(confirmedRootCause)
	if rootCause == "" && inv.Diagnosis != nil {
		// confirm 未填根因:人点了"确认"即认可诊断结论,用**排名第一的假设陈述**
		// 作为标注真值 —— 那正是他确认的那句话。
		// 不用 ConfirmedFacts:那是证据陈述,不是根因判断。
		rootCause = topHypothesisStatement(inv.Diagnosis)
	}
	if rootCause == "" {
		a.log.Info("skip golden case promotion: 无标注真值",
			"investigation_id", inv.InvestigationID)
		return
	}
	caseID, created, err := a.store.PromoteInvestigationToGoldenCase(
		r.Context(), inv, inc, rootCause, author)
	if err != nil {
		a.log.Warn("promote golden case failed(反馈已保存,不影响)",
			"investigation_id", inv.InvestigationID, "err", err)
		return
	}
	if created {
		a.store.Audit(r.Context(), inv.TenantID, author, "golden_case_promoted",
			"golden_case", caseID, "ok", nil,
			map[string]any{"investigation_id": inv.InvestigationID, "review_status": "pending"})
		a.log.Info("golden case promoted (pending review)",
			"case_id", caseID, "investigation_id", inv.InvestigationID)
		if a.goldenMet != nil {
			a.goldenMet.IncGoldenCasePromoted()
		}
	}
}

// topHypothesisStatement 取排名最高(rank 最小)的假设陈述。
// rank 未设置(全为 0)时退回列表首项。
func topHypothesisStatement(d *model.DiagnosisResult) string {
	best := ""
	bestRank := 0
	for _, h := range d.Hypotheses {
		st := strings.TrimSpace(h.Statement)
		if st == "" {
			continue
		}
		if best == "" || (h.Rank > 0 && (bestRank == 0 || h.Rank < bestRank)) {
			best, bestRank = st, h.Rank
		}
	}
	return best
}
