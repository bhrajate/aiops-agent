package obsquery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateDNS1123(t *testing.T) {
	ok := []string{"", "payment", "checkout-v2", "a.b.c", "ns1"}
	for _, s := range ok {
		if err := validateDNS1123("x", s); err != nil {
			t.Errorf("validateDNS1123(%q) unexpected err: %v", s, err)
		}
	}
	bad := []string{`p"ayment`, "ns{x}", "a,b", "UPPER", "has space", `x=~".*"`, "-lead", "trail-"}
	for _, s := range bad {
		if err := validateDNS1123("x", s); err == nil {
			t.Errorf("validateDNS1123(%q) expected error", s)
		}
	}
}

func TestInjectNamespaceMatchers(t *testing.T) {
	cases := []struct {
		name, expr, ns, want string
		wantErr              bool
	}{
		{name: "empty block", expr: `up{}`, ns: "payment", want: `up{namespace="payment"}`},
		{name: "adds ns", expr: `rate(http_requests_total{code=~"5.."}[5m])`, ns: "payment",
			want: `rate(http_requests_total{namespace="payment",code=~"5.."}[5m])`},
		{name: "matching ns kept", expr: `up{namespace="payment",code="200"}`, ns: "payment",
			want: `up{namespace="payment",code="200"}`},
		{name: "multiple blocks", expr: `sum(a{x="1"}) / sum(b{y="2"})`, ns: "payment",
			want: `sum(a{namespace="payment",x="1"}) / sum(b{namespace="payment",y="2"})`},
		{name: "cross-namespace rejected", expr: `up{namespace="other"}`, ns: "payment", wantErr: true},
		{name: "regex namespace rejected", expr: `up{namespace=~".*"}`, ns: "payment", wantErr: true},
		{name: "no selector rejected", expr: `vector(1)`, ns: "payment", wantErr: true},
		{name: "comma inside value", expr: `up{msg="a,b"}`, ns: "payment", want: `up{namespace="payment",msg="a,b"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := injectNamespaceMatchers(c.expr, ScopeLabel{Name: "namespace", Value: c.ns})
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

// TestPromNamespaceInjected 验证无论是默认表达式还是调用方传入的表达式,
// namespace 匹配器都会真正出现在发往上游的请求里。
func TestPromNamespaceInjected(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()
	l := New(Config{PrometheusURL: srv.URL})

	// 默认表达式。
	if _, err := l.QueryMetrics(context.Background(), liveScope(), map[string]any{}); err != nil {
		t.Fatalf("default QueryMetrics: %v", err)
	}
	if !strings.Contains(gotQuery, `namespace="payment"`) {
		t.Errorf("default query missing namespace matcher: %q", gotQuery)
	}

	// 调用方表达式未带 namespace 时会被注入一个。
	_, err := l.QueryMetrics(context.Background(), liveScope(), map[string]any{"expr": `rate(http_requests_total{code=~"5.."}[5m])`})
	if err != nil {
		t.Fatalf("custom QueryMetrics: %v", err)
	}
	if !strings.Contains(gotQuery, `namespace="payment"`) {
		t.Errorf("custom query missing namespace matcher: %q", gotQuery)
	}
}

func TestPromCrossNamespaceRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()
	l := New(Config{PrometheusURL: srv.URL})
	_, err := l.QueryMetrics(context.Background(), liveScope(), map[string]any{"expr": `up{namespace="other"}`})
	if err == nil {
		t.Fatal("expected cross-namespace query to be rejected")
	}
}

func TestLokiNamespaceInjected(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
	}))
	defer srv.Close()
	l := New(Config{LokiURL: srv.URL})
	_, err := l.SearchLogs(context.Background(), liveScope(), map[string]any{"query": `{app="checkout"} |= "error"`})
	if err != nil {
		t.Fatalf("SearchLogs: %v", err)
	}
	if !strings.Contains(gotQuery, `namespace="payment"`) {
		t.Errorf("loki query missing namespace matcher: %q", gotQuery)
	}
}

func TestWindowClampedToMax(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	scope := liveScope()
	scope.TimeRange = &TimeRange{
		From: now.Add(-100 * time.Hour).Format(time.RFC3339),
		To:   now.Format(time.RFC3339),
	}
	from, to := window(scope, func() time.Time { return now })
	if d := to.Sub(from); d > maxWindow {
		t.Errorf("window %v exceeds max %v", d, maxWindow)
	}
	if !from.Equal(now.Add(-maxWindow)) {
		t.Errorf("from = %v, want %v", from, now.Add(-maxWindow))
	}
}

func TestWindowInvertedFallback(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	scope := liveScope()
	scope.TimeRange = &TimeRange{
		From: now.Format(time.RFC3339),
		To:   now.Add(-time.Hour).Format(time.RFC3339), // to 早于 from
	}
	from, to := window(scope, func() time.Time { return now })
	if !to.After(from) {
		t.Errorf("window not positive: from=%v to=%v", from, to)
	}
}

// TestUpstreamBodyLimited 证明 LimitReader 会截断超大的上游响应体:把上限调得极小后,
// 本来合法的 JSON 会在流中途被切断,解码失败,而不是把全部内容缓冲下来。
func TestUpstreamBodyLimited(t *testing.T) {
	orig := maxUpstreamBody
	maxUpstreamBody = 64 // bytes
	defer func() { maxUpstreamBody = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 一份体量很大但合法的 JSON 文档,远超 64 字节的上限。
		var b strings.Builder
		b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
		for i := 0; i < 1000; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"metric":{"k":"vvvvvvvvvvvvvvvvvvvv"},"values":[]}`)
		}
		b.WriteString("]}}")
		_, _ = w.Write([]byte(b.String()))
	}))
	defer srv.Close()

	var out promResponse
	err := httpGetJSON(context.Background(), srv.Client(), srv.URL, &out)
	if err == nil {
		t.Fatal("expected decode error from truncated (limited) body")
	}
}

func TestPromStepAdaptive(t *testing.T) {
	base := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	// 24 小时时间窗在不超过 1000 个采样点的约束下,step 必须 >= 约 86 秒。
	step := promStep(base.Add(-24*time.Hour), base)
	if step < 80*time.Second {
		t.Errorf("24h step too small: %v", step)
	}
	// 极小的时间窗保持 60 秒默认值(或更小)。
	if s := promStep(base.Add(-2*time.Minute), base); s > 60*time.Second {
		t.Errorf("2m step too large: %v", s)
	}
}
