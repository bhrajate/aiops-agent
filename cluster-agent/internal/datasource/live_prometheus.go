package datasource

// live_prometheus.go: read-only Prometheus HTTP API client.
// Only GET /api/v1/query_range is used — no remote-write, admin or delete API.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

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

// defaultLiveMetricExpr builds a generic 5xx error-ratio query for the resource.
func defaultLiveMetricExpr(resource string) string {
	if resource == "" {
		return `sum(rate(http_requests_total{code=~"5.."}[5m])) / sum(rate(http_requests_total[5m]))`
	}
	return fmt.Sprintf(
		`sum(rate(http_requests_total{service="%s",code=~"5.."}[5m])) / sum(rate(http_requests_total{service="%s"}[5m]))`,
		resource, resource)
}

func (p *promClient) queryRange(ctx context.Context, scope Scope, args map[string]any, from, to time.Time) (Result, error) {
	expr, _ := args["expr"].(string)
	if strings.TrimSpace(expr) == "" {
		expr = defaultLiveMetricExpr(liveResource(scope))
	}
	step := 60 * time.Second
	if d := to.Sub(from); d > 0 && d < step {
		step = d
	}

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
	return json.NewDecoder(resp.Body).Decode(out)
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
