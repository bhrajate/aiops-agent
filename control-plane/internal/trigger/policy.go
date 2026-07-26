// Package trigger 实现 Trigger Policy Engine(文档 6.3):
// 确定性判断是否深度 RCA、是否命中硬停止条件。这些判断不交给 LLM。
package trigger

import "github.com/aiops/control-plane/internal/model"

// PolicyVersion 记录到 investigation,便于审计与回归。
const PolicyVersion = "trigger-policy/v1"

// Decision 触发决策。
type Decision struct {
	Trigger bool
	Reason  string
}

// EvaluateAuto 针对自动流程(由 incidents 事件驱动)判断是否发起调查。
// 快速分诊默认触发;此处判断"是否值得启动一次带深度 RCA 能力的调查"。
func EvaluateAuto(inc model.Incident) Decision {
	// 深度 RCA 建议条件(满足其一)——文档 6.3
	switch {
	case inc.Severity == "P1" || inc.Severity == "P2":
		return Decision{true, "severity_p1_p2"}
	case inc.SignalCount >= 3:
		return Decision{true, "signal_burst"} // 异常持续/影响面扩大的近似
	case inc.FaultCategory == "release_regression":
		return Decision{true, "recent_change_correlation"}
	default:
		// P3/P4 且信号少:首版仍启动快速分诊(Workflow 内部会决定是否深挖)
		return Decision{true, "auto_triage"}
	}
}

// StopReason 硬停止条件(文档 6.3),返回非空表示应停止。
func StopReason(inc model.Incident, hasActiveSameVersion bool) string {
	switch {
	case inc.Status == "resolved" || inc.Status == "closed":
		return "incident_resolved_or_closed"
	case hasActiveSameVersion:
		return "existing_investigation_same_version"
	default:
		return ""
	}
	// 说明:维护窗口/静默、预算耗尽、冷却、并发上限等在生产中接入配置源;
	// 首版实现最关键的两条,其余留接口(见 README 的 TODO)。
}
