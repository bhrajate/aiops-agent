package auth

import (
	"context"
	"crypto/subtle"
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
		// token 校验通过但一个角色都没有 —— 这个身份接下来会在**每个**端点上拿 403,
		// 而那与"正确地拒绝越权"在日志和审计里完全同形。
		//
		// 实测(真实 Keycloak):开箱的 access token 顶层没有 roles、
		// 也没有 clusters/namespaces。若 IdP 侧的 protocol mapper 没配对,
		// 症状就是"登录成功但什么都打不开",而运维会去查 RBAC 配置 ——
		// 方向完全错了。所以在这里单独报出来,并指名最可能的原因。
		if len(p.Roles) == 0 {
			a.warnNoRoles(p.Subject)
		}
		ctx := context.WithValue(r.Context(), principalKey, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireInternalToken 保护内部 API(SECURITY §2):恒定时间校验共享密钥头。
// requireToken=true 时,即使配置的 token 为空也一律拒绝(生产模式,防止误配静默放行)。
func RequireInternalToken(token string, requireToken bool, next http.Handler) http.Handler {
	expected := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		if token == "" {
			if requireToken {
				// 生产:未配置 token 视为拒绝(不应发生,启动校验已拦截;此处纵深防御)
				writeErr(w, http.StatusUnauthorized, "unauthorized", "internal token not configured")
				return
			}
			// 开发:未配置则放行(兼容本地无鉴权联调)
			next.ServeHTTP(w, r)
			return
		}
		got := []byte(r.Header.Get("X-Internal-Token"))
		if subtle.ConstantTimeCompare(got, expected) != 1 {
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
