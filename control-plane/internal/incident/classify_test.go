package incident

import (
	"testing"

	"github.com/aiops/control-plane/internal/model"
)

// TestClassifyFault_LabelNamesMustNotMatch 是这一处缺陷的核心断言。
//
// 原实现把 labelBlob 拼成 "key=value key=value ..." 后做子串匹配,于是**标签名本身**
// 参与判定。而几乎每条真实 K8s 告警都带 `deployment=<名字>`,其中含 "deploy",
// 于是无条件命中 release_regression 分支。
//
// 后果有两层:
//  1. fault_category 是 EvaluateAuto 变更关联判据的输入 —— 于是**每个 incident
//     都因"变更关联"被触发**,F7 的策略形同失效(实测就是这样发现的:
//     指标里只有 recent_change_correlation 一种 reason);
//  2. fault_category 会下发给 planner,把 RCA 的先验偏向"发布回归",
//     而真实根因可能完全无关。
//
// 现有测试只用 alertname 标签,结构性地碰不到这个情形。
func TestClassifyFault_LabelNamesMustNotMatch(t *testing.T) {
	// 一条最普通的 CPU 告警,带真实 K8s 告警必有的 deployment 标签。
	sig := model.Signal{
		SignalType: "alert",
		Labels: map[string]string{
			"alertname":  "HighCPUUsage",
			"deployment": "checkout",
			"namespace":  "payment",
			"severity":   "warning",
		},
	}
	got := ClassifyFault(sig)
	if got == "release_regression" {
		t.Errorf("deployment 标签名不该让 CPU 告警变成发布回归:got %q", got)
	}
	if got != "resource" {
		t.Errorf("CPU 告警应归为 resource,got %q", got)
	}
}

// TestClassifyFault_ValueStillMatches 标签**值**里的关键词仍应生效。
// 修复不能把判据一起废掉。
func TestClassifyFault_ValueStillMatches(t *testing.T) {
	cases := []struct {
		name string
		sig  model.Signal
		want string
	}{
		{"change 类型", model.Signal{SignalType: "change",
			Labels: map[string]string{"deployment": "checkout"}}, "release_regression"},
		{"reason 值含 release", model.Signal{SignalType: "alert",
			Labels: map[string]string{"alertname": "X", "reason": "ReleaseRollout"}}, "release_regression"},
		{"alertname 含 rollout", model.Signal{SignalType: "alert",
			Labels: map[string]string{"alertname": "RolloutStuck", "deployment": "cart"}}, "release_regression"},
		{"资源类", model.Signal{SignalType: "alert",
			Labels: map[string]string{"alertname": "MemoryThrottled", "deployment": "cart"}}, "resource"},
		{"依赖类", model.Signal{SignalType: "alert",
			Labels: map[string]string{"alertname": "UpstreamTimeout", "deployment": "cart"}}, "dependency"},
		{"Pod 类", model.Signal{SignalType: "alert",
			Labels: map[string]string{"alertname": "PodCrashLoopBackOff", "deployment": "cart"}}, "pod_workload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyFault(tc.sig); got != tc.want {
				t.Errorf("ClassifyFault = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClassifyFault_KindStillMatches 资源类型(Kind)参与判定是合理的:
// 它是值不是标签名。
func TestClassifyFault_KindStillMatches(t *testing.T) {
	sig := model.Signal{
		SignalType:  "alert",
		ResourceRef: model.ResourceRef{Kind: "Pod", Name: "checkout-abc12"},
		Labels:      map[string]string{"alertname": "SomethingOdd"},
	}
	if got := ClassifyFault(sig); got != "pod_workload" {
		t.Errorf("Kind=Pod 应归为 pod_workload,got %q", got)
	}
}

// TestClassifyFault_NamespaceValueNotMisread 命名空间**值**恰好含关键词时,
// 不该被误判。这类误判在生产里很难发现:namespace 叫 "deploy-tools" 的团队,
// 所有告警都会被当成发布回归。
func TestClassifyFault_NamespaceValueNotMisread(t *testing.T) {
	sig := model.Signal{
		SignalType: "alert",
		Labels: map[string]string{
			"alertname": "PodCrashLoopBackOff",
			"namespace": "deploy-tools", // 值里含 deploy
			"pod":       "worker-1",
		},
	}
	// CrashLoop 是明确的 pod_workload 信号,不该被 namespace 的名字盖过。
	if got := ClassifyFault(sig); got != "pod_workload" {
		t.Errorf("CrashLoop 应归为 pod_workload,不该被 namespace 名字盖过,got %q", got)
	}
}
