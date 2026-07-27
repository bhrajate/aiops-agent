package datasource

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

	l := NewLive(LiveConfig{PrometheusURL: srv.URL})
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
	l := NewLive(LiveConfig{PrometheusURL: srv.URL})
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

	l := NewLive(LiveConfig{LokiURL: srv.URL})
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

	l := NewLive(LiveConfig{TempoURL: srv.URL})
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
	l := NewLive(LiveConfig{})
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

func TestFromEnvSelectsMockByDefault(t *testing.T) {
	t.Setenv("AIOPS_DATASOURCE", "")
	ds, mode := FromEnv()
	if mode != "mock" {
		t.Errorf("mode = %q, want mock", mode)
	}
	if _, ok := ds.(*Mock); !ok {
		t.Errorf("default datasource is not *Mock: %T", ds)
	}
}

func TestFromEnvSelectsLive(t *testing.T) {
	t.Setenv("AIOPS_DATASOURCE", "live")
	ds, mode := FromEnv()
	if mode != "live" {
		t.Errorf("mode = %q, want live", mode)
	}
	if _, ok := ds.(*Live); !ok {
		t.Errorf("live datasource is not *Live: %T", ds)
	}
}
