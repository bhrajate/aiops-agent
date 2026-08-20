package auth

import (
	"context"
	"testing"
	"time"
)

func TestRBAC(t *testing.T) {
	viewer := Principal{Roles: []string{"viewer"}}
	oncall := Principal{Roles: []string{"oncall"}}

	if !viewer.Can(ActionReadIncident) {
		t.Error("viewer 应能读 incident")
	}
	if viewer.Can(ActionStartInvestig) {
		t.Error("viewer 不应能启动调查")
	}
	if !oncall.Can(ActionStartInvestig) || !oncall.Can(ActionFeedback) {
		t.Error("oncall 应能启动调查与反馈")
	}
}

func TestABACScope(t *testing.T) {
	// bob:仅 prod-cn-1 的 payment/cart
	bob := Principal{Roles: []string{"oncall"}, Clusters: []string{"prod-cn-1"}, Namespaces: []string{"payment", "cart"}}
	if !bob.InScope("prod-cn-1", "payment") {
		t.Error("bob 应可访问 prod-cn-1/payment")
	}
	if bob.InScope("prod-cn-1", "inventory") {
		t.Error("bob 不应访问 inventory")
	}
	if bob.InScope("edge-eu-2", "payment") {
		t.Error("bob 不应访问其他集群")
	}
	// sre:全命名空间
	alice := Principal{Roles: []string{"sre"}, Clusters: []string{"*"}, Namespaces: []string{"*"}}
	if !alice.InScope("any-cluster", "any-ns") {
		t.Error("sre + 通配集群应可访问任意")
	}
}

func TestEffectiveAccessIntersection(t *testing.T) {
	// 用户能访问,但 Agent 服务身份不覆盖该集群 → 拒绝(三者交集)
	user := Principal{Roles: []string{"sre"}, Clusters: []string{"*"}, Namespaces: []string{"*"}}
	agent := AgentServiceScope{Clusters: []string{"prod-cn-1"}}
	if !EffectiveAccess(user, agent, "prod-cn-1", "payment") {
		t.Error("集群在三者交集内应允许")
	}
	if EffectiveAccess(user, agent, "edge-eu-2", "payment") {
		t.Error("Agent 不覆盖 edge-eu-2,应拒绝(即便用户是 * )")
	}
}

// 新增两个动作的角色边界。这些是"给多了也不会报错"的地方 ——
// 权限放宽不会有任何测试失败、任何日志告警,只会在事后审计时才发现。
func TestRBACWorkbenchActions(t *testing.T) {
	viewer := Principal{Roles: []string{"viewer"}}
	oncall := Principal{Roles: []string{"oncall"}}
	sre := Principal{Roles: []string{"sre"}}
	admin := Principal{Roles: []string{"admin"}}

	// 认领 / 标记已解决:oncall 起。viewer 改状态会让"谁在负责"失真,
	// 而值班交接完全依赖这个信息。
	if viewer.Can(ActionUpdateIncident) {
		t.Error("viewer 不应能变更 incident 状态")
	}
	for _, p := range []Principal{oncall, sre, admin} {
		if !p.Can(ActionUpdateIncident) {
			t.Errorf("%v 应能变更 incident 状态", p.Roles)
		}
	}

	// 审计日志:只给 sre/admin。它跨命名空间且含被拒绝访问的目标 ID,
	// 那本身能推断出存在哪些资源。
	for _, p := range []Principal{viewer, oncall} {
		if p.Can(ActionReadAudit) {
			t.Errorf("%v 不应能读审计日志", p.Roles)
		}
	}
	for _, p := range []Principal{sre, admin} {
		if !p.Can(ActionReadAudit) {
			t.Errorf("%v 应能读审计日志", p.Roles)
		}
	}
}

// 角色是包含关系:viewer ⊂ oncall ⊂ sre ⊂ admin(SECURITY §2)。
// 断言这个不变量,避免后续加动作时只补了 sre 忘了 admin ——
// 那会造成 admin 比 sre 权限更小,而没人会想到去测这个方向。
func TestRoleHierarchyIsNested(t *testing.T) {
	chain := []string{"viewer", "oncall", "sre", "admin"}
	for i := 1; i < len(chain); i++ {
		lower := Principal{Roles: []string{chain[i-1]}}
		higher := Principal{Roles: []string{chain[i]}}
		for _, a := range rolePermissions[chain[i-1]] {
			if !higher.Can(a) {
				t.Errorf("%s 拥有 %q 但 %s 没有 —— 角色包含关系被破坏",
					chain[i-1], a, chain[i])
			}
			_ = lower
		}
	}
}

func TestHS256IssueAndVerify(t *testing.T) {
	a := NewAuthenticator(Config{Mode: ModeHS256, HS256Key: "test-secret", Issuer: "aiops-dev", Audience: "aiops"})
	p := Principal{Subject: "alice", Roles: []string{"sre"}, Clusters: []string{"*"}, Namespaces: []string{"*"}}
	tok, err := a.Issue(p, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Authenticate(context.Background(), "Bearer "+tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Subject != "alice" || !got.Can(ActionStartInvestig) {
		t.Errorf("round-trip claims mismatch: %+v", got)
	}
}

func TestRejectBadToken(t *testing.T) {
	a := NewAuthenticator(Config{Mode: ModeHS256, HS256Key: "secret", Issuer: "aiops-dev", Audience: "aiops"})
	if _, err := a.Authenticate(context.Background(), ""); err == nil {
		t.Error("空 header 应拒绝")
	}
	if _, err := a.Authenticate(context.Background(), "Bearer garbage"); err == nil {
		t.Error("垃圾 token 应拒绝")
	}
	// 错误密钥签发的 token 应被拒
	other := NewAuthenticator(Config{Mode: ModeHS256, HS256Key: "other", Issuer: "aiops-dev", Audience: "aiops"})
	tok, _ := other.Issue(Principal{Subject: "x"}, time.Hour)
	if _, err := a.Authenticate(context.Background(), "Bearer "+tok); err == nil {
		t.Error("异密钥 token 应拒绝")
	}
}

func TestDevUserLogin(t *testing.T) {
	users := DefaultDevUsers()
	if _, ok := VerifyDevUser(users, "alice", "alice-pass"); !ok {
		t.Error("alice 正确密码应通过")
	}
	if _, ok := VerifyDevUser(users, "alice", "wrong"); ok {
		t.Error("错误密码应失败")
	}
	if _, ok := VerifyDevUser(users, "nobody", "x"); ok {
		t.Error("不存在用户应失败")
	}
}
