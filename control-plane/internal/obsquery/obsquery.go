// Package obsquery 直接查询共享的可观测性后端(Prometheus / Loki / Tempo)。
//
// 为什么在控制面而不是 cluster-agent:
// 这些后端是**多集群共用的中心服务**,不在任何一个 K8s 集群内。让每集群的
// cluster-agent 去代理它们只带来无谓的网络绕行(控制面→某集群 agent→中心后端)、
// N 份重复凭据,以及"AIOps 最需要工作时,却因某个集群 agent 挂掉而同时失去
// metrics/logs/traces 三类证据"的可用性风险。cluster-agent 只保留它真正
// 必须在集群内做的事:访问该集群的 Kubernetes API。
//
// 安全约束(不因"在控制面内部"而放松):
// 真正的不可信输入是**模型输出的查询参数**,它在控制面内与在 agent 内同样不可信。
// 因此完整保留原有守卫:PromQL AST 级 label 强制注入(防裸选择器绕过)、
// LogQL 流选择器注入、cluster+namespace 双维度强制(共享后端必需)、
// 跨范围 matcher 拒绝、DNS-1123 名称白名单、响应体大小上限、时间窗上限。
//
// 权衡记录:控制面因此持有观测后端凭据。控制面是对外暴露 API 的组件,
// 若其威胁模型变化(如暴露到更不可信网络),这是第一个应回退的决定
// (改回由独立 agent 持有凭据)。见 docs/ARCHITECTURE.md 能力边界。
package obsquery

import (
	"fmt"
	"time"
)

// maxWindow 单次查询的最大时间跨度上限,防止超大范围扫描打爆后端。
const maxWindow = 24 * time.Hour

// TimeRange 查询时间范围(RFC3339 字符串,由 Tool Gateway 注入)。
type TimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ResourceRef 目标资源标识。
type ResourceRef struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
	UID  string `json:"uid,omitempty"`
}

// Scope 由 Tool Gateway 强制注入的查询范围,约束每次调用到单一
// 集群 / 命名空间 / 资源 / 时间窗。查询实现必须遵守它。
type Scope struct {
	ClusterID string      `json:"cluster_id"`
	Namespace string      `json:"namespace"`
	Resource  ResourceRef `json:"resource,omitempty"`
	TimeRange *TimeRange  `json:"time_range,omitempty"`
}

// ResourceName 返回目标资源名(未设置时为空)。
func (s Scope) ResourceName() string { return s.Resource.Name }

// Result 归一化的查询输出:summary 供 LLM 使用,raw 为结构化证据。
type Result struct {
	Source    string `json:"source"`
	Summary   string `json:"summary"`
	Raw       any    `json:"raw"`
	Freshness string `json:"freshness"`
}

// ns 返回作用域命名空间(缺省 default)。
func ns(scope Scope) string {
	if scope.Namespace == "" {
		return "default"
	}
	return scope.Namespace
}

// liveResource 返回作用域资源名(可能为空,表示不限定具体资源)。
func liveResource(scope Scope) string { return scope.ResourceName() }

// window 解析查询时间窗,并施加上限与反向/空区间兜底。
// 超过 maxWindow 时截断为"最近 maxWindow",避免超大范围扫描。
func window(scope Scope, now func() time.Time) (from, to time.Time) {
	to = now().UTC()
	from = to.Add(-5 * time.Minute)
	if scope.TimeRange != nil {
		if t, err := time.Parse(time.RFC3339, scope.TimeRange.From); err == nil {
			from = t
		}
		if t, err := time.Parse(time.RFC3339, scope.TimeRange.To); err == nil {
			to = t
		}
	}
	if !to.After(from) {
		from = to.Add(-5 * time.Minute)
	}
	if to.Sub(from) > maxWindow {
		from = to.Add(-maxWindow)
	}
	return from, to
}

// promStep 按时间跨度自适应步长,控制返回样本点数量。
func promStep(from, to time.Time) time.Duration {
	const targetPoints = 1000
	step := 60 * time.Second
	d := to.Sub(from)
	if d <= 0 {
		return step
	}
	if d < step {
		return d
	}
	if min := d / targetPoints; min > step {
		step = (min/time.Second + 1) * time.Second
	}
	return step
}

// unavailable 为未配置的后端构造一个良性 Result,使工具优雅降级而非报错。
func unavailable(source, namespace, resource, why string) Result {
	return Result{
		Source:  source + "/unavailable",
		Summary: why,
		Raw: map[string]any{
			"available": false,
			"namespace": namespace,
			"resource":  resource,
			"reason":    why,
		},
		Freshness: "n/a",
	}
}

// round4 保留 4 位小数(避免摘要里出现超长浮点)。
func round4(f float64) float64 {
	return float64(int(f*10000+0.5)) / 10000
}

// pct 格式化为百分比字符串。
func pct(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }
