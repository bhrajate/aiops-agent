package eventwatch

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ev(t, reason, ns string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e", Namespace: ns},
		Type:           t,
		Reason:         reason,
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p", Namespace: ns},
	}
}

func TestFilterDropsNormalEvents(t *testing.T) {
	// 全量转发是灾难:每次 Scheduled/Pulled/Created/Started 都进来。
	f := NewFilter(nil, nil)
	for _, r := range []string{"Scheduled", "Pulled", "Created", "Started"} {
		if f.Allow(ev(corev1.EventTypeNormal, r, "payment")) {
			t.Errorf("Normal/%s 不该放行", r)
		}
	}
}

func TestFilterIsDefaultDeny(t *testing.T) {
	// 未知 reason 一律不报,而不是"不在黑名单就报"。
	// 后者在 K8s 新增 reason 时会突然开始刷新事件类型,而没人会预料到。
	f := NewFilter(nil, nil)
	if f.Allow(ev(corev1.EventTypeWarning, "SomeBrandNewReason", "payment")) {
		t.Error("未知 reason 不该放行(必须默认拒绝)")
	}
}

func TestFilterExcludesUnhealthyByDefault(t *testing.T) {
	// Unhealthy 在滚动发布期间持续刷(探针在新 Pod 就绪前失败是正常的)。
	// 放进默认名单会把每次正常发布变成一串 signal,而值班人员学会忽略它之后,
	// 真正的探针故障也就看不见了。
	f := NewFilter(nil, nil)
	if f.Allow(ev(corev1.EventTypeWarning, "Unhealthy", "payment")) {
		t.Error("Unhealthy 不该在默认白名单里")
	}
	// 但显式配置时要能放行
	if !NewFilter([]string{"Unhealthy"}, nil).Allow(ev(corev1.EventTypeWarning, "Unhealthy", "payment")) {
		t.Error("显式配了 Unhealthy 应放行")
	}
}

func TestFilterAllowsDefaultFaultReasons(t *testing.T) {
	f := NewFilter(nil, nil)
	for _, r := range DefaultReasons {
		if !f.Allow(ev(corev1.EventTypeWarning, r, "payment")) {
			t.Errorf("默认白名单里的 %s 应放行", r)
		}
	}
}

func TestFilterNamespaceScope(t *testing.T) {
	f := NewFilter(nil, []string{"payment", "cart"})
	if !f.Allow(ev(corev1.EventTypeWarning, "OOMKilling", "payment")) {
		t.Error("范围内的 namespace 应放行")
	}
	if f.Allow(ev(corev1.EventTypeWarning, "OOMKilling", "kube-system")) {
		t.Error("范围外的 namespace 不该放行")
	}
	// 空范围 = 不限
	if !NewFilter(nil, nil).Allow(ev(corev1.EventTypeWarning, "OOMKilling", "anything")) {
		t.Error("未配范围时应不限 namespace")
	}
}

func TestFilterNilSafe(t *testing.T) {
	if NewFilter(nil, nil).Allow(nil) {
		t.Error("nil 事件不该放行")
	}
}

func TestFilterTypeIsCaseInsensitive(t *testing.T) {
	// K8s 里 Type 是 "Warning",但不同版本/客户端的大小写不完全一致。
	// 大小写敏感会让筛选静默漏掉全部事件 —— 而"一条都没上报"看起来
	// 和"集群很健康"一样。
	f := NewFilter(nil, nil)
	if !f.Allow(ev("warning", "OOMKilling", "payment")) {
		t.Error("type 大小写不该影响判定")
	}
}
