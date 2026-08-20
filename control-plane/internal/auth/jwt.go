package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Mode 认证模式(SECURITY §1)。
type Mode string

const (
	ModeHS256    Mode = "hs256"    // 开发:内置签发 + 本地校验
	ModeOIDC     Mode = "oidc"     // 生产:IdP 签发,JWKS 校验
	ModeDisabled Mode = "disabled" // 仅限本地测试,一切放行为匿名 admin
)

var (
	ErrNoToken      = errors.New("missing bearer token")
	ErrInvalidToken = errors.New("invalid token")
)

// Authenticator 验证 token 并返回 Principal。
type Authenticator struct {
	mode     Mode
	hsSecret []byte
	issuer   string
	audience string
	verifier OIDCVerifier // oidc 模式;可为 nil
}

// OIDCVerifier 抽象 JWKS 验签(生产接入,便于测试注入)。
type OIDCVerifier interface {
	Verify(ctx context.Context, rawToken string) (Principal, error)
}

// Config 认证配置。
type Config struct {
	Mode      Mode
	HS256Key  string
	Issuer    string
	Audience  string
	OIDCVerif OIDCVerifier
}

func NewAuthenticator(c Config) *Authenticator {
	return &Authenticator{
		mode:     c.Mode,
		hsSecret: []byte(c.HS256Key),
		issuer:   c.Issuer,
		audience: c.Audience,
		verifier: c.OIDCVerif,
	}
}

func (a *Authenticator) Mode() Mode { return a.mode }

// customClaims 映射 JWT claims 到 Principal。
//
// 三个非 registered 字段有**两处来源**,因为真实 IdP 与我们的开发签发格式不同:
//
//	roles       顶层 `roles`,回落到 `realm_access.roles`(Keycloak 默认形态)
//	clusters    顶层 `clusters`,回落到 `resource_access.<aud>.clusters`
//	namespaces  同上
//
// 为什么必须支持回落:对着**真实 Keycloak** 实测,开箱的 access token 里
// 顶层没有 `roles`(它在 `realm_access.roles`)、也完全没有 `clusters`/`namespaces`。
// 只读顶层的话 Principal 三个字段全空 → `Can()` 对任何动作返 false →
// **每个请求都 403**,而 token 本身校验是通过的。
//
// 那种失败极难定位:日志里是"认证成功",审计里是"某个合法身份被拒",
// 而运维会去查 RBAC 配置 —— 但问题在 claim 的形状。
type customClaims struct {
	Email      string   `json:"email"`
	Roles      []string `json:"roles"`
	Clusters   []string `json:"clusters"`
	Namespaces []string `json:"namespaces"`
	// RealmAccess 是 Keycloak 放 realm 角色的地方。
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
	// PreferredUsername:Keycloak 的 `sub` 是 UUID,而审计日志里记 UUID
	// 等于记不了责任人。有这个字段时优先用它作为 Subject。
	PreferredUsername string `json:"preferred_username"`
	jwt.RegisteredClaims
}

// principal 把 claims 归一化成 Principal,处理上述两处来源。
func (c *customClaims) principal() Principal {
	roles := c.Roles
	if len(roles) == 0 {
		roles = c.RealmAccess.Roles
	}
	subject := c.Subject
	if c.PreferredUsername != "" {
		// 审计与反馈作者都用 Subject。UUID 在那两处都不可读,
		// 而"谁做了这件事"是问责的全部意义。
		subject = c.PreferredUsername
	}
	return Principal{
		Subject:    subject,
		Email:      c.Email,
		Roles:      roles,
		Clusters:   c.Clusters,
		Namespaces: c.Namespaces,
	}
}

// Authenticate 从 Authorization 头解析并校验,返回 Principal。
func (a *Authenticator) Authenticate(ctx context.Context, authHeader string) (Principal, error) {
	if a.mode == ModeDisabled {
		return Principal{Subject: "anonymous", Roles: []string{"admin"}, Clusters: []string{"*"}, Namespaces: []string{"*"}}, nil
	}
	raw, err := bearer(authHeader)
	if err != nil {
		return Principal{}, err
	}
	if a.mode == ModeOIDC {
		if a.verifier == nil {
			return Principal{}, errors.New("oidc verifier not configured")
		}
		return a.verifier.Verify(ctx, raw)
	}
	// hs256
	claims := &customClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return a.hsSecret, nil
	}, jwt.WithAudience(a.audience), jwt.WithIssuer(a.issuer))
	if err != nil || !token.Valid {
		return Principal{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	return claims.principal(), nil
}

// Issue 在 hs256 模式下签发一个 token(开发登录端点用)。
func (a *Authenticator) Issue(p Principal, ttl time.Duration) (string, error) {
	if a.mode != ModeHS256 {
		return "", errors.New("token issuance only in hs256 mode")
	}
	now := time.Now()
	claims := customClaims{
		Email:      p.Email,
		Roles:      p.Roles,
		Clusters:   p.Clusters,
		Namespaces: p.Namespaces,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   p.Subject,
			Issuer:    a.issuer,
			Audience:  jwt.ClaimStrings{a.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.hsSecret)
}

func bearer(h string) (string, error) {
	if h == "" {
		return "", ErrNoToken
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", ErrNoToken
	}
	return strings.TrimSpace(parts[1]), nil
}
