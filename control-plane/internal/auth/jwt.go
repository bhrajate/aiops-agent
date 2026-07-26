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
type customClaims struct {
	Email      string   `json:"email"`
	Roles      []string `json:"roles"`
	Clusters   []string `json:"clusters"`
	Namespaces []string `json:"namespaces"`
	jwt.RegisteredClaims
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
	return Principal{
		Subject:    claims.Subject,
		Email:      claims.Email,
		Roles:      claims.Roles,
		Clusters:   claims.Clusters,
		Namespaces: claims.Namespaces,
	}, nil
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
