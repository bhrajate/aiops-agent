package auth

// "认证通过但零角色"的告警。
//
// 这是 OIDC 接入时最容易踩、也最难定位的一个状态:token 校验通过,
// 但 Principal 的 Roles 是空的 —— 于是 `Can()` 对任何动作都返 false,
// 该身份在**每个**端点上拿 403。
//
// 难定位的原因是它与"正确地拒绝越权"完全同形:
//   - 日志:认证成功
//   - 审计:某个合法身份被拒(result=denied)
//   - 指标:403 计数上升
//
// 运维看到这些会去查 RBAC 角色配置,而真正的原因通常在 IdP 侧的
// protocol mapper —— 实测真实 Keycloak 的 access token 里顶层没有 `roles`
// (它在 `realm_access.roles`),也完全没有 `clusters`/`namespaces`。
//
// 所以单独报一条,并指名最可能的原因与检查方法。

import (
	"log/slog"
	"sync"
	"time"
)

// noRoleThrottle 控制同一 subject 的告警频率。
//
// 不节流的话,一个配错的身份在轮询界面时会每秒刷几条同样的 WARN,
// 把日志淹掉 —— 而淹掉日志本身会掩盖别的问题。
const noRoleThrottle = 5 * time.Minute

var (
	noRoleMu   sync.Mutex
	noRoleSeen = map[string]time.Time{}
)

// warnNoRoles 报告一个通过认证但没有任何角色的身份。
//
// logger 取全局默认:Authenticator 没有 logger 字段,而为这一条加构造参数
// 会波及所有调用点。slog 的默认 handler 在 main 里已被设成 JSON。
func (a *Authenticator) warnNoRoles(subject string) {
	if !noRoleShouldWarn(subject, time.Now()) {
		return
	}
	slog.Warn("身份通过认证但没有任何角色 —— 它将在每个端点上拿 403",
		"subject", subject,
		"mode", string(a.mode),
		"hint", "OIDC 模式请检查 IdP 的 protocol mapper:token 里需要顶层 "+
			"roles/clusters/namespaces 三个 claim(Keycloak 默认把角色放在 "+
			"realm_access.roles,本系统会自动回落读它,但 clusters/namespaces "+
			"必须由 mapper 显式加上,否则 ABAC 范围为空、同样是全 403)")
}

// noRoleShouldWarn 判断是否该为该 subject 输出告警(按 subject 节流)。
// 抽出来是为了能直接测节流行为,不必依赖日志输出。
func noRoleShouldWarn(subject string, now time.Time) bool {
	noRoleMu.Lock()
	defer noRoleMu.Unlock()
	if last, ok := noRoleSeen[subject]; ok && now.Sub(last) < noRoleThrottle {
		return false
	}
	// 上限保护:异常流量下(比如攻击者用大量随机 sub 的伪造 token)这张表会无界增长。
	// 满了就整张清掉 —— 它只是节流状态,丢了最坏结果是多打几条日志。
	if len(noRoleSeen) > 1024 {
		noRoleSeen = map[string]time.Time{}
	}
	noRoleSeen[subject] = now
	return true
}
