package datasource

// live_prometheus.go: read-only Prometheus HTTP API client.
// Only GET /api/v1/query_range is used — no remote-write, admin or delete API.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxUpstreamBody caps how much of an upstream (Prometheus / Loki / Tempo)
// response body we will read before decoding, so a hostile or misbehaving
// upstream cannot OOM the agent with an unbounded stream. It is a var (not a
// const) purely so tests can shrink it; production always uses 32 MiB.
var maxUpstreamBody int64 = 32 << 20 // 32 MiB

type promClient struct {
	base string
	hc   *http.Client
}

// promResponse models the subset of the Prometheus query_range envelope we use.
type promResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// defaultLiveMetricExpr builds a generic 5xx error-ratio query for the resource,
// always scoped to the namespace so the default never queries cluster-wide.
func defaultLiveMetricExpr(namespace, resource string) string {
	if resource == "" {
		return fmt.Sprintf(
			`sum(rate(http_requests_total{namespace=%q,code=~"5.."}[5m])) / sum(rate(http_requests_total{namespace=%q}[5m]))`,
			namespace, namespace)
	}
	return fmt.Sprintf(
		`sum(rate(http_requests_total{namespace=%q,service=%q,code=~"5.."}[5m])) / sum(rate(http_requests_total{namespace=%q,service=%q}[5m]))`,
		namespace, resource, namespace, resource)
}

func (p *promClient) queryRange(ctx context.Context, scope Scope, args map[string]any, from, to time.Time, clusterScope ScopeLabel) (Result, error) {
	namespace := ns(scope)
	resource := liveResource(scope)
	// Reject names that could break out of the injected label matchers.
	if err := validateDNS1123("namespace", namespace); err != nil {
		return Result{}, err
	}
	if err := validateDNS1123("resource", resource); err != nil {
		return Result{}, err
	}

	// 强制约束:namespace + (可选)cluster。共享后端下必须带 cluster,
	// 否则会读到其他集群的同名 namespace。
	required := []ScopeLabel{{Name: "namespace", Value: namespace}}
	if clusterScope.Name != "" {
		if err := validateDNS1123("cluster", clusterScope.Value); err != nil {
			return Result{}, err
		}
		required = append(required, clusterScope)
	}

	expr, _ := args["expr"].(string)
	if strings.TrimSpace(expr) == "" {
		// 默认查询按 namespace 构造,再统一过一遍注入以补上 cluster 约束。
		expr = defaultLiveMetricExpr(namespace, resource)
	}
	// Caller-supplied 或默认 expr 一律过 AST 注入:每个 selector 都被限定,
	// 裸选择器(`... or up`)无法逃逸;已带但跨范围的 matcher 直接拒绝。
	scoped, err := scopePromQL(expr, required...)
	if err != nil {
		return Result{}, fmt.Errorf("query_metrics scope: %w", err)
	}
	expr = scoped
	step := promStep(from, to)

	q := url.Values{}
	q.Set("query", expr)
	q.Set("start", strconv.FormatInt(from.Unix(), 10))
	q.Set("end", strconv.FormatInt(to.Unix(), 10))
	q.Set("step", strconv.FormatFloat(step.Seconds(), 'f', -1, 64))
	endpoint := p.base + "/api/v1/query_range?" + q.Encode()

	var pr promResponse
	if err := httpGetJSON(ctx, p.hc, endpoint, &pr); err != nil {
		return Result{}, fmt.Errorf("prometheus query_range: %w", err)
	}
	if pr.Status != "success" {
		return Result{}, fmt.Errorf("prometheus error: %s: %s", pr.ErrorType, pr.Error)
	}

	series := make([]map[string]any, 0, len(pr.Data.Result))
	var maxLast float64
	for _, r := range pr.Data.Result {
		pts := make([]map[string]any, 0, len(r.Values))
		var last float64
		for _, v := range r.Values {
			if len(v) != 2 {
				continue
			}
			ts, _ := v[0].(float64)
			val, _ := parsePromValue(v[1])
			last = val
			pts = append(pts, map[string]any{
				"t": time.Unix(int64(ts), 0).UTC().Format(time.RFC3339),
				"v": round4(val),
			})
		}
		if last > maxLast {
			maxLast = last
		}
		series = append(series, map[string]any{
			"metric": r.Metric,
			"points": pts,
		})
	}

	summary := fmt.Sprintf("%s/%s 指标查询返回 %d 条时间序列(表达式:%s),峰值样本约 %s。",
		ns(scope), orAll(liveResource(scope)), len(series), truncate(expr, 80), pct(maxLast))
	if len(series) == 0 {
		summary = fmt.Sprintf("%s/%s 指标查询在该时间窗内无匹配序列(表达式:%s)。",
			ns(scope), orAll(liveResource(scope)), truncate(expr, 80))
	}
	raw := map[string]any{
		"cluster_id": scope.ClusterID,
		"namespace":  ns(scope),
		"expr":       expr,
		"time_range": TimeRange{From: from.UTC().Format(time.RFC3339), To: to.UTC().Format(time.RFC3339)},
		"step_sec":   step.Seconds(),
		"series":     series,
	}
	return Result{Source: "prometheus", Summary: summary, Raw: raw, Freshness: "live"}, nil
}

// parsePromValue converts a Prometheus sample value (string or number) to float.
func parsePromValue(v any) (float64, error) {
	switch t := v.(type) {
	case string:
		return strconv.ParseFloat(t, 64)
	case float64:
		return t, nil
	default:
		return 0, fmt.Errorf("unexpected sample value type %T", v)
	}
}

func httpGetJSON(ctx context.Context, hc *http.Client, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	// Bound the read: never decode more than maxUpstreamBody from the upstream.
	return json.NewDecoder(io.LimitReader(resp.Body, maxUpstreamBody)).Decode(out)
}

func orAll(s string) string {
	if s == "" {
		return "(全部)"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
