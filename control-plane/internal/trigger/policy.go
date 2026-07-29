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

// EvaluateAuto 用默认阈值判断是否自动发起调查。
//
// 旧实现四个分支全返回 true —— 伪装成策略的常量(F7)。真正的判据见
// autopolicy.go,阈值可配;此函数保留为默认配置下的便捷入口。
func EvaluateAuto(inc model.Incident) Decision {
	return EvaluateAutoWithConfig(inc, DefaultAutoPolicy())
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
