package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/aiops/control-plane/internal/model"
	"github.com/aiops/control-plane/internal/store"
	"github.com/aiops/control-plane/internal/temporalx"
)

// WorkflowStarter 抽象 Temporal 启动/信号能力(便于降级:Temporal 不可用时仍持久化调查记录)。
type WorkflowStarter interface {
	Start(ctx context.Context, workflowID string, args temporalx.StartArgs) (string, error)
	Signal(ctx context.Context, workflowID, signalName string, payload any) error
}

// Orchestrator 把 Incident 转化为 Investigation 并启动 Temporal 工作流。
// 自动流程(消费 incidents)和人工发起(API)都走这里。
type Orchestrator struct {
	store       *store.Store
	wf          WorkflowStarter
	internalURL string
	tenant      string
	cooldownSec int
	maxActive   int
	log         *slog.Logger
}

// Limits 触发策略的冷却/并发上限(文档 6.3 硬停止条件)。
type Limits struct {
	CooldownSec int
	MaxActive   int
}

func NewOrchestrator(s *store.Store, wf WorkflowStarter, internalURL, tenant string, limits Limits, log *slog.Logger) *Orchestrator {
	return &Orchestrator{
		store: s, wf: wf, internalURL: internalURL, tenant: tenant,
		cooldownSec: limits.CooldownSec, maxActive: limits.MaxActive, log: log,
	}
}

// HandleIncidentEvent 消费 incidents topic(bus.Handler 签名)。
func (o *Orchestrator) HandleIncidentEvent(ctx context.Context, _ []byte, value []byte) error {
	var inc model.Incident
	if err := json.Unmarshal(value, &inc); err != nil {
		o.log.Warn("bad incident payload", "err", err)
		return nil
	}
	dec := EvaluateAuto(inc)
	if !dec.Trigger {
		return nil
	}

	// 若该 Incident 已有活跃调查(任意版本),不重复启动,而是通过 Temporal Signal
	// 通知已有 Workflow 有新版本(文档 6.2)。
	if existing, ok := o.activeInvestigation(ctx, inc.IncidentID); ok {
		if existing.WorkflowID != "" {
			err := o.wf.Signal(ctx, existing.WorkflowID, "IncidentUpdated", map[string]any{
				"incident_id": inc.IncidentID, "version": inc.Version, "severity": inc.Severity,
			})
			if err != nil {
				o.log.Warn("signal IncidentUpdated failed", "workflow", existing.WorkflowID, "err", err)
			} else {
				o.log.Info("incident updated signalled to existing investigation",
					"investigation_id", existing.InvestigationID, "version", inc.Version)
			}
			_, _ = o.store.AppendEvent(ctx, existing.InvestigationID, "incident_updated",
				map[string]any{"version": inc.Version})
		}
		return nil
	}

	_, err := o.StartInvestigation(ctx, inc.IncidentID, "system", dec.Reason, nil)
	if err != nil {
		o.log.Warn("auto start investigation failed", "incident", inc.IncidentID, "err", err)
		// 不返回 error:避免重复消费风暴;失败已审计
	}
	return nil
}

// activeInvestigation 返回该 Incident 下最新的非终态调查。
func (o *Orchestrator) activeInvestigation(ctx context.Context, incidentID string) (model.Investigation, bool) {
	invs, err := o.store.ListInvestigationsByIncident(ctx, incidentID)
	if err != nil {
		return model.Investigation{}, false
	}
	for _, iv := range invs { // 已按 started_at DESC 排序
		if iv.Phase != "closed" && iv.Phase != "cancelled" {
			return iv, true
		}
	}
	return model.Investigation{}, false
}

// StartInvestigation 创建调查并启动工作流。命中硬停止条件时返回错误(带原因)。
// budget 为 nil 时用默认预算。triggeredBy = "system" 或用户名。
func (o *Orchestrator) StartInvestigation(ctx context.Context, incidentID, triggeredBy, reason string, budget *model.Budget) (model.Investigation, error) {
	inc, err := o.store.GetIncident(ctx, incidentID)
	if err != nil {
		return model.Investigation{}, fmt.Errorf("load incident: %w", err)
	}
	// 用 incident 自身的租户(多租户隔离),仅在缺失时回退到全局默认。
	tenant := inc.TenantID
	if tenant == "" {
		tenant = o.tenant
	}

	active, err := o.store.HasActiveInvestigation(ctx, inc.IncidentID, inc.Version)
	if err != nil {
		return model.Investigation{}, err
	}
	// 冷却期:同 incident 上次调查距今秒数
	sincePrior, hasPrior, err := o.store.SecondsSinceLastInvestigation(ctx, inc.IncidentID)
	if err != nil {
		return model.Investigation{}, err
	}
	// 并发上限:该租户当前活跃调查数
	activeCount, err := o.store.CountActiveInvestigations(ctx, tenant)
	if err != nil {
		return model.Investigation{}, err
	}
	stopIn := StopInput{
		Incident:             inc,
		HasActiveSameVersion: active,
		SecondsSincePrior:    sincePrior,
		HasPrior:             hasPrior,
		CooldownSec:          o.cooldownSec,
		ActiveCount:          activeCount,
		MaxActive:            o.maxActive,
	}
	if stop := StopReason(stopIn); stop != "" {
		o.store.Audit(ctx, tenant, triggeredBy, "investigation_stopped", "incident", inc.IncidentID, "denied",
			nil, map[string]any{"reason": stop, "active_count": activeCount})
		return model.Investigation{}, &StopError{Reason: stop}
	}

	b := model.DefaultBudget()
	if budget != nil {
		b = *budget
	}
	inv := model.Investigation{
		InvestigationID: "inv-" + randHex(10),
		TenantID:        tenant,
		IncidentID:      inc.IncidentID,
		IncidentVersion: inc.Version,
		WorkflowID:      fmt.Sprintf("investigation/%s/%d", inc.IncidentID, inc.Version),
		Phase:           "queued",
		TriggerReason:   reason,
		TriggeredBy:     triggeredBy,
		Budget:          b,
		Usage:           model.Usage{},
		PolicyVersion:   PolicyVersion,
	}
	if err := o.store.CreateInvestigation(ctx, inv); err != nil {
		return model.Investigation{}, fmt.Errorf("create investigation: %w", err)
	}
	if _, err := o.store.AppendEvent(ctx, inv.InvestigationID, "phase_changed",
		map[string]any{"phase": "queued", "reason": reason, "triggered_by": triggeredBy}); err != nil {
		o.log.Warn("append event failed", "err", err)
	}

	// 启动 Temporal 工作流(降级:失败也保留调查记录,标记以便重试)
	budgetMap := map[string]any{
		"max_duration_sec": b.MaxDurationSec, "max_rounds": b.MaxRounds,
		"max_tokens": b.MaxTokens, "max_cost_usd": b.MaxCostUSD, "max_tool_calls": b.MaxToolCalls,
	}
	runID, err := o.wf.Start(ctx, inv.WorkflowID, temporalx.StartArgs{
		InvestigationID:    inv.InvestigationID,
		IncidentID:         inc.IncidentID,
		IncidentVersion:    inc.Version,
		TenantID:           tenant,
		ClusterID:          inc.ClusterID,
		Budget:             budgetMap,
		ControlInternalURL: o.internalURL,
	})
	if err != nil {
		o.log.Warn("temporal start failed (investigation persisted)", "workflow", inv.WorkflowID, "err", err)
		o.store.Audit(ctx, tenant, triggeredBy, "workflow_start_failed", "investigation", inv.InvestigationID, "error",
			nil, map[string]any{"err": err.Error()})
		return inv, nil
	}
	inv.RunID = runID
	if err := o.store.SetInvestigationWorkflow(ctx, inv.InvestigationID, inv.WorkflowID, runID); err != nil {
		o.log.Warn("set workflow id failed", "err", err)
	}
	o.store.Audit(ctx, tenant, triggeredBy, "investigation_started", "investigation", inv.InvestigationID, "ok",
		map[string]any{"cluster": inc.ClusterID}, map[string]any{"reason": reason, "workflow_id": inv.WorkflowID})
	o.log.Info("investigation started", "investigation_id", inv.InvestigationID, "workflow", inv.WorkflowID)
	return inv, nil
}

// StopError 表示命中硬停止条件。
type StopError struct{ Reason string }

func (e *StopError) Error() string { return "investigation stopped: " + e.Reason }
