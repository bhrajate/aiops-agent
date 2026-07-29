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
	// 既没有配置 URL 也没有 kube 客户端:每个工具都必须返回 "unavailable" 结果,
	// 绝不能 panic 或报错。
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
	// 全局 AIOPS_CLUSTER_LABEL 应回落到三个后端(向后兼容:老部署只设了这一个变量)。
	if cfg.ClusterLabels.Prometheus != "cluster_id" ||
		cfg.ClusterLabels.Loki != "cluster_id" ||
		cfg.ClusterLabels.Tempo != "cluster_id" {
		t.Errorf("全局 label 未回落到各后端: %+v", cfg.ClusterLabels)
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

// TestConfigFromEnv_PerBackendOverride 验证后端专属变量优先于全局值,
// 且这个组合(Prom=cluster + Tempo=k8s.cluster.name)在改动前**无法同时生效**。
func TestConfigFromEnv_PerBackendOverride(t *testing.T) {
	t.Setenv("AIOPS_CLUSTER_LABEL", "cluster")
	t.Setenv("AIOPS_TEMPO_CLUSTER_LABEL", "k8s.cluster.name")
	t.Setenv("AIOPS_LOKI_CLUSTER_LABEL", "k8s_cluster_name")
	cfg := ConfigFromEnv()
	if cfg.ClusterLabels.Prometheus != "cluster" {
		t.Errorf("Prometheus 应回落到全局值: %q", cfg.ClusterLabels.Prometheus)
	}
	if cfg.ClusterLabels.Loki != "k8s_cluster_name" {
		t.Errorf("Loki 应用专属值: %q", cfg.ClusterLabels.Loki)
	}
	if cfg.ClusterLabels.Tempo != "k8s.cluster.name" {
		t.Errorf("Tempo 应用专属值: %q", cfg.ClusterLabels.Tempo)
	}
	if err := cfg.ClusterLabels.Validate(); err != nil {
		t.Errorf("该组合应合法: %v", err)
	}
}

// TestConfigFromEnv_Disabled 验证显式关闭。
// 要求显式表态而非留空即关闭:留空静默不隔离会让 RCA 读到其他集群同名
// namespace 的数据,而该错误在诊断结论里看不出来。
func TestConfigFromEnv_Disabled(t *testing.T) {
	t.Setenv("AIOPS_CLUSTER_LABEL", "cluster")
	t.Setenv("AIOPS_CLUSTER_LABEL_DISABLED", "true")
	cfg := ConfigFromEnv()
	if len(cfg.ClusterLabels.Unenforced()) != 3 {
		t.Errorf("显式关闭后三个后端都应不强制: %+v", cfg.ClusterLabels)
	}
}

// TestConfigFromEnv_Defaults 验证未设任何变量时用各后端的惯例默认值——
// 这些默认值不同,正是本改动的理由。
func TestConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("AIOPS_CLUSTER_LABEL", "")
	t.Setenv("AIOPS_PROM_CLUSTER_LABEL", "")
	t.Setenv("AIOPS_LOKI_CLUSTER_LABEL", "")
	t.Setenv("AIOPS_TEMPO_CLUSTER_LABEL", "")
	t.Setenv("AIOPS_CLUSTER_LABEL_DISABLED", "")
	cl := ConfigFromEnv().ClusterLabels
	if cl.Prometheus != DefaultPromClusterLabel || cl.Loki != DefaultLokiClusterLabel {
		t.Errorf("Prom/Loki 默认应为 cluster: %+v", cl)
	}
	if cl.Tempo != DefaultTempoClusterLabel {
		t.Errorf("Tempo 默认应为 OTel 语义约定 k8s.cluster.name: %q", cl.Tempo)
	}
	if err := cl.Validate(); err != nil {
		t.Errorf("默认值必须自洽合法: %v", err)
	}
}
