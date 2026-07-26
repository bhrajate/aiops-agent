package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func doReq(h http.Handler, path, token string) int {
	r := httptest.NewRequest(http.MethodPost, path, nil)
	if token != "" {
		r.Header.Set("X-Internal-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestInternalToken_ProdEmptyDenies(t *testing.T) {
	// 生产模式 requireToken=true 且配置 token 为空:一律拒绝(纵深防御)
	h := RequireInternalToken("", true, okHandler())
	if code := doReq(h, "/internal/x", ""); code != http.StatusUnauthorized {
		t.Errorf("生产模式空 token 应拒绝,got %d", code)
	}
}

func TestInternalToken_DevEmptyAllows(t *testing.T) {
	// 开发模式 requireToken=false 且未配置:放行
	h := RequireInternalToken("", false, okHandler())
	if code := doReq(h, "/internal/x", ""); code != http.StatusOK {
		t.Errorf("开发模式空 token 应放行,got %d", code)
	}
}

func TestInternalToken_MatchAndMismatch(t *testing.T) {
	h := RequireInternalToken("secret-token", true, okHandler())
	if code := doReq(h, "/internal/x", "secret-token"); code != http.StatusOK {
		t.Errorf("正确 token 应放行,got %d", code)
	}
	if code := doReq(h, "/internal/x", "wrong"); code != http.StatusUnauthorized {
		t.Errorf("错误 token 应拒绝,got %d", code)
	}
	if code := doReq(h, "/internal/x", ""); code != http.StatusUnauthorized {
		t.Errorf("缺 token 应拒绝,got %d", code)
	}
}

func TestInternalToken_HealthMetricsBypass(t *testing.T) {
	h := RequireInternalToken("secret-token", true, okHandler())
	if code := doReq(h, "/healthz", ""); code != http.StatusOK {
		t.Errorf("/healthz 应免鉴权,got %d", code)
	}
	if code := doReq(h, "/metrics", ""); code != http.StatusOK {
		t.Errorf("/metrics 应免鉴权,got %d", code)
	}
}
