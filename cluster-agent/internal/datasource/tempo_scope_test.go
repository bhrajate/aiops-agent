package datasource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 验证 Tempo 查询强制注入 namespace tag、拒绝非法 service(H2)。
func TestTempoSearch_ScopeIsolation(t *testing.T) {
	var gotTags string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTags = r.URL.Query().Get("tags")
		_ = json.NewEncoder(w).Encode(tempoSearchResponse{})
	}))
	defer srv.Close()

	c := &tempoClient{base: srv.URL, hc: srv.Client()}
	scope := Scope{ClusterID: "prod-cn-1", Namespace: "payment"}
	now := time.Now()

	// 正常:注入 namespace tag
	if _, err := c.search(context.Background(), scope, map[string]any{"service": "checkout"}, now.Add(-time.Hour), now); err != nil {
		t.Fatalf("search 报错: %v", err)
	}
	if !strings.Contains(gotTags, "k8s.namespace.name=payment") {
		t.Errorf("Tempo 查询未注入 namespace tag,got tags=%q", gotTags)
	}

	// 越权/注入:非法 service 必须被拒(不能靠覆盖 service 读跨 namespace)
	bad := []any{"other ns", "svc\"inject", "a=b", "../x"}
	for _, s := range bad {
		if _, err := c.search(context.Background(), scope, map[string]any{"service": s}, now.Add(-time.Hour), now); err == nil {
			t.Errorf("非法 service %v 应被拒绝", s)
		}
	}
}

var _ = url.Values{}
