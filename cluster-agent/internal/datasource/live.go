package datasource

// live.go implements the production, READ-ONLY DataSource.
//
// READ-ONLY GUARANTEE
// -------------------
// The Cluster Agent is read-only by contract (see package doc and
// docs/SECURITY.md). The Live data source upholds this at three levels:
//
//  1. Kubernetes: it only ever calls get/list on the typed client
//     (kubeReader in kubernetes.go). It never constructs create/update/
//     patch/delete/exec/attach/portforward requests, and the rest.Config is
//     used solely to build a read client. No write verb is wrapped anywhere.
//  2. Prometheus / Loki / Tempo: access is limited to their query HTTP GET
//     endpoints (query_range / search). No remote-write, no admin API, no
//     delete-series calls exist in this package.
//  3. Graceful degradation: when an upstream URL (or the Kubernetes client)
//     is not configured, the corresponding tool returns an "unavailable"
//     Result instead of panicking, so a partial deployment stays safe.
//
// This mirrors the deterministic Mock so the Tool Gateway sees an identical
// Result shape regardless of the active backend.

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"
)

// Live is the real, read-only DataSource. Any sub-backend may be nil when its
// URL / client is not configured; the owning tool then degrades gracefully.
type Live struct {
	prom  *promClient
	loki  *lokiClient
	tempo *tempoClient
	kube  *kubeReader
	now   func() time.Time
}

var _ DataSource = (*Live)(nil)

// LiveConfig configures the Live data source. Empty URLs disable the matching
// backend (its tool degrades to an "unavailable" Result).
type LiveConfig struct {
	PrometheusURL string
	LokiURL       string
	TempoURL      string
	// Kubeconfig, when empty, means "try in-cluster config". Ignored on builds
	// without client-go.
	Kubeconfig string
	// HTTPTimeout bounds every upstream HTTP call. Zero -> 15s.
	HTTPTimeout time.Duration
}

// NewLive builds a Live data source from cfg. It never fails hard: a missing
// upstream simply disables the matching tool. The Kubernetes client is built
// best-effort; when client-go cannot produce a config, kube stays nil.
func NewLive(cfg LiveConfig) *Live {
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	hc := &http.Client{Timeout: timeout}

	l := &Live{now: time.Now}
	if u := strings.TrimSpace(cfg.PrometheusURL); u != "" {
		l.prom = &promClient{base: strings.TrimRight(u, "/"), hc: hc}
	}
	if u := strings.TrimSpace(cfg.LokiURL); u != "" {
		l.loki = &lokiClient{base: strings.TrimRight(u, "/"), hc: hc}
	}
	if u := strings.TrimSpace(cfg.TempoURL); u != "" {
		l.tempo = &tempoClient{base: strings.TrimRight(u, "/"), hc: hc}
	}
	if kr, err := newKubeReader(cfg.Kubeconfig); err == nil {
		l.kube = kr
	}
	return l
}

// LiveConfigFromEnv reads the AIOPS_* live-mode configuration.
func LiveConfigFromEnv() LiveConfig {
	return LiveConfig{
		PrometheusURL: os.Getenv("AIOPS_PROM_URL"),
		LokiURL:       os.Getenv("AIOPS_LOKI_URL"),
		TempoURL:      os.Getenv("AIOPS_TEMPO_URL"),
		Kubeconfig:    os.Getenv("AIOPS_KUBECONFIG"),
	}
}

// FromEnv selects the DataSource implementation from AIOPS_DATASOURCE
// (mock | live; default mock) and returns it together with the chosen mode
// label for startup logging.
func FromEnv() (DataSource, string) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AIOPS_DATASOURCE"))) {
	case "live":
		return NewLive(LiveConfigFromEnv()), "live"
	default:
		return NewMock(), "mock"
	}
}

// unavailable builds a well-formed Result for a backend that is not configured,
// so tools degrade gracefully instead of erroring or panicking.
func unavailable(source, namespace, resource, why string) Result {
	return Result{
		Source:  source + "/unavailable",
		Summary: why,
		Raw: map[string]any{
			"available": false,
			"namespace": namespace,
			"resource":  resource,
			"reason":    why,
		},
		Freshness: "n/a",
	}
}

// maxWindow bounds the effective query window. A caller-supplied range wider
// than this is truncated (keeping the requested "to" and pulling "from"
// forward) so a single query cannot ask an upstream for an unbounded span of
// samples/logs and exhaust memory or the upstream itself.
const maxWindow = 24 * time.Hour

// window resolves the effective query window from the scope, defaulting to the
// last 5 minutes ending "now". The window is clamped so it is always positive
// and never wider than maxWindow.
func (l *Live) window(scope Scope) (from, to time.Time) {
	to = l.now().UTC()
	from = to.Add(-5 * time.Minute)
	if scope.TimeRange != nil {
		if t, err := time.Parse(time.RFC3339, scope.TimeRange.From); err == nil {
			from = t
		}
		if t, err := time.Parse(time.RFC3339, scope.TimeRange.To); err == nil {
			to = t
		}
	}
	// Guard against inverted or empty ranges: fall back to a 5m lookback.
	if !to.After(from) {
		from = to.Add(-5 * time.Minute)
	}
	// Truncate an over-wide window to the most recent maxWindow.
	if to.Sub(from) > maxWindow {
		from = to.Add(-maxWindow)
	}
	return from, to
}

// promStep picks a query_range step that keeps the returned sample count
// bounded (~<=1000 points) regardless of how wide the window is, so Prometheus
// is never asked to materialise a massive matrix.
func promStep(from, to time.Time) time.Duration {
	const targetPoints = 1000
	step := 60 * time.Second
	d := to.Sub(from)
	if d <= 0 {
		return step
	}
	if d < step {
		return d
	}
	if min := d / targetPoints; min > step {
		// Round up to whole seconds for a clean step value.
		step = (min/time.Second + 1) * time.Second
	}
	return step
}

// liveResource returns the effective resource name from the scope.
func liveResource(scope Scope) string {
	if r := scope.ResourceName(); r != "" {
		return r
	}
	return ""
}

// --- DataSource dispatch: Kubernetes-backed tools ---

func (l *Live) GetWorkloadState(ctx context.Context, scope Scope, args map[string]any) (Result, error) {
	if l.kube == nil {
		return unavailable("kubernetes", ns(scope), liveResource(scope), "未配置 Kubernetes 客户端(in-cluster/kubeconfig 均不可用),工作负载查询降级"), nil
	}
	return l.kube.workloadState(ctx, scope)
}

func (l *Live) GetKubernetesEvents(ctx context.Context, scope Scope, args map[string]any) (Result, error) {
	if l.kube == nil {
		return unavailable("kubernetes", ns(scope), liveResource(scope), "未配置 Kubernetes 客户端,事件查询降级"), nil
	}
	return l.kube.events(ctx, scope)
}

func (l *Live) ListRecentChanges(ctx context.Context, scope Scope, args map[string]any) (Result, error) {
	if l.kube == nil {
		return unavailable("change-intel", ns(scope), liveResource(scope), "未配置 Kubernetes 客户端,变更(ReplicaSet 版本历史)查询降级"), nil
	}
	return l.kube.recentChanges(ctx, scope)
}

func (l *Live) InspectDependencies(ctx context.Context, scope Scope, args map[string]any) (Result, error) {
	if l.kube == nil {
		return unavailable("topology", ns(scope), liveResource(scope), "未配置 Kubernetes 客户端,依赖拓扑查询降级"), nil
	}
	return l.kube.dependencies(ctx, scope)
}

// --- DataSource dispatch: HTTP-backed tools ---

func (l *Live) QueryMetrics(ctx context.Context, scope Scope, args map[string]any) (Result, error) {
	if l.prom == nil {
		return unavailable("prometheus", ns(scope), liveResource(scope), "未配置 AIOPS_PROM_URL,指标查询降级"), nil
	}
	from, to := l.window(scope)
	return l.prom.queryRange(ctx, scope, args, from, to)
}

func (l *Live) SearchLogs(ctx context.Context, scope Scope, args map[string]any) (Result, error) {
	if l.loki == nil {
		return unavailable("loki", ns(scope), liveResource(scope), "未配置 AIOPS_LOKI_URL,日志查询降级"), nil
	}
	from, to := l.window(scope)
	return l.loki.queryRange(ctx, scope, args, from, to)
}

func (l *Live) GetTraces(ctx context.Context, scope Scope, args map[string]any) (Result, error) {
	if l.tempo == nil {
		return unavailable("tempo", ns(scope), liveResource(scope), "未配置 AIOPS_TEMPO_URL,链路查询降级"), nil
	}
	from, to := l.window(scope)
	return l.tempo.search(ctx, scope, args, from, to)
}
