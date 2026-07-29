package trigger

import (
	"strings"
	"testing"

	"github.com/aiops/control-plane/internal/model"
)

// TestEvaluateAuto_IsNoLongerConstant 是 F7 的核心断言。
//
// 旧实现四个分支**全部返回 true** —— 伪装成策略的常量。这条用例的意义在于:
// 只要有人把它改回"一律触发",测试就会失败。断言的是"这个函数会拒绝某些输入",
// 而不是某个具体阈值。
func TestEvaluateAuto_IsNoLongerConstant(t *testing.T) {
	lowValue := model.Incident{Severity: "P4", SignalCount: 1}
	if dec := EvaluateAuto(lowValue); dec.Trigger {
		t.Fatal("P4 单信号无变更关联应被跳过:否则 EvaluateAuto 仍是常量,每个 incident 都烧一次模型调用")
	}
}

// TestEvaluateAuto_NeverSkipsImportant 反向守卫,比上一条更重要。
//
// 收紧策略的风险是拦掉真问题。这些输入**必须**触发,任何阈值调整都不能改变。
func TestEvaluateAuto_NeverSkipsImportant(t *testing.T) {
	cases := []struct {
		name string
		inc  model.Incident
		want string
	}{
		{"P1", model.Incident{Severity: "P1", SignalCount: 1}, "severity_p1"},
		{"P2", model.Incident{Severity: "P2", SignalCount: 1}, "severity_p2"},
		{"P3 单信号(未列入可跳过)", model.Incident{Severity: "P3", SignalCount: 1}, "default_triage"},
		{"变更关联(release_regression)", model.Incident{Severity: "P4", SignalCount: 1,
			FaultCategory: "release_regression"}, "recent_change_correlation"},
		{"变更关联(change_refs 非空)", model.Incident{Severity: "P4", SignalCount: 1,
			ChangeRefs: []any{map[string]any{"id": "deploy-1"}}}, "recent_change_correlation"},
		{"信号突发", model.Incident{Severity: "P4", SignalCount: 3}, "signal_burst"},
		{"影响面跨服务", model.Incident{Severity: "P4", SignalCount: 1,
			BlastRadius: map[string]any{"services": float64(2)}}, "blast_radius_expanded"},
		{"影响面跨命名空间", model.Incident{Severity: "P4", SignalCount: 1,
			BlastRadius: map[string]any{"namespaces": float64(2)}}, "blast_radius_expanded"},
		{"未知严重度兜底触发", model.Incident{Severity: "", SignalCount: 1}, "default_triage"},
		{"非法严重度兜底触发", model.Incident{Severity: "SEV0", SignalCount: 1}, "default_triage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec := EvaluateAuto(tc.inc)
			if !dec.Trigger {
				t.Fatalf("必须触发,却被跳过(reason=%s)", dec.Reason)
			}
			if dec.Reason != tc.want {
				t.Errorf("reason = %q, want %q(reason 会进审计,用于回答'为什么触发')", dec.Reason, tc.want)
			}
		})
	}
}

// TestEvaluateAuto_SkipReasonIsInformative 跳过时 reason 必须能解释原因。
// 它会写入审计与指标,用于回答"为什么这个故障没有诊断"。
func TestEvaluateAuto_SkipReasonIsInformative(t *testing.T) {
	dec := EvaluateAuto(model.Incident{Severity: "P4", SignalCount: 1})
	if dec.Trigger {
		t.Fatal("应被跳过")
	}
	if dec.Reason == "" {
		t.Fatal("跳过必须给出原因:否则审计里只有一条'没触发',无法排查")
	}
	if !strings.Contains(dec.Reason, "p4") {
		t.Errorf("reason 应包含判定依据,got %q", dec.Reason)
	}
}

// TestEvaluateAuto_TriggerAllRestoresOldBehaviour 回退开关必须真的一律触发。
func TestEvaluateAuto_TriggerAllRestoresOldBehaviour(t *testing.T) {
	cfg := DefaultAutoPolicy()
	cfg.TriggerAll = true
	for _, inc := range []model.Incident{
		{Severity: "P4", SignalCount: 1},
		{Severity: "P3", SignalCount: 0},
		{Severity: ""},
	} {
		if dec := EvaluateAutoWithConfig(inc, cfg); !dec.Trigger {
			t.Errorf("TriggerAll=true 时必须一律触发,%+v 被跳过", inc)
		}
	}
}

// TestEvaluateAuto_ConfigurableThresholds 阈值可配,且配置真的生效。
func TestEvaluateAuto_ConfigurableThresholds(t *testing.T) {
	t.Run("把 P3 加入可跳过集合", func(t *testing.T) {
		cfg := DefaultAutoPolicy()
		cfg.SkipSeverities = map[string]bool{"P3": true, "P4": true}
		if dec := EvaluateAutoWithConfig(model.Incident{Severity: "P3", SignalCount: 1}, cfg); dec.Trigger {
			t.Error("配置后 P3 应可被跳过")
		}
	})
	t.Run("突发阈值调高后不再命中", func(t *testing.T) {
		cfg := DefaultAutoPolicy()
		cfg.BurstSignalCount = 10
		if dec := EvaluateAutoWithConfig(model.Incident{Severity: "P4", SignalCount: 3}, cfg); dec.Trigger {
			t.Errorf("阈值 10 时 3 条信号不应命中突发,reason=%s", dec.Reason)
		}
	})
	t.Run("突发判据可关闭", func(t *testing.T) {
		cfg := DefaultAutoPolicy()
		cfg.BurstSignalCount = 0
		if dec := EvaluateAutoWithConfig(model.Incident{Severity: "P4", SignalCount: 100}, cfg); dec.Trigger {
			t.Errorf("BurstSignalCount<=0 应关闭该判据,reason=%s", dec.Reason)
		}
	})
	t.Run("变更关联判据可关闭", func(t *testing.T) {
		cfg := DefaultAutoPolicy()
		cfg.TriggerOnChangeCorrelation = false
		inc := model.Incident{Severity: "P4", SignalCount: 1, FaultCategory: "release_regression"}
		if dec := EvaluateAutoWithConfig(inc, cfg); dec.Trigger {
			t.Errorf("关闭后不应因变更关联触发,reason=%s", dec.Reason)
		}
	})
	t.Run("提升 P3 为无条件触发", func(t *testing.T) {
		cfg := DefaultAutoPolicy()
		cfg.AlwaysSeverities = map[string]bool{"P1": true, "P2": true, "P3": true}
		dec := EvaluateAutoWithConfig(model.Incident{Severity: "P3", SignalCount: 1}, cfg)
		if !dec.Trigger || dec.Reason != "severity_p3" {
			t.Errorf("P3 应无条件触发,got trigger=%v reason=%s", dec.Trigger, dec.Reason)
		}
	})
}

// TestEvaluateAuto_SeverityCaseInsensitive 上游大小写不一致不该改变判定。
func TestEvaluateAuto_SeverityCaseInsensitive(t *testing.T) {
	for _, sev := range []string{"p1", "P1", " p1 "} {
		dec := EvaluateAuto(model.Incident{Severity: sev, SignalCount: 1})
		if !dec.Trigger || dec.Reason != "severity_p1" {
			t.Errorf("severity=%q 应识别为 P1,got trigger=%v reason=%s", sev, dec.Trigger, dec.Reason)
		}
	}
	// 可跳过集合同样要大小写无关,否则 "p4" 会走兜底触发,策略形同失效。
	if dec := EvaluateAuto(model.Incident{Severity: "p4", SignalCount: 1}); dec.Trigger {
		t.Error("小写 p4 也应被跳过")
	}
}

// TestBlastExpanded_HandlesJSONNumbers blast_radius 来自 JSONB,解码后是 float64;
// 内部直接构造时可能是 int。两者都要认。
func TestBlastExpanded_HandlesJSONNumbers(t *testing.T) {
	cases := []struct {
		blast map[string]any
		want  bool
	}{
		{map[string]any{"services": float64(2)}, true},
		{map[string]any{"services": 2}, true},
		{map[string]any{"services": int64(2)}, true},
		{map[string]any{"services": float64(1)}, false},
		{map[string]any{"namespaces": float64(3)}, true},
		{map[string]any{"services": "2"}, false}, // 字符串不认,避免误判
		{map[string]any{}, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := blastExpanded(tc.blast); got != tc.want {
			t.Errorf("blastExpanded(%v) = %v, want %v", tc.blast, got, tc.want)
		}
	}
}
