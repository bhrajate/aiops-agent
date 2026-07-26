package datasource

// live_tempo.go: read-only Tempo HTTP API client.
// Only GET /api/search is used (trace discovery). No ingest / delete API.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type tempoClient struct {
	base string
	hc   *http.Client
}

// tempoSearchResponse models Tempo's /api/search envelope.
type tempoSearchResponse struct {
	Traces []struct {
		TraceID           string `json:"traceID"`
		RootServiceName   string `json:"rootServiceName"`
		RootTraceName     string `json:"rootTraceName"`
		StartTimeUnixNano string `json:"startTimeUnixNano"`
		DurationMs        int    `json:"durationMs"`
	} `json:"traces"`
}

func (c *tempoClient) search(ctx context.Context, scope Scope, args map[string]any, from, to time.Time) (Result, error) {
	svc, _ := args["service"].(string)
	if strings.TrimSpace(svc) == "" {
		svc = liveResource(scope)
	}
	// Isolation (parity with Prometheus/Loki): validate the caller-supplied
	// service name and always constrain the search to the scope namespace so a
	// caller cannot read other tenants' traces by overriding "service".
	if err := validateDNS1123("service", svc); err != nil {
		return Result{}, fmt.Errorf("get_traces scope: %w", err)
	}
	namespace := ns(scope)
	if err := validateDNS1123("namespace", namespace); err != nil {
		return Result{}, fmt.Errorf("get_traces scope: %w", err)
	}

	q := url.Values{}
	// Force the namespace tag; add service.name when known. url.Values.Encode
	// escapes the values, and DNS-1123 validation above blocks tag-syntax breakout.
	tags := "k8s.namespace.name=" + namespace
	if svc != "" {
		tags += " service.name=" + svc
	}
	q.Set("tags", tags)
	q.Set("start", strconv.FormatInt(from.Unix(), 10))
	q.Set("end", strconv.FormatInt(to.Unix(), 10))
	q.Set("limit", "20")
	endpoint := c.base + "/api/search?" + q.Encode()

	var tr tempoSearchResponse
	if err := httpGetJSON(ctx, c.hc, endpoint, &tr); err != nil {
		return Result{}, fmt.Errorf("tempo search: %w", err)
	}

	traces := make([]map[string]any, 0, len(tr.Traces))
	var slowest int
	var slowestID string
	for _, t := range tr.Traces {
		if t.DurationMs > slowest {
			slowest = t.DurationMs
			slowestID = t.TraceID
		}
		traces = append(traces, map[string]any{
			"trace_id":     t.TraceID,
			"root_service": t.RootServiceName,
			"root_span":    t.RootTraceName,
			"total_ms":     t.DurationMs,
		})
	}

	summary := fmt.Sprintf("%s/%s 采样到 %d 条 trace,最慢 %dms(trace %s)。",
		ns(scope), orAll(svc), len(traces), slowest, truncate(slowestID, 16))
	if len(traces) == 0 {
		summary = fmt.Sprintf("%s/%s 在该时间窗内无匹配 trace。", ns(scope), orAll(svc))
	}
	raw := map[string]any{
		"cluster_id":    scope.ClusterID,
		"namespace":     ns(scope),
		"resource":      svc,
		"traces":        traces,
		"slowest_ms":    slowest,
		"slowest_trace": slowestID,
	}
	return Result{Source: "tempo", Summary: summary, Raw: raw, Freshness: "live"}, nil
}
