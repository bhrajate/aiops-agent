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

// StopInput 硬停止判断的输入(文档 6.3)。
type StopInput struct {
	Incident             model.Incident
	HasActiveSameVersion bool
	// 冷却:同 incident 上次调查距今秒数;HasPrior=false 表示无历史。
	SecondsSincePrior float64
	HasPrior          bool
	CooldownSec       int // <=0 关闭冷却
	// 并发:该租户当前活跃调查数与上限。
	ActiveCount int
	MaxActive   int // <=0 关闭并发上限
}

// StopReason 硬停止条件(文档 6.3),返回非空表示应停止。
// 已实现:已解决/关闭、同版本已有调查、冷却期内、租户并发上限。
// 维护窗口/静默、租户模型预算仍留接口(接配置源)。
func StopReason(in StopInput) string {
	switch {
	case in.Incident.Status == "resolved" || in.Incident.Status == "closed":
		return "incident_resolved_or_closed"
	case in.HasActiveSameVersion:
		return "existing_investigation_same_version"
	case in.CooldownSec > 0 && in.HasPrior && in.SecondsSincePrior < float64(in.CooldownSec):
		return "cooldown_active"
	case in.MaxActive > 0 && in.ActiveCount >= in.MaxActive:
		return "tenant_concurrency_limit"
	default:
		return ""
	}
}
