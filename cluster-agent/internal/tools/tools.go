// Package tools maps the typed read-only tool names onto DataSource methods
// and exposes their JSON-schema descriptors for the /tools endpoint.
package tools

import (
	"context"
	"fmt"

	"github.com/aiops/cluster-agent/internal/datasource"
)

// handler invokes one DataSource method.
type handler func(ctx context.Context, ds datasource.DataSource, scope datasource.Scope, args map[string]any) (datasource.Result, error)

// Tool describes a single typed tool: its name, human description, argument
// JSON schema, and the DataSource-backed handler.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"input_schema"`
	handler     handler        `json:"-"`
}

// Registry holds the tool set bound to a DataSource.
type Registry struct {
	ds    datasource.DataSource
	tools map[string]Tool
	order []string
}

// NewRegistry builds the full read-only tool set over ds.
func NewRegistry(ds datasource.DataSource) *Registry {
	r := &Registry{ds: ds, tools: map[string]Tool{}}
	for _, t := range defaultTools() {
		r.tools[t.Name] = t
		r.order = append(r.order, t.Name)
	}
	return r
}

// List returns the tools in stable declaration order (for /tools).
func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Has reports whether name is a registered tool. Callers (e.g. the HTTP layer)
// use it to keep unbounded, attacker-controlled names out of metric labels.
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// ErrUnknownTool is returned when a tool name is not registered.
type ErrUnknownTool struct{ Name string }

func (e ErrUnknownTool) Error() string { return fmt.Sprintf("unknown tool: %q", e.Name) }

// Invoke dispatches to the named tool, passing the injected scope through.
func (r *Registry) Invoke(ctx context.Context, name string, scope datasource.Scope, args map[string]any) (datasource.Result, error) {
	t, ok := r.tools[name]
	if !ok {
		return datasource.Result{}, ErrUnknownTool{Name: name}
	}
	if args == nil {
		args = map[string]any{}
	}
	return t.handler(ctx, r.ds, scope, args)
}

func strSchema(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func defaultTools() []Tool {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	return []Tool{
		{
			Name:        "get_workload_state",
			Description: "查询 Deployment/ReplicaSet/Pod 的健康状态、版本分布与就绪副本数(只读)。",
			Schema:      obj(map[string]any{}),
			handler: func(ctx context.Context, ds datasource.DataSource, s datasource.Scope, a map[string]any) (datasource.Result, error) {
				return ds.GetWorkloadState(ctx, s, a)
			},
		},
		{
			Name:        "get_kubernetes_events",
			Description: "返回目标资源最近的 Kubernetes 事件(BackOff/OOMKilling/Unhealthy 等,只读)。",
			Schema:      obj(map[string]any{}),
			handler: func(ctx context.Context, ds datasource.DataSource, s datasource.Scope, a map[string]any) (datasource.Result, error) {
				return ds.GetKubernetesEvents(ctx, s, a)
			},
		},
		{
			Name:        "query_metrics",
			Description: "按 PromQL 风格表达式在时间范围内查询指标(错误率、延迟、CPU 等,只读)。",
			Schema: obj(map[string]any{
				"expr": strSchema("PromQL 风格查询表达式,缺省时按故障场景返回默认指标"),
			}),
			handler: func(ctx context.Context, ds datasource.DataSource, s datasource.Scope, a map[string]any) (datasource.Result, error) {
				return ds.QueryMetrics(ctx, s, a)
			},
		},
		{
			Name:        "search_logs",
			Description: "检索目标资源的日志行(只读)。",
			Schema: obj(map[string]any{
				"query": strSchema("日志过滤关键字或 LogQL 风格表达式"),
			}),
			handler: func(ctx context.Context, ds datasource.DataSource, s datasource.Scope, a map[string]any) (datasource.Result, error) {
				return ds.SearchLogs(ctx, s, a)
			},
		},
		{
			Name:        "get_traces",
			Description: "返回目标资源的分布式调用链与瓶颈 span(只读)。",
			Schema:      obj(map[string]any{}),
			handler: func(ctx context.Context, ds datasource.DataSource, s datasource.Scope, a map[string]any) (datasource.Result, error) {
				return ds.GetTraces(ctx, s, a)
			},
		},
		{
			Name:        "list_recent_changes",
			Description: "列出近期发布、配置与基础设施变更(一等证据,只读)。",
			Schema:      obj(map[string]any{}),
			handler: func(ctx context.Context, ds datasource.DataSource, s datasource.Scope, a map[string]any) (datasource.Result, error) {
				return ds.ListRecentChanges(ctx, s, a)
			},
		},
		{
			Name:        "inspect_dependencies",
			Description: "返回目标资源周边的服务依赖边(上下游、错误率、延迟、来源与置信度,只读)。",
			Schema:      obj(map[string]any{}),
			handler: func(ctx context.Context, ds datasource.DataSource, s datasource.Scope, a map[string]any) (datasource.Result, error) {
				return ds.InspectDependencies(ctx, s, a)
			},
		},
	}
}
