package trigger

import (
	"testing"

	"github.com/aiops/control-plane/internal/model"
)

func TestEvaluateAutoDeterministic(t *testing.T) {
	// P1/P2 必触发
	if d := EvaluateAuto(model.Incident{Severity: "P1"}); !d.Trigger || d.Reason != "severity_p1_p2" {
		t.Errorf("P1 应触发 severity_p1_p2, got %+v", d)
	}
	// 信号突增
	if d := EvaluateAuto(model.Incident{Severity: "P3", SignalCount: 5}); !d.Trigger || d.Reason != "signal_burst" {
		t.Errorf("信号突增应触发 signal_burst, got %+v", d)
	}
	// 发布回归
	if d := EvaluateAuto(model.Incident{Severity: "P4", FaultCategory: "release_regression"}); !d.Trigger || d.Reason != "recent_change_correlation" {
		t.Errorf("发布回归应触发, got %+v", d)
	}
}

func TestStopReason(t *testing.T) {
	if r := StopReason(model.Incident{Status: "resolved"}, false); r == "" {
		t.Error("已解决的 incident 应停止")
	}
	if r := StopReason(model.Incident{Status: "open"}, true); r != "existing_investigation_same_version" {
		t.Errorf("已有同版本活跃调查应停止, got %q", r)
	}
	if r := StopReason(model.Incident{Status: "open"}, false); r != "" {
		t.Errorf("open 且无活跃调查不应停止, got %q", r)
	}
}
