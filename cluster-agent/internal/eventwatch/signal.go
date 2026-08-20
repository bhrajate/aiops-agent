package eventwatch

// K8s Event → Signal 的映射。
//
// 补的能力边界(docs/ARCHITECTURE.md):"仅 pull —— 无主动上报、无 K8s Event
// watch。瞬时事件若超出查询时间窗或被 K8s 回收即不可得。"
//
// 为什么拉取式不够:Event 在 etcd 里默认只留 1 小时,而一次调查从 signal 到
// collecting 阶段可能过了几十分钟。更要紧的是**没有告警规则覆盖的故障根本不会
// 触发调查** —— 那个 OOMKilled 事件从头到尾没人查过,1 小时后消失。
//
// 它**不是**一条持久管道:丢一次推送不是灾难(事件仍可被 get_kubernetes_events
// 查到,只要还没被 GC)。这个定位决定了下面的可靠性取舍都偏向"简单 + 不影响
// 既有路径",而不是"不丢"。

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Signal 是发给控制面 POST /v1/signals 的**原生格式**载荷。
//
// 字段名与 control-plane/internal/model.Signal 的 json tag 对齐。刻意不共享类型:
// cluster-agent 是独立 Go module,不依赖 control-plane —— 那条边界是有意的
// (agent 跑在业务集群里,不该拉进控制面的依赖树)。代价是这里的 json tag
// 必须与那边手工保持一致,故有 TestSignalJSONShape 钉住字段名。
type Signal struct {
	// SignalID 刻意**留空**。
	//
	// 幂等键由控制面的 model.DeriveSignalID 推导
	// (payload 哈希 + signal_type + starts_at,见 ingress.go 的 fromNative)。
	// agent 自己算一份会得到第二套幂等规则,而两套规则的分歧只会在生产的重复
	// 数据里显现 —— 那是最难查的一类。所以这里不填,让控制面唯一负责。
	SignalID    string            `json:"signal_id,omitempty"`
	TenantID    string            `json:"tenant_id,omitempty"`
	ClusterID   string            `json:"cluster_id"`
	Source      string            `json:"source"`
	SignalType  string            `json:"signal_type"`
	ResourceRef ResourceRef       `json:"resource_ref"`
	Severity    string            `json:"severity"`
	StartsAt    *time.Time        `json:"starts_at,omitempty"`
	Labels      map[string]string `json:"labels"`
}

// ResourceRef 与 model.ResourceRef 的 json tag 对齐。
type ResourceRef struct {
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
}

// SourceKubernetes 让 signal 的来源可区分 —— 值班台据此回答
// "多少故障是主动发现的"(与 SLO watcher 的 detector 标签同一用途)。
const SourceKubernetes = "kubernetes"

// bucketWidth 是 lastTimestamp 的归一化粒度。
//
// **这是本文件最关键的一个取舍。** kubelet 把重复事件聚合成一个 Event 对象:
// 同一 UID,count 递增,lastTimestamp 前移。informer 会为每次递增推一个 UPDATE;
// agent 重启或 resync 会把现存事件全量重放。两种直觉做法都错:
//
//   - 只按 UID 定身份:重放安全,但一次 OOMKill 循环两小时只产生一个 signal,
//     incident.last_seen 永不前移。值班界面上那个故障看起来"10 分钟前的事",
//     而它正在发生。
//   - 每个 UPDATE 一条:count 从 1 涨到 200 就是 200 条 signal。这正是 F12 的
//     失效模式 —— 表里去重了但 signal_count 虚增,而那个数喂给触发策略判
//     "信号突发"(burst_signals=3)。一个 Pod 反复重启会被读成影响面扩大,
//     拉起多轮 RCA。
//
// 取 5 分钟桶:每事件每小时最多 12 条 signal(上界确定),同时 last_seen 能前移。
// 桶太窄退化成 F12,太宽则 last_seen 迟滞到没有意义。
const bucketWidth = 5 * time.Minute

// bucketTime 把时刻归一化到 5 分钟桶的起点(UTC)。
//
// 必须统一到 UTC:控制面的 DeriveSignalID 用 RFC3339Nano 格式化 startsAt,
// 同一时刻的不同时区表示会得到不同 ID(见 signalid.go 的注释)。
func bucketTime(t time.Time) time.Time {
	return t.UTC().Truncate(bucketWidth)
}

// eventTime 取事件的"最近一次发生"时刻,按可用性回退。
//
// 新版 K8s 用 series.lastObservedTime / eventTime,旧版用 lastTimestamp。
// 全空时回退到 firstTimestamp,再空则由调用方用当前时刻兜底 ——
// 返回零值让调用方决定,而不是在这里悄悄用 time.Now():
// 那会让同一事件在每次 resync 都落进新桶,退化成 F12。
func eventTime(ev *corev1.Event) time.Time {
	if ev.Series != nil && !ev.Series.LastObservedTime.IsZero() {
		return ev.Series.LastObservedTime.Time
	}
	if !ev.LastTimestamp.IsZero() {
		return ev.LastTimestamp.Time
	}
	if !ev.EventTime.IsZero() {
		return ev.EventTime.Time
	}
	return ev.FirstTimestamp.Time
}

// severityFor 把 reason 映射成归一化严重级别。
//
// K8s Event 没有 severity 字段。默认 "warning" 而**不是** critical:
// 后者会让一次节点抖动刷出一屏 P1,而 P1 在触发策略里是"必触发"
// (always=[P1 P2]),等于每个 Pod 事件都消耗一次分诊模型调用。
func severityFor(reason string) string {
	switch reason {
	case "OOMKilling", "Evicted", "NodeNotReady", "FailedCreatePodSandBox":
		return "critical"
	default:
		return "warning"
	}
}

// ToSignal 把一条 Event 转成原生格式 Signal。
//
// 关键约束:**返回的载荷必须在同一时间桶内逐字节稳定**。控制面用整个 payload
// 的 sha256 参与推导 signal_id,所以任何随时间变化的字段都会破坏幂等:
//   - 不放 count(它就是递增的那个)
//   - 不放 received_at(控制面自己填)
//   - lastTimestamp 只以**归一化后的桶**出现在 starts_at 里
func ToSignal(ev *corev1.Event, clusterID, tenantID string, now time.Time) Signal {
	t := eventTime(ev)
	if t.IsZero() {
		t = now
	}
	bucket := bucketTime(t)

	obj := ev.InvolvedObject
	labels := map[string]string{
		// alertname 与 rule_id 是下游 ClassifyFault / resourceFromAlertLabels
		// 认的两个标签,给上它们才能走通既有的分类与聚合。
		"alertname": "K8sEvent" + ev.Reason,
		"rule_id":   "k8s-event-" + strings.ToLower(ev.Reason),
		"reason":    ev.Reason,
		"namespace": obj.Namespace,
		// detector 让"主动发现"可区分,与 SLO watcher 同一用途。
		"detector": SourceKubernetes,
		// event_uid 便于人工回查原始 Event。它在同一事件内恒定,不破坏幂等。
		"event_uid": string(ev.UID),
	}
	if obj.Kind != "" {
		labels["kind"] = obj.Kind
	}
	// 刻意**不**在 agent 侧把 Pod 归约到所属工作负载。
	// 控制面有 model.WorkloadName 做这件事,且 blast_radius.services 依赖它
	// (F3)。两处都做归约迟早在某个 Kind 上分叉,而分叉的表现是影响面算错。
	return Signal{
		ClusterID:  clusterID,
		TenantID:   tenantID,
		Source:     SourceKubernetes,
		SignalType: "alert",
		Severity:   severityFor(ev.Reason),
		ResourceRef: ResourceRef{
			Namespace: obj.Namespace,
			Kind:      obj.Kind,
			Name:      obj.Name,
			UID:       string(obj.UID),
		},
		StartsAt: &bucket,
		Labels:   labels,
	}
}
