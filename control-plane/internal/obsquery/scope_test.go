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

// TestPromNamespaceInjected verifies the namespace matcher reaches the wire for
// both the default and a caller-supplied expression.
func TestPromNamespaceInjected(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()
	l := New(Config{PrometheusURL: srv.URL})

	// Default expression.
	if _, err := l.QueryMetrics(context.Background(), liveScope(), map[string]any{}); err != nil {
		t.Fatalf("default QueryMetrics: %v", err)
	}
	if !strings.Contains(gotQuery, `namespace="payment"`) {
		t.Errorf("default query missing namespace matcher: %q", gotQuery)
	}

	// Caller expression without namespace gets one injected.
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
		To:   now.Add(-time.Hour).Format(time.RFC3339), // to < from
	}
	from, to := window(scope, func() time.Time { return now })
	if !to.After(from) {
		t.Errorf("window not positive: from=%v to=%v", from, to)
	}
}

// TestUpstreamBodyLimited proves the LimitReader truncates an oversized upstream
// body: with a tiny cap the (otherwise valid) JSON is cut off mid-stream and
// decoding fails instead of buffering everything.
func TestUpstreamBodyLimited(t *testing.T) {
	orig := maxUpstreamBody
	maxUpstreamBody = 64 // bytes
	defer func() { maxUpstreamBody = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A large but valid JSON document, far bigger than the 64-byte cap.
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
	// A 24h window at <=1000 points must yield step >= ~86s.
	step := promStep(base.Add(-24*time.Hour), base)
	if step < 80*time.Second {
		t.Errorf("24h step too small: %v", step)
	}
	// A tiny window keeps the 60s default (or smaller).
	if s := promStep(base.Add(-2*time.Minute), base); s > 60*time.Second {
		t.Errorf("2m step too large: %v", s)
	}
}
