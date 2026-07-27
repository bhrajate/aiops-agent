package obsquery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func liveScope() Scope {
	return Scope{ClusterID: "prod-cn-1", Namespace: "payment", Resource: ResourceRef{Name: "checkout"}}
}

func TestLivePrometheusQueryRange(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"version":"v2.3.0"},"values":[[1690000000,"0.01"],[1690000060,"0.082"]]},
			{"metric":{"version":"v2.2.4"},"values":[[1690000000,"0.001"],[1690000060,"0.001"]]}
		]}}`))
	}))
	defer srv.Close()

	l := New(Config{PrometheusURL: srv.URL})
	res, err := l.QueryMetrics(context.Background(), liveScope(), map[string]any{})
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if gotPath != "/api/v1/query_range" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "checkout") {
		t.Errorf("query should target resource: %q", gotQuery)
	}
	if res.Source != "prometheus" {
		t.Errorf("source = %q", res.Source)
	}
	raw := res.Raw.(map[string]any)
	series := raw["series"].([]map[string]any)
	if len(series) != 2 {
		t.Fatalf("expected 2 series, got %d", len(series))
	}
	if res.Summary == "" {
		t.Error("empty summary")
	}
}

func TestLivePrometheusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error"}`))
	}))
	defer srv.Close()
	l := New(Config{PrometheusURL: srv.URL})
	if _, err := l.QueryMetrics(context.Background(), liveScope(), nil); err == nil {
		t.Fatal("expected error on prometheus status=error")
	}
}

func TestLiveLokiQueryRange(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[
			{"stream":{"pod":"checkout-0"},"values":[
				["1690000000000000000","ERROR upstream payment-gateway request timeout"],
				["1690000001000000000","WARN connection pool exhausted"]
			]}
		]}}`))
	}))
	defer srv.Close()

	l := New(Config{LokiURL: srv.URL})
	res, err := l.SearchLogs(context.Background(), liveScope(), map[string]any{})
	if err != nil {
		t.Fatalf("SearchLogs: %v", err)
	}
	if gotPath != "/loki/api/v1/query_range" {
		t.Errorf("path = %q", gotPath)
	}
	if res.Source != "loki" {
		t.Errorf("source = %q", res.Source)
	}
	raw := res.Raw.(map[string]any)
	if raw["matched"].(int) != 2 {
		t.Errorf("matched = %v", raw["matched"])
	}
	byLevel := raw["by_level"].(map[string]int)
	if byLevel["ERROR"] != 1 || byLevel["WARN"] != 1 {
		t.Errorf("level classification wrong: %+v", byLevel)
	}
}

func TestLiveTempoSearch(t *testing.T) {
	var gotPath, gotTags string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/traces/") {
			// span 详情调用(P2):返回空 batches,不影响 search 断言
			_, _ = w.Write([]byte(`{"batches":[]}`))
			return
		}
		gotPath = r.URL.Path
		gotTags = r.URL.Query().Get("tags")
		_, _ = w.Write([]byte(`{"traces":[
			{"traceID":"abc123","rootServiceName":"checkout","rootTraceName":"POST /checkout","durationMs":2100},
			{"traceID":"def456","rootServiceName":"checkout","rootTraceName":"GET /cart","durationMs":120}
		]}`))
	}))
	defer srv.Close()

	l := New(Config{TempoURL: srv.URL})
	res, err := l.GetTraces(context.Background(), liveScope(), map[string]any{})
	if err != nil {
		t.Fatalf("GetTraces: %v", err)
	}
	if gotPath != "/api/search" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotTags, "checkout") {
		t.Errorf("tags should carry service: %q", gotTags)
	}
	raw := res.Raw.(map[string]any)
	if raw["slowest_ms"].(int) != 2100 {
		t.Errorf("slowest_ms = %v", raw["slowest_ms"])
	}
	if raw["slowest_trace"].(string) != "abc123" {
		t.Errorf("slowest_trace = %v", raw["slowest_trace"])
	}
}

func TestLiveGracefulDegradation(t *testing.T) {
	// No URLs, no kube client configured: every tool must return an
	// "unavailable" Result, never panic or error.
	l := New(Config{})
	scope := liveScope()
	cases := map[string]func() (Result, error){
		"metrics": func() (Result, error) { return l.QueryMetrics(context.Background(), scope, nil) },
		"logs":    func() (Result, error) { return l.SearchLogs(context.Background(), scope, nil) },
		"traces":  func() (Result, error) { return l.GetTraces(context.Background(), scope, nil) },
	}
	for name, fn := range cases {
		res, err := fn()
		if err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
		}
		if !strings.HasSuffix(res.Source, "/unavailable") {
			t.Errorf("%s: expected unavailable source, got %q", name, res.Source)
		}
		raw := res.Raw.(map[string]any)
		if raw["available"].(bool) {
			t.Errorf("%s: expected available=false", name)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("AIOPS_PROM_URL", "http://prom:9090")
	t.Setenv("AIOPS_LOKI_URL", "http://loki:3100")
	t.Setenv("AIOPS_TEMPO_URL", "http://tempo:3200")
	t.Setenv("AIOPS_CLUSTER_LABEL", "cluster_id")
	cfg := ConfigFromEnv()
	if cfg.PrometheusURL != "http://prom:9090" || cfg.LokiURL != "http://loki:3100" ||
		cfg.TempoURL != "http://tempo:3200" || cfg.ClusterLabel != "cluster_id" {
		t.Errorf("ConfigFromEnv mismatch: %+v", cfg)
	}
	c := New(cfg)
	if !c.Configured() {
		t.Error("三个后端都配置了,Configured 应为 true")
	}
	if len(c.Backends()) != 3 {
		t.Errorf("Backends() = %v, want 3", c.Backends())
	}
}

func TestNotConfigured(t *testing.T) {
	c := New(Config{})
	if c.Configured() {
		t.Error("空配置 Configured 应为 false")
	}
	if len(c.Backends()) != 0 {
		t.Errorf("空配置不应有后端: %v", c.Backends())
	}
}
