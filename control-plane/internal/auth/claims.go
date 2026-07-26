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
)

// rolePermissions 定义 RBAC(SECURITY §2)。viewer ⊂ oncall ⊂ sre ⊂ admin。
var rolePermissions = map[string][]Action{
	"viewer": {ActionReadIncident, ActionReadEvidence},
	"oncall": {ActionReadIncident, ActionReadEvidence, ActionStartInvestig, ActionCancelInvestig, ActionFeedback},
	"sre":    {ActionReadIncident, ActionReadEvidence, ActionStartInvestig, ActionCancelInvestig, ActionFeedback},
	"admin":  {ActionReadIncident, ActionReadEvidence, ActionStartInvestig, ActionCancelInvestig, ActionFeedback},
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
