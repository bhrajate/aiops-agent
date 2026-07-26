package auth

import (
	"context"
	"encoding/json"
	"net/http"
)

type ctxKey int

const principalKey ctxKey = 0

// FromContext 取出当前请求的 Principal。
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}

// Middleware 认证中间件:校验 Bearer token,注入 Principal 到 context。
// 失败返回 401。放行 skip 列表(如 /healthz、/v1/auth/login、/v1/signals 自有鉴权)。
func (a *Authenticator) Middleware(skip func(r *http.Request) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skip != nil && skip(r) {
			next.ServeHTTP(w, r)
			return
		}
		p, err := a.Authenticate(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), principalKey, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireInternalToken 保护内部 API(SECURITY §2):校验共享密钥头。
func RequireInternalToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		if token != "" && r.Header.Get("X-Internal-Token") != token {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid internal token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	var b struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	b.Error.Code = code
	b.Error.Message = msg
	_ = json.NewEncoder(w).Encode(b)
}
