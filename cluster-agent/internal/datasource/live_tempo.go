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

	// 取最慢 trace 的 span 详情,定位"哪个下游 span 变慢"(仅只读 GET /api/traces/{id})。
	var topSpanName, topSpanSvc string
	var topSpanMs int
	var spans []map[string]any
	if slowestID != "" {
		if sp, top, err := c.traceSpans(ctx, slowestID); err != nil {
			// span 详情失败不致命:降级为仅 trace 发现
		} else {
			spans = sp
			topSpanName, topSpanSvc, topSpanMs = top.name, top.svc, top.ms
		}
	}

	summary := fmt.Sprintf("%s/%s 采样到 %d 条 trace,最慢 %dms(trace %s)。",
		ns(scope), orAll(svc), len(traces), slowest, truncate(slowestID, 16))
	if topSpanName != "" {
		summary += fmt.Sprintf(" 最慢 span:%s@%s 耗时 %dms(定位下游瓶颈)。", topSpanName, topSpanSvc, topSpanMs)
	}
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
		"slowest_spans": spans,
		"slowest_span":  map[string]any{"name": topSpanName, "service": topSpanSvc, "duration_ms": topSpanMs},
	}
	return Result{Source: "tempo", Summary: summary, Raw: raw, Freshness: "live"}, nil
}

// tempoTraceResponse 建模 GET /api/traces/{id} 的 OTLP batches 结构(只取定位瓶颈所需字段)。
type tempoTraceResponse struct {
	Batches []struct {
		Resource struct {
			Attributes []struct {
				Key   string `json:"key"`
				Value struct {
					StringValue string `json:"stringValue"`
				} `json:"value"`
			} `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []struct {
				Name              string `json:"name"`
				StartTimeUnixNano string `json:"startTimeUnixNano"`
				EndTimeUnixNano   string `json:"endTimeUnixNano"`
			} `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"batches"`
}

type topSpan struct {
	name, svc string
	ms        int
}

// traceSpans 拉取单条 trace 的所有 span,返回 span 列表与最慢 span(只读)。
func (c *tempoClient) traceSpans(ctx context.Context, traceID string) ([]map[string]any, topSpan, error) {
	// traceID 仅允许十六进制,防路径注入。
	if !isHex(traceID) {
		return nil, topSpan{}, fmt.Errorf("invalid trace id")
	}
	var tr tempoTraceResponse
	if err := httpGetJSON(ctx, c.hc, c.base+"/api/traces/"+traceID, &tr); err != nil {
		return nil, topSpan{}, err
	}
	var out []map[string]any
	var top topSpan
	for _, b := range tr.Batches {
		svc := ""
		for _, a := range b.Resource.Attributes {
			if a.Key == "service.name" {
				svc = a.Value.StringValue
			}
		}
		for _, ss := range b.ScopeSpans {
			for _, s := range ss.Spans {
				ms := spanDurationMs(s.StartTimeUnixNano, s.EndTimeUnixNano)
				out = append(out, map[string]any{"name": s.Name, "service": svc, "duration_ms": ms})
				if ms > top.ms {
					top = topSpan{name: s.Name, svc: svc, ms: ms}
				}
			}
		}
	}
	return out, top, nil
}

func spanDurationMs(startNano, endNano string) int {
	s, err1 := strconv.ParseInt(startNano, 10, 64)
	e, err2 := strconv.ParseInt(endNano, 10, 64)
	if err1 != nil || err2 != nil || e < s {
		return 0
	}
	return int((e - s) / 1_000_000)
}

// isHex 判断字符串是否全为十六进制字符(trace id 校验)。
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
