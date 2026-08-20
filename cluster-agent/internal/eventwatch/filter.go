package eventwatch

// 事件筛选。**默认拒绝**。
//
// 全量转发是灾难:每次 Scheduled / Pulled / Created / Started 都进来。
// 而只按 type=Warning 也不够 —— Unhealthy 在滚动发布期间会持续刷
// (探针在新 Pod 就绪前失败是正常的),那会把每次正常发布变成一串 signal,
// 而值班人员学会忽略它之后,真正的探针故障也就看不见了。

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// DefaultReasons 是默认放行的 reason 白名单:按"确实指示故障"筛。
//
// Unhealthy 刻意**不在**默认名单里(理由见文件头)。需要它的集群显式配。
var DefaultReasons = []string{
	"OOMKilling",
	"Evicted",
	"FailedScheduling",
	"FailedMount",
	"FailedAttachVolume",
	"BackOff",
	"ErrImagePull",
	"Failed",
	"FailedCreatePodSandBox",
	"NodeNotReady",
	"Preempted",
}

// Filter 决定一条事件是否上报。
type Filter struct {
	reasons    map[string]bool
	namespaces map[string]bool // 空 = 全部
}

// NewFilter 构造筛选器。reasons 为空时用 DefaultReasons;
// namespaces 为空表示不限命名空间。
func NewFilter(reasons, namespaces []string) *Filter {
	f := &Filter{reasons: map[string]bool{}, namespaces: map[string]bool{}}
	if len(reasons) == 0 {
		reasons = DefaultReasons
	}
	for _, r := range reasons {
		if r = strings.TrimSpace(r); r != "" {
			f.reasons[r] = true
		}
	}
	for _, n := range namespaces {
		if n = strings.TrimSpace(n); n != "" {
			f.namespaces[n] = true
		}
	}
	return f
}

// Allow 判断事件是否应上报。
//
// 三个条件全部满足才放行:type=Warning、reason 在白名单、namespace 在范围内。
// 顺序无关紧要(都是 O(1)),但**默认拒绝**这一点很重要:
// 未知 reason 一律不报,而不是"不在黑名单就报"。后者在 K8s 新增 reason 时
// 会突然开始刷新的事件类型,而没人会预料到。
func (f *Filter) Allow(ev *corev1.Event) bool {
	if ev == nil {
		return false
	}
	if !strings.EqualFold(ev.Type, corev1.EventTypeWarning) {
		return false
	}
	if !f.reasons[ev.Reason] {
		return false
	}
	if len(f.namespaces) > 0 && !f.namespaces[ev.InvolvedObject.Namespace] {
		return false
	}
	return true
}
