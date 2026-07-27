package trigger

import (
	"context"
	"log/slog"
	"time"

	"github.com/aiops/control-plane/internal/store"
	"github.com/aiops/control-plane/internal/temporalx"
)

// Reconciler 补偿孤儿调查(A2)。
//
// 崩溃窗口:CreateInvestigation(phase=queued)与 wf.Start 不是原子操作,
// 之间进程被杀会留下永远 queued、无 run_id 的调查。Reconciler 周期性扫描
// 这类调查并重试启动 Temporal 工作流;重试仍失败且超过最大存活期则置为终态
// (cancelled)并审计,避免无限悬挂。
//
// 幂等性:workflow ID 固定为 investigation/{incident}/{version},重复启动
// 同 ID 会被 Temporal 判为 already-started,因此重试是安全的。
type Reconciler struct {
	store    *store.Store
	wf       WorkflowStarter
	internal string
	graceSec int // 宽限期:创建后多久仍无 run_id 才算孤儿
	maxAge   time.Duration
	log      *slog.Logger
}

func NewReconciler(s *store.Store, wf WorkflowStarter, internalURL string, graceSec int, log *slog.Logger) *Reconciler {
	if graceSec <= 0 {
		graceSec = 60
	}
	return &Reconciler{
		store: s, wf: wf, internal: internalURL,
		graceSec: graceSec, maxAge: 24 * time.Hour, log: log,
	}
}

// Run 周期性对账,直至 ctx 取消。启动时先立即跑一次(覆盖"重启后恢复"场景)。
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	r.ReconcileOnce(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.ReconcileOnce(ctx)
		}
	}
}

// ReconcileOnce 扫描并补偿一批孤儿调查。返回补偿成功数。
func (r *Reconciler) ReconcileOnce(ctx context.Context) int {
	orphans, err := r.store.FindOrphanInvestigations(ctx, r.graceSec, 50)
	if err != nil {
		r.log.Warn("find orphan investigations failed", "err", err)
		return 0
	}
	if len(orphans) == 0 {
		return 0
	}
	r.log.Info("reconciling orphan investigations", "count", len(orphans))
	fixed := 0
	for _, o := range orphans {
		wfID := o.WorkflowID
		if wfID == "" {
			wfID = workflowID(o.IncidentID, o.IncidentVersion)
		}
		b := o.Budget
		runID, serr := r.wf.Start(ctx, wfID, temporalx.StartArgs{
			InvestigationID: o.InvestigationID,
			IncidentID:      o.IncidentID,
			IncidentVersion: o.IncidentVersion,
			TenantID:        o.TenantID,
			Budget: map[string]any{
				"max_duration_sec": b.MaxDurationSec, "max_rounds": b.MaxRounds,
				"max_tokens": b.MaxTokens, "max_cost_usd": b.MaxCostUSD,
				"max_tool_calls": b.MaxToolCalls,
			},
			ControlInternalURL: r.internal,
		})
		if serr != nil {
			r.log.Warn("orphan reconcile: workflow start failed",
				"investigation_id", o.InvestigationID, "err", serr)
			r.store.Audit(ctx, o.TenantID, "system", "investigation_reconcile",
				"investigation", o.InvestigationID, "error", nil,
				map[string]any{"err": serr.Error()})
			continue
		}
		if err := r.store.SetInvestigationWorkflow(ctx, o.InvestigationID, wfID, runID); err != nil {
			r.log.Warn("orphan reconcile: persist run id failed", "err", err)
		}
		r.store.Audit(ctx, o.TenantID, "system", "investigation_reconcile",
			"investigation", o.InvestigationID, "ok", nil,
			map[string]any{"workflow_id": wfID, "run_id": runID})
		r.log.Info("orphan investigation recovered",
			"investigation_id", o.InvestigationID, "workflow", wfID)
		fixed++
	}
	return fixed
}
