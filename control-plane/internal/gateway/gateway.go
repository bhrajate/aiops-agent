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

// observabilityTools 是查询"共享观测后端"的工具。这些数据源(Prometheus/Loki/Tempo)
// 通常是多集群共用的中心服务,不在任一 K8s 集群内——因此**按数据源归属**路由到
// 中心 Observability Agent,而不是绕经某个集群的 cluster-agent。
// K8s 类工具(get_workload_state 等)仍走对应集群的 agent(需集群内 SA)。
var observabilityTools = map[string]bool{
	"query_metrics": true,
	"search_logs":   true,
	"get_traces":    true,
}

// RawSnapshotStore 抽象对象存储上传(便于降级:未配置时跳过)。
type RawSnapshotStore interface {
	PutJSON(ctx context.Context, key string, data []byte) (string, error)
}

// ToolMetrics 抽象工具指标记录(nil-safe;避免 gateway 直接依赖 telemetry 包）。
type ToolMetrics interface {
	ObserveTool(tool, result string, seconds float64)
}

// ToolInvoker 抽象一个"能执行只读工具"的后端(cluster-agent 或中心 Observability Agent)。
type ToolInvoker interface {
	Invoke(ctx context.Context, tool string, args map[string]any, scope agentclient.Scope) (agentclient.ToolResult, error)
}

type Gateway struct {
	store   *store.Store
	agents  *agentclient.Registry // K8s 类工具:按 cluster_id 路由到该集群的 Agent
	obs     ToolInvoker           // 观测类工具:中心 Observability Agent(可为 nil→回退到集群 agent)
	obj     RawSnapshotStore      // 可为 nil(降级:仅存摘要)
	metrics ToolMetrics           // 可为 nil
	log     *slog.Logger
}

func New(s *store.Store, agents *agentclient.Registry, obs ToolInvoker, obj RawSnapshotStore, metrics ToolMetrics, log *slog.Logger) *Gateway {
	return &Gateway{store: s, agents: agents, obs: obs, obj: obj, metrics: metrics, log: log}
}

// invokerFor 按工具的数据源归属选择执行后端:
//   - 观测类工具 + 已配置中心 Observability Agent → 中心 agent(不绕集群);
//   - 其余(K8s 类)→ 按 incident 集群路由到该集群 agent。
//
// 未配置中心 agent 时,观测类工具回退到集群 agent(兼容"每集群自带后端"拓扑)。
func (g *Gateway) invokerFor(tool, clusterID string) (ToolInvoker, error) {
	if observabilityTools[tool] && g.obs != nil {
		return g.obs, nil
	}
	return g.agents.For(clusterID)
}

func (g *Gateway) observeTool(tool, result string, seconds float64) {
	if g.metrics != nil {
		g.metrics.ObserveTool(tool, result, seconds)
	}
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
	// tenant 先用缺省,解析出 investigation 后改用其真实 tenant(多租户隔离)
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
	if inv.TenantID != "" {
		tenant = inv.TenantID // 用调查真实租户记录证据与审计
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

	// 5) 按数据源归属选择后端:观测类→中心 Observability Agent;K8s 类→该集群 agent。
	//    K8s 类未配置对应集群 agent 则拒绝(绝不回退到别集群=跨集群越权)。
	invoker, aerr := g.invokerFor(req.Tool, scope.ClusterID)
	if aerr != nil {
		g.deny(ctx, tenant, req, "no_agent_for_cluster")
		return InvokeResult{Status: "denied", Reason: "no_agent_for_cluster"}, nil
	}

	// 调用只读数据源
	start := time.Now()
	res, err := invoker.Invoke(ctx, req.Tool, req.Arguments, scope)
	elapsed := time.Since(start)
	if err != nil {
		g.observeTool(req.Tool, "error", elapsed.Seconds())
		g.store.Audit(ctx, tenant, "cluster-agent", "tool_invoke", "investigation", req.InvestigationID, "error",
			map[string]any{"cluster": scope.ClusterID, "namespace": scope.Namespace},
			map[string]any{"tool": req.Tool, "err": err.Error()})
		return InvokeResult{}, fmt.Errorf("tool %s failed: %w", req.Tool, err)
	}
	g.observeTool(req.Tool, "ok", elapsed.Seconds())

	// 6) 脱敏(对 summary 与 raw 做敏感信息擦除)
	redacted := false
	res.Summary, redacted = Redact(res.Summary)
	rawBytes, _ := json.Marshal(res.Raw)
	redactedRaw, r2 := Redact(string(rawBytes))
	redacted = redacted || r2

	// 7) 冻结为不可变 Evidence
	evID := "ev-" + randHex(10)

	// 原始快照(脱敏后)上传对象存储,库里只留 raw_ref(SECURITY §6;最小化进模型内容)
	rawRef := ""
	if g.obj != nil {
		key := fmt.Sprintf("%s/%s.json", req.InvestigationID, evID)
		if ref, uerr := g.obj.PutJSON(ctx, key, []byte(redactedRaw)); uerr != nil {
			g.log.Warn("evidence snapshot upload failed (summary still persisted)", "err", uerr)
		} else {
			rawRef = ref
		}
	}

	ev := model.Evidence{
		EvidenceID:      evID,
		TenantID:        tenant,
		InvestigationID: req.InvestigationID,
		Type:            evidenceType[req.Tool],
		Source:          res.Source,
		ToolName:        req.Tool,
		Query:           map[string]any{"arguments": req.Arguments, "scope": scope},
		TimeRange:       scope.TimeRange,
		Summary:         res.Summary,
		RawRef:          rawRef,
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
	// 时间窗从 incident first_seen 推导(而非固定 1h):故障早于 1 小时开始时,
	// 固定窗会漏掉起始证据。取 first_seen 前留 15 分钟基线,上限 24h 防超大扫描。
	now := time.Now().UTC()
	from := now.Add(-1 * time.Hour)
	if !inc.FirstSeen.IsZero() {
		cand := inc.FirstSeen.Add(-15 * time.Minute)
		if cand.Before(from) {
			from = cand
		}
		if min := now.Add(-24 * time.Hour); from.Before(min) {
			from = min // 上限 24h
		}
	}
	return agentclient.Scope{
		ClusterID: inc.ClusterID,
		Namespace: ns,
		Resource:  resource,
		TimeRange: map[string]any{
			"from": from.Format(time.RFC3339),
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
