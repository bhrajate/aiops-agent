// Package gateway 实现 Tool Gateway(文档第 9 节):
// 工具注册、授权、范围注入、Schema 校验、脱敏、限额、审计。
// 模型/Worker 只能通过 Gateway 访问只读工具,不能绕过策略。
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/aiops/control-plane/internal/agentclient"
	"github.com/aiops/control-plane/internal/model"
	"github.com/aiops/control-plane/internal/store"
)

// allowedTools 首版类型化只读工具集合(文档 9.1)。retrieve_runbook 由 Gateway 直接查知识库。
var allowedTools = map[string]bool{
	"get_workload_state":    true,
	"get_kubernetes_events": true,
	"query_metrics":         true,
	"search_logs":           true,
	"get_traces":            true,
	"list_recent_changes":   true,
	"inspect_dependencies":  true,
	"retrieve_runbook":      true,
}

// evidenceType 工具 → 证据类型映射。
var evidenceType = map[string]string{
	"get_workload_state":    "kubernetes",
	"get_kubernetes_events": "kubernetes",
	"query_metrics":         "metric",
	"search_logs":           "log",
	"get_traces":            "trace",
	"list_recent_changes":   "change",
	"inspect_dependencies":  "kubernetes",
	"retrieve_runbook":      "knowledge",
}

type Gateway struct {
	store *store.Store
	agent *agentclient.Client
	log   *slog.Logger
}

func New(s *store.Store, agent *agentclient.Client, log *slog.Logger) *Gateway {
	return &Gateway{store: s, agent: agent, log: log}
}

// InvokeRequest 来自 AI Worker 的工具调用请求。
type InvokeRequest struct {
	InvestigationID string         `json:"investigation_id"`
	IncidentID      string         `json:"incident_id"`
	Tool            string         `json:"tool"`
	Arguments       map[string]any `json:"arguments"`
}

// InvokeResult 返回给 Worker。
type InvokeResult struct {
	Status   string          `json:"status"` // ok | denied
	Reason   string          `json:"reason,omitempty"`
	Evidence *model.Evidence `json:"evidence,omitempty"`
}

// Invoke 执行工具:授权 → 范围注入 → schema 校验 → 调用数据源 → 脱敏 → 持久化 Evidence → 审计。
func (g *Gateway) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	tenant := "default"

	// 1) 工具白名单
	if !allowedTools[req.Tool] {
		g.deny(ctx, tenant, req, "tool_not_allowed")
		return InvokeResult{Status: "denied", Reason: "tool_not_allowed"}, nil
	}

	// 2) 授权:调查必须存在,且工具目标限定在该 Incident 范围内(最小权限)
	inv, err := g.store.GetInvestigation(ctx, req.InvestigationID)
	if err != nil {
		g.deny(ctx, tenant, req, "investigation_not_found")
		return InvokeResult{Status: "denied", Reason: "investigation_not_found"}, nil
	}
	inc, err := g.store.GetIncident(ctx, inv.IncidentID)
	if err != nil {
		g.deny(ctx, tenant, req, "incident_not_found")
		return InvokeResult{Status: "denied", Reason: "incident_not_found"}, nil
	}

	// 3) 范围注入:强制注入 cluster / namespace / 资源 / 时间窗口(不信任 Worker 自带范围)
	scope := g.buildScope(inc)

	// retrieve_runbook:走知识库,不经 cluster-agent
	if req.Tool == "retrieve_runbook" {
		return g.runbook(ctx, tenant, req, inc)
	}

	// 4) Schema 校验(基础:参数必须是对象;限制时间跨度由 scope 决定)
	if req.Arguments == nil {
		req.Arguments = map[string]any{}
	}

	// 5) 调用只读数据源
	start := time.Now()
	res, err := g.agent.Invoke(ctx, req.Tool, req.Arguments, scope)
	elapsed := time.Since(start)
	if err != nil {
		g.store.Audit(ctx, tenant, "cluster-agent", "tool_invoke", "investigation", req.InvestigationID, "error",
			map[string]any{"cluster": scope.ClusterID, "namespace": scope.Namespace},
			map[string]any{"tool": req.Tool, "err": err.Error()})
		return InvokeResult{}, fmt.Errorf("tool %s failed: %w", req.Tool, err)
	}

	// 6) 脱敏(对 summary 与 raw 做敏感信息擦除)
	redacted := false
	res.Summary, redacted = Redact(res.Summary)
	rawBytes, _ := json.Marshal(res.Raw)
	redactedRaw, r2 := Redact(string(rawBytes))
	redacted = redacted || r2

	// 7) 冻结为不可变 Evidence
	ev := model.Evidence{
		EvidenceID:      "ev-" + randHex(10),
		TenantID:        tenant,
		InvestigationID: req.InvestigationID,
		Type:            evidenceType[req.Tool],
		Source:          res.Source,
		ToolName:        req.Tool,
		Query:           map[string]any{"arguments": req.Arguments, "scope": scope},
		TimeRange:       scope.TimeRange,
		Summary:         res.Summary,
		ContentHash:     hashStr(res.Summary + redactedRaw),
		Freshness:       res.Freshness,
		RedactionStatus: redactionStatus(redacted),
		CreatedAt:       time.Now().UTC(),
	}
	if err := g.store.InsertEvidence(ctx, ev); err != nil {
		return InvokeResult{}, fmt.Errorf("persist evidence: %w", err)
	}

	// 8) 审计(记录参数、身份、目标范围、结果摘要、耗时、证据引用)
	g.store.Audit(ctx, tenant, "cluster-agent", "tool_invoke", "evidence", ev.EvidenceID, "ok",
		map[string]any{"cluster": scope.ClusterID, "namespace": scope.Namespace, "resource": scope.Resource},
		map[string]any{"tool": req.Tool, "elapsed_ms": elapsed.Milliseconds(),
			"summary": truncate(ev.Summary, 200), "investigation_id": req.InvestigationID})

	_, _ = g.store.AppendEvent(ctx, req.InvestigationID, "tool_called",
		map[string]any{"tool": req.Tool, "evidence_id": ev.EvidenceID, "source": ev.Source})
	_, _ = g.store.AppendEvent(ctx, req.InvestigationID, "evidence_added",
		map[string]any{"evidence_id": ev.EvidenceID, "type": ev.Type, "summary": truncate(ev.Summary, 200)})

	return InvokeResult{Status: "ok", Evidence: &ev}, nil
}

func (g *Gateway) runbook(ctx context.Context, tenant string, req InvokeRequest, inc model.Incident) (InvokeResult, error) {
	q, _ := req.Arguments["query"].(string)
	if q == "" {
		q = inc.FaultCategory
	}
	items, err := g.store.SearchKnowledge(ctx, q, 3)
	if err != nil {
		return InvokeResult{}, err
	}
	summary := "未找到匹配的 Runbook(仅作参考知识,不作实时证据)。"
	if len(items) > 0 {
		summary = fmt.Sprintf("检索到 %d 条参考 Runbook:", len(items))
		for _, it := range items {
			summary += "\n- " + it.Title
		}
	}
	raw, _ := json.Marshal(items)
	ev := model.Evidence{
		EvidenceID:      "ev-" + randHex(10),
		TenantID:        tenant,
		InvestigationID: req.InvestigationID,
		Type:            "knowledge",
		Source:          "knowledge_service",
		ToolName:        "retrieve_runbook",
		Query:           map[string]any{"query": q},
		Summary:         summary,
		ContentHash:     hashStr(string(raw)),
		Freshness:       "n/a",
		RedactionStatus: "clean",
		CreatedAt:       time.Now().UTC(),
	}
	if err := g.store.InsertEvidence(ctx, ev); err != nil {
		return InvokeResult{}, err
	}
	_, _ = g.store.AppendEvent(ctx, req.InvestigationID, "tool_called",
		map[string]any{"tool": "retrieve_runbook", "evidence_id": ev.EvidenceID})
	return InvokeResult{Status: "ok", Evidence: &ev}, nil
}

func (g *Gateway) buildScope(inc model.Incident) agentclient.Scope {
	ns := ""
	var resource map[string]any
	if len(inc.AffectedResources) > 0 {
		r := inc.AffectedResources[0]
		ns = r.Namespace
		resource = map[string]any{"kind": r.Kind, "name": r.Name, "uid": r.UID}
	}
	now := time.Now().UTC()
	return agentclient.Scope{
		ClusterID: inc.ClusterID,
		Namespace: ns,
		Resource:  resource,
		TimeRange: map[string]any{
			"from": now.Add(-1 * time.Hour).Format(time.RFC3339),
			"to":   now.Format(time.RFC3339),
		},
	}
}

func (g *Gateway) deny(ctx context.Context, tenant string, req InvokeRequest, reason string) {
	g.store.Audit(ctx, tenant, "model", "tool_invoke", "investigation", req.InvestigationID, "denied",
		nil, map[string]any{"tool": req.Tool, "reason": reason})
	g.log.Warn("tool invoke denied", "tool", req.Tool, "reason", reason, "investigation", req.InvestigationID)
}

func redactionStatus(r bool) string {
	if r {
		return "redacted"
	}
	return "clean"
}

func hashStr(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var _ = regexp.MustCompile
