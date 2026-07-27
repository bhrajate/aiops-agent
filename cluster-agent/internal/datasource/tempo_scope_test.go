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
	if _, err := c.search(context.Background(), scope, map[string]any{"service": "checkout"}, now.Add(-time.Hour), now, ScopeLabel{}); err != nil {
		t.Fatalf("search 报错: %v", err)
	}
	if !strings.Contains(gotTags, "k8s.namespace.name=payment") {
		t.Errorf("Tempo 查询未注入 namespace tag,got tags=%q", gotTags)
	}

	// 越权/注入:非法 service 必须被拒(不能靠覆盖 service 读跨 namespace)
	bad := []any{"other ns", "svc\"inject", "a=b", "../x"}
	for _, s := range bad {
		if _, err := c.search(context.Background(), scope, map[string]any{"service": s}, now.Add(-time.Hour), now, ScopeLabel{}); err == nil {
			t.Errorf("非法 service %v 应被拒绝", s)
		}
	}
}

// 验证 Tempo 取最慢 trace 的 span 详情,定位最慢 span(P2)。
func TestTempoSearch_SpanDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/search"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"traces": []map[string]any{
					{"traceID": "abc123", "rootServiceName": "checkout", "rootTraceName": "GET /pay", "durationMs": 2100},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/api/traces/"):
			// 两个 span:上游 100ms,下游 db.query 1900ms(应被选为最慢)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"batches": []map[string]any{
					{
						"resource": map[string]any{"attributes": []map[string]any{
							{"key": "service.name", "value": map[string]any{"stringValue": "auth-service"}},
						}},
						"scopeSpans": []map[string]any{
							{"spans": []map[string]any{
								{"name": "db.query", "startTimeUnixNano": "1000000000", "endTimeUnixNano": "1901000000"},
							}},
						},
					},
				},
			})
		}
	}))
	defer srv.Close()

	c := &tempoClient{base: srv.URL, hc: srv.Client()}
	scope := Scope{ClusterID: "prod-cn-1", Namespace: "payment"}
	now := time.Now()
	res, err := c.search(context.Background(), scope, map[string]any{"service": "checkout"}, now.Add(-time.Hour), now, ScopeLabel{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(res.Summary, "db.query") || !strings.Contains(res.Summary, "auth-service") {
		t.Errorf("summary 应含最慢 span db.query@auth-service,got %q", res.Summary)
	}
	ss, _ := res.Raw.(map[string]any)["slowest_span"].(map[string]any)
	if ss == nil || ss["name"] != "db.query" || ss["duration_ms"].(int) != 901 {
		t.Errorf("slowest_span 定位错误: %+v", ss)
	}
}

func TestIsHex(t *testing.T) {
	if !isHex("abc123DEF") {
		t.Error("合法 hex 应通过")
	}
	for _, bad := range []string{"", "../etc", "abc/xyz", "g123"} {
		if isHex(bad) {
			t.Errorf("非法 trace id %q 应拒绝", bad)
		}
	}
}

var _ = url.Values{}
