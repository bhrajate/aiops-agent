package trigger

import (
	"testing"

	"github.com/aiops/control-plane/internal/model"
)

func TestEvaluateAutoDeterministic(t *testing.T) {
	// P1/P2 必触发。reason 拆成 severity_p1 / severity_p2(原为合并的
	// severity_p1_p2):它落到 investigations.trigger_reason 供审计,
	// 分开能直接看出是哪一级触发的,不必再回查 incident。
	if d := EvaluateAuto(model.Incident{Severity: "P1"}); !d.Trigger || d.Reason != "severity_p1" {
		t.Errorf("P1 应触发 severity_p1, got %+v", d)
	}
	if d := EvaluateAuto(model.Incident{Severity: "P2"}); !d.Trigger || d.Reason != "severity_p2" {
		t.Errorf("P2 应触发 severity_p2, got %+v", d)
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
	open := model.Incident{Status: "open"}
	if r := StopReason(StopInput{Incident: model.Incident{Status: "resolved"}}); r == "" {
		t.Error("已解决的 incident 应停止")
	}
	if r := StopReason(StopInput{Incident: open, HasActiveSameVersion: true}); r != "existing_investigation_same_version" {
		t.Errorf("已有同版本活跃调查应停止, got %q", r)
	}
	if r := StopReason(StopInput{Incident: open}); r != "" {
		t.Errorf("open 且无约束不应停止, got %q", r)
	}
	// 冷却期内应停止
	if r := StopReason(StopInput{Incident: open, HasPrior: true, SecondsSincePrior: 60, CooldownSec: 300}); r != "cooldown_active" {
		t.Errorf("冷却期内应停止, got %q", r)
	}
	// 超过冷却期不停止
	if r := StopReason(StopInput{Incident: open, HasPrior: true, SecondsSincePrior: 400, CooldownSec: 300}); r != "" {
		t.Errorf("超过冷却期不应停止, got %q", r)
	}
	// 并发达上限应停止
	if r := StopReason(StopInput{Incident: open, ActiveCount: 20, MaxActive: 20}); r != "tenant_concurrency_limit" {
		t.Errorf("并发上限应停止, got %q", r)
	}
	// 并发未达上限不停止
	if r := StopReason(StopInput{Incident: open, ActiveCount: 5, MaxActive: 20}); r != "" {
		t.Errorf("并发未达上限不应停止, got %q", r)
	}
}
