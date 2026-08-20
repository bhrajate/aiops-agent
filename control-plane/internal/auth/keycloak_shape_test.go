package auth

// 真实 Keycloak token 形态的回归。
//
// 下面这个 claim 结构是**从真实 Keycloak 26 抓下来的**(realm=aiops,
// client=aiops,用户 alice 带 realm role `sre` 与 user attribute
// clusters/namespaces)。我起了一个真 Keycloak 做接入演练,发现开箱形态与
// 本系统原先的假设有四处不同,其中两处会导致**每个请求都 403**:
//
//	claim                 我们原先假设      Keycloak 实际
//	roles                 顶层数组          在 realm_access.roles 下      ← 全 403
//	clusters/namespaces    顶层数组          不存在(需 protocol mapper)   ← 全 403
//	aud                   字符串            数组 ["aiops","account"]
//	sub                   用户名            UUID                          ← 审计记不了人
//
// 前两处的症状完全一样:token 校验通过、日志显示认证成功、审计显示
// "合法身份被拒",而运维会去查 RBAC 配置 —— 方向完全错了。
//
// 用固化的 claim 结构而不是起真 Keycloak:这条要进 CI,而 CI 里跑
// Keycloak 太重(镜像 + realm 配置 + 用户属性策略)。形态一旦变了,
// 这个 fixture 会过期 —— 所以把抓取方式写在这里,便于日后复核:
//
//	docker run -d -p 8480:8080 -e KC_BOOTSTRAP_ADMIN_USERNAME=admin \
//	  -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin quay.io/keycloak/keycloak:26.0 start-dev
//	# 建 realm/client/user + realm role + 三个 protocol mapper
//	# + realm users/profile 里 unmanagedAttributePolicy=ENABLED
//	#   (Keycloak 26 默认禁用非托管用户属性,不开的话 clusters/namespaces 出不来)
//	# 然后 password grant 拿 access_token 解出 payload

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// keycloakClaims 复刻真实 Keycloak access token 的 claim 形态。
func keycloakClaims(iss string) jwt.MapClaims {
	return jwt.MapClaims{
		"iss": iss,
		// 数组形态的 aud —— 含我们的 audience 与 Keycloak 自带的 "account"
		"aud":                []string{"aiops", "account"},
		"sub":                "181ebcc4-8ee2-41c0-9535-cc3a9be8bbc5", // UUID,不是用户名
		"email":              "alice@corp.example",
		"preferred_username": "alice",
		// 角色在这里,**不在**顶层
		"realm_access": map[string]any{
			"roles": []string{"offline_access", "sre", "uma_authorization", "default-roles-aiops"},
		},
		// 这两个必须由 protocol mapper 显式加,否则不存在
		"clusters":   []string{"*"},
		"namespaces": []string{"*"},
		"exp":        time.Now().Add(time.Hour).Unix(),
		"iat":        time.Now().Add(-time.Minute).Unix(),
	}
}

func TestKeycloakShapeMapsToUsablePrincipal(t *testing.T) {
	idp := newFakeIdP(t)
	v := newVerifier(idp)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, keycloakClaims(testIssuer))
	tok.Header["kid"] = idp.kid
	raw, err := tok.SignedString(idp.key)
	if err != nil {
		t.Fatalf("签发: %v", err)
	}

	p, err := v.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("真实 Keycloak 形态的 token 应通过校验: %v", err)
	}

	// 1) 角色必须从 realm_access.roles 取到 —— 否则 Can() 全 false,每个端点 403
	if !p.Can(ActionReadIncident) {
		t.Errorf("Roles=%v 未授予读权限 —— realm_access.roles 回落失效,"+
			"症状是'登录成功但什么都打不开'", p.Roles)
	}
	if !p.Can(ActionReviewGolden) {
		t.Errorf("sre 应能审核评测用例,Roles=%v", p.Roles)
	}

	// 2) ABAC 范围必须非空 —— 否则 InScope 恒 false,同样是全 403
	if !p.InScope("prod-cn-1", "payment") {
		t.Errorf("Clusters=%v Namespaces=%v —— ABAC 范围为空",
			p.Clusters, p.Namespaces)
	}

	// 3) Subject 必须是可读的用户名。审计与反馈作者都用它,
	//    记 UUID 等于记不了责任人 —— 而问责是审计的全部意义。
	if p.Subject != "alice" {
		t.Errorf("Subject = %q, want alice(应优先 preferred_username 而非 UUID sub)",
			p.Subject)
	}
	if p.Email != "alice@corp.example" {
		t.Errorf("Email = %q", p.Email)
	}
}

func TestKeycloakArrayAudienceAccepted(t *testing.T) {
	// aud 是数组且含无关项("account")。若实现只接受字符串 aud,
	// 或要求 aud 完全等于配置值,真实 IdP 的 token 会被全部拒绝。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	claims := keycloakClaims(testIssuer)
	claims["aud"] = []string{"account", "other-app", "aiops"} // 我们的 aud 不在首位
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = idp.kid
	raw, _ := tok.SignedString(idp.key)

	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatalf("数组 aud 中包含目标 audience 时应通过: %v", err)
	}
}

func TestKeycloakArrayAudienceWithoutOursRejected(t *testing.T) {
	// 反面:数组里没有我们的 audience 就必须拒 ——
	// 否则同一 IdP 给别的系统签的 token 能用来访问本系统。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	claims := keycloakClaims(testIssuer)
	claims["aud"] = []string{"account", "some-other-app"}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = idp.kid
	raw, _ := tok.SignedString(idp.key)

	if _, err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("aud 数组里没有本系统的 audience,必须拒绝")
	}
}

func TestTopLevelRolesTakePrecedence(t *testing.T) {
	// 顶层 roles 存在时优先用它:企业若配了 mapper 把角色平铺到顶层,
	// 那是更明确的意图,不该被 realm_access 里的默认角色覆盖。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	claims := keycloakClaims(testIssuer)
	claims["roles"] = []string{"viewer"} // 顶层只给 viewer
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = idp.kid
	raw, _ := tok.SignedString(idp.key)

	p, err := v.Verify(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Can(ActionStartInvestig) {
		t.Errorf("顶层 roles=[viewer] 应优先,但拿到了更高权限:%v", p.Roles)
	}
}

func TestNoRolesWarningIsThrottledPerSubject(t *testing.T) {
	// "认证通过但零角色"会在每个请求上触发。不节流的话,一个配错的身份
	// 在轮询界面时每秒刷几条同样的 WARN,把日志淹掉 —— 而淹掉日志会掩盖别的问题。
	now := time.Now()
	if !noRoleShouldWarn("bob", now) {
		t.Fatal("首次应告警")
	}
	if noRoleShouldWarn("bob", now.Add(time.Minute)) {
		t.Error("节流窗口内不该重复告警")
	}
	if !noRoleShouldWarn("bob", now.Add(noRoleThrottle+time.Second)) {
		t.Error("超过节流窗口应再次告警")
	}
	// 不同 subject 互不影响 —— 否则一个配错的身份会掩盖另一个
	if !noRoleShouldWarn("carol", now.Add(time.Minute)) {
		t.Error("不同 subject 应各自计时")
	}
}

var _ = rsa.GenerateKey
var _ = rand.Reader
