package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// DevUser 开发用户(仅 hs256 模式的本地登录端点使用;生产由 IdP 负责)。
type DevUser struct {
	Username     string
	PasswordHash string // sha256 十六进制串
	Principal    Principal
}

// DefaultDevUsers 演示账号。密码即用户名后缀 "-pass"(如 alice / alice-pass)。
// 仅用于本地端到端演示,切勿用于生产。
func DefaultDevUsers() map[string]DevUser {
	mk := func(user, pw string, roles, clusters, ns []string) DevUser {
		return DevUser{
			Username:     user,
			PasswordHash: sha256hex(pw),
			Principal: Principal{
				Subject: user, Email: user + "@corp.example",
				Roles: roles, Clusters: clusters, Namespaces: ns,
			},
		}
	}
	return map[string]DevUser{
		"alice":  mk("alice", "alice-pass", []string{"sre"}, []string{"*"}, []string{"*"}),
		"bob":    mk("bob", "bob-pass", []string{"oncall"}, []string{"prod-cn-1"}, []string{"payment", "cart"}),
		"viewer": mk("viewer", "viewer-pass", []string{"viewer"}, []string{"prod-cn-1"}, []string{"payment"}),
	}
}

// Verify 校验用户名密码,返回 Principal。
func VerifyDevUser(users map[string]DevUser, username, password string) (Principal, bool) {
	u, ok := users[username]
	if !ok {
		return Principal{}, false
	}
	if subtle.ConstantTimeCompare([]byte(u.PasswordHash), []byte(sha256hex(password))) != 1 {
		return Principal{}, false
	}
	return u.Principal, true
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
