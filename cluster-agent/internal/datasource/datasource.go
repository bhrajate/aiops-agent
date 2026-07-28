// Package datasource 定义 Cluster Agent 工具所使用的只读数据源抽象,
// 以及工具协议上交换的请求/响应共享类型。
//
// Cluster Agent 在契约上是**只读**的:DataSource 绝不允许变更集群状态、执行命令
// 或开启 shell。所有方法只做查询。
package datasource

import "context"

// TimeRange 是闭区间查询时间窗。两端均为 RFC3339 字符串。
type TimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ResourceRef 标识目标资源。由 Tool Gateway 以对象形式下发
// (见 docs/INTEGRATION.md);所有字段均可选。
type ResourceRef struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
	UID  string `json:"uid,omitempty"`
}

// Scope 由 Tool Gateway 注入,把每次工具调用约束在单一
// 集群 / 命名空间 / 资源 / 时间窗内。工具**必须**遵守。
//
// Live 数据源中的强制方式(见 live.go):
//   - Kubernetes 工具只查询 scope 指定的命名空间(使用 Namespaced 客户端)。
//
// 注:可观测性查询(Prometheus / Loki / Tempo)及其 label 强制注入、DNS-1123
// 校验、时间窗上限等守卫已迁至控制面 control-plane/internal/obsquery
// —— 那些后端是多集群共用的中心服务,不在任何集群内。本组件只做集群内 K8s 只读。
type Scope struct {
	ClusterID string      `json:"cluster_id"`
	Namespace string      `json:"namespace"`
	Resource  ResourceRef `json:"resource,omitempty"`
	TimeRange *TimeRange  `json:"time_range,omitempty"`
}

// ResourceName 返回目标资源名(未设置时为空)。
func (s Scope) ResourceName() string { return s.Resource.Name }

// Result 是返回给 Tool Gateway 的归一化工具输出。
//
//	source    来源系统(kubernetes | prometheus | loki | tempo | ...)
//	summary   供 LLM 阅读的自然语言(中文)摘要
//	raw       结构化证据载荷
//	freshness 数据新鲜度标记,例如 "10s"
type Result struct {
	Source    string `json:"source"`
	Summary   string `json:"summary"`
	Raw       any    `json:"raw"`
	Freshness string `json:"freshness"`
}

// DataSource 是可插拔的只读后端。第一版实现是确定性的 Mock;后续实现可包装
// client-go、Prometheus、Loki 与 Tempo。每个方法恰好对应一个强类型工具。
type DataSource interface {
	// GetWorkloadState 报告 Deployment / ReplicaSet / Pod 的健康状况。
	GetWorkloadState(ctx context.Context, scope Scope, args map[string]any) (Result, error)
	// GetKubernetesEvents 返回该资源近期的 Kubernetes 事件。
	GetKubernetesEvents(ctx context.Context, scope Scope, args map[string]any) (Result, error)
	// ListRecentChanges 返回发布、配置与基础设施变更(一等证据)。
	ListRecentChanges(ctx context.Context, scope Scope, args map[string]any) (Result, error)
	// InspectDependencies 返回该资源周边的服务依赖边。
	InspectDependencies(ctx context.Context, scope Scope, args map[string]any) (Result, error)
}
