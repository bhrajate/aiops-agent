// Package auth 实现认证(JWT/OIDC)与授权(RBAC/ABAC)。见 docs/SECURITY.md。
package auth

import "slices"

// Principal 是经认证的调用者身份(从 JWT claims 提取)。
type Principal struct {
	Subject    string   `json:"sub"`
	Email      string   `json:"email"`
	Roles      []string `json:"roles"`
	Clusters   []string `json:"clusters"`   // ABAC:可访问集群(含 "*")
	Namespaces []string `json:"namespaces"` // ABAC:可访问命名空间(含 "*")
}

// Action 是受 RBAC 约束的动作。
type Action string

const (
	ActionReadIncident   Action = "read_incident"
	ActionReadEvidence   Action = "read_evidence"
	ActionStartInvestig  Action = "start_investigation"
	ActionCancelInvestig Action = "cancel_investigation"
	ActionFeedback       Action = "feedback"
	// ActionReviewGolden 审核评测用例(反馈闭环)。
	//
	// 刻意**只给 sre/admin**,不给 oncall:批准一条用例意味着它进入评测集,
	// 而评测集决定发布质量门槛 —— 一条错误标注会让门槛失真,且这种失真很难发现
	// (门槛照常通过或照常失败,只是标准错了)。值班人员在故障处置中提交反馈是
	// 本职,但决定"什么算正确答案"应由更少的人负责。
	ActionReviewGolden Action = "review_golden_case"
	// ActionUpdateIncident 变更 Incident 状态(认领 / 标记已解决)。
	//
	// 给 oncall 及以上:认领是值班动作的起点 —— "这个我在看了"必须能被别人看到,
	// 否则同一个 P1 会有三个人同时开始排查。viewer 不给:只读角色改状态会让
	// "谁在负责"这个信息失真,而值班交接完全依赖它。
	ActionUpdateIncident Action = "update_incident"
	// ActionReadAudit 读取审计日志。
	//
	// 只给 sre/admin。审计日志跨 incident、跨命名空间,且含被拒绝访问的
	// 目标 ID 与 scope —— 那本身是敏感信息(能推断出存在哪些资源)。
	// ABAC 在这里没法逐行过滤(记录的是"谁试图访问什么",不都挂在 incident 上),
	// 所以用 RBAC 收紧到少数人,而不是给个过滤不干净的接口。
	ActionReadAudit Action = "read_audit"
)

// rolePermissions 定义 RBAC(SECURITY §2)。viewer ⊂ oncall ⊂ sre ⊂ admin。
var rolePermissions = map[string][]Action{
	"viewer": {ActionReadIncident, ActionReadEvidence},
	"oncall": {ActionReadIncident, ActionReadEvidence, ActionStartInvestig, ActionCancelInvestig,
		ActionFeedback, ActionUpdateIncident},
	"sre": {ActionReadIncident, ActionReadEvidence, ActionStartInvestig, ActionCancelInvestig,
		ActionFeedback, ActionReviewGolden, ActionUpdateIncident, ActionReadAudit},
	"admin": {ActionReadIncident, ActionReadEvidence, ActionStartInvestig, ActionCancelInvestig,
		ActionFeedback, ActionReviewGolden, ActionUpdateIncident, ActionReadAudit},
}

// Can 判断 principal 的角色是否允许某动作(RBAC)。
func (p Principal) Can(a Action) bool {
	for _, role := range p.Roles {
		if perms, ok := rolePermissions[role]; ok && slices.Contains(perms, a) {
			return true
		}
	}
	return false
}

// wildcardOrContains 判断集合是否包含 v 或通配 "*"。
func wildcardOrContains(set []string, v string) bool {
	for _, s := range set {
		if s == "*" || s == v {
			return true
		}
	}
	return false
}

// InScope 判断 principal 是否有权访问给定集群/命名空间(ABAC)。
// sre/admin 视为可访问全部命名空间(SECURITY §2)。
func (p Principal) InScope(cluster, namespace string) bool {
	if !wildcardOrContains(p.Clusters, cluster) {
		return false
	}
	if p.hasAllNamespaces() {
		return true
	}
	if namespace == "" {
		return true // 无命名空间维度的资源
	}
	return wildcardOrContains(p.Namespaces, namespace)
}

func (p Principal) hasAllNamespaces() bool {
	for _, r := range p.Roles {
		if r == "sre" || r == "admin" {
			return true
		}
	}
	return wildcardOrContains(p.Namespaces, "*")
}

// AgentServiceScope 表示 Agent 服务身份的权限范围(每集群只读)。
// 有效权限 = 用户 ∩ Agent ∩ Incident 范围(SECURITY §2)。
type AgentServiceScope struct {
	Clusters []string
}

// EffectiveAccess 计算三者交集:用户可访问 && Agent 可访问 && 命中 Incident 范围。
func EffectiveAccess(user Principal, agent AgentServiceScope, incidentCluster, incidentNamespace string) bool {
	if !user.InScope(incidentCluster, incidentNamespace) {
		return false
	}
	if !wildcardOrContains(agent.Clusters, incidentCluster) {
		return false
	}
	return true
}
