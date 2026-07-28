// Package tools 把强类型只读工具名映射到 DataSource 方法上,并为 /tools 端点
// 暴露它们的 JSON Schema 描述。
package tools

import (
	"context"
	"fmt"

	"github.com/aiops/cluster-agent/internal/datasource"
)

// handler 调用某一个 DataSource 方法。
type handler func(ctx context.Context, ds datasource.DataSource, scope datasource.Scope, args map[string]any) (datasource.Result, error)

// Tool 描述单个强类型工具:名称、可读描述、参数 JSON Schema,以及背后由
// DataSource 支撑的 handler。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"input_schema"`
	handler     handler        `json:"-"`
}

// Registry 保存绑定到某个 DataSource 的工具集合。
type Registry struct {
	ds    datasource.DataSource
	tools map[string]Tool
	order []string
}

// NewRegistry 基于 ds 构建完整的只读工具集。
func NewRegistry(ds datasource.DataSource) *Registry {
	r := &Registry{ds: ds, tools: map[string]Tool{}}
	for _, t := range defaultTools() {
		r.tools[t.Name] = t
		r.order = append(r.order, t.Name)
	}
	return r
}

// List 按稳定的声明顺序返回工具列表(供 /tools 使用)。
func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Has 判断 name 是否为已注册的工具。调用方(例如 HTTP 层)用它把无界的、
// 由攻击者控制的名字挡在指标标签之外。
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// ErrUnknownTool 在工具名未注册时返回。
type ErrUnknownTool struct{ Name string }

func (e ErrUnknownTool) Error() string { return fmt.Sprintf("unknown tool: %q", e.Name) }

// Invoke 分发到指定名字的工具,并把注入的 scope 透传下去。
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
