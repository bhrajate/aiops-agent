package obsquery

// live_loki.go:只读的 Loki HTTP API 客户端。
// 只使用 GET /loki/api/v1/query_range。

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type lokiClient struct {
	base string
	hc   *http.Client
}

// lokiResponse 建模 Loki query_range 的 streams 响应结构。
type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"` // [ ["<纳秒时间戳>", "日志行"], ... ]
		} `json:"result"`
	} `json:"data"`
}

// defaultLogQL 为该资源构建 LogQL 选择器;未限定资源时回退到整个命名空间。
func defaultLogQL(scope Scope) string {
	res := liveResource(scope)
	if res == "" {
		return fmt.Sprintf(`{namespace=%q}`, ns(scope))
	}
	return fmt.Sprintf(`{namespace=%q,app=%q}`, ns(scope), res)
}

func (c *lokiClient) queryRange(ctx context.Context, scope Scope, args map[string]any, from, to time.Time, clusterScope ScopeLabel) (Result, error) {
	// 拒绝可能突破注入的 label 匹配器的名字。
	if err := validateDNS1123("namespace", ns(scope)); err != nil {
		return Result{}, err
	}
	if err := validateDNS1123("resource", liveResource(scope)); err != nil {
		return Result{}, err
	}

	// 强制约束:namespace + (可选)cluster(共享后端必需)。
	required := []ScopeLabel{{Name: "namespace", Value: ns(scope)}}
	if clusterScope.Name != "" {
		if err := validateDNS1123("cluster", clusterScope.Value); err != nil {
			return Result{}, err
		}
		required = append(required, clusterScope)
	}

	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		query = defaultLogQL(scope)
	}
	// 默认与调用方 LogQL 一律过注入:每个流选择器都被限定,跨范围 matcher 拒绝。
	scoped, err := injectNamespaceMatchers(query, required...)
	if err != nil {
		return Result{}, fmt.Errorf("search_logs scope: %w", err)
	}
	query = scoped

	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(from.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(to.UnixNano(), 10))
	q.Set("limit", "100")
	q.Set("direction", "backward")
	endpoint := c.base + "/loki/api/v1/query_range?" + q.Encode()

	var lr lokiResponse
	if err := httpGetJSON(ctx, c.hc, endpoint, &lr); err != nil {
		return Result{}, fmt.Errorf("loki query_range: %w", err)
	}
	if lr.Status != "success" {
		return Result{}, fmt.Errorf("loki status: %s", lr.Status)
	}

	lines := make([]map[string]any, 0, 64)
	levels := map[string]int{}
	for _, stream := range lr.Data.Result {
		pod := stream.Stream["pod"]
		if pod == "" {
			pod = stream.Stream["instance"]
		}
		for _, v := range stream.Values {
			if len(v) != 2 {
				continue
			}
			ns, _ := strconv.ParseInt(v[0], 10, 64)
			msg := v[1]
			lvl := detectLogLevel(msg)
			levels[lvl]++
			lines = append(lines, map[string]any{
				"timestamp": time.Unix(0, ns).UTC().Format(time.RFC3339Nano),
				"level":     lvl,
				"pod":       pod,
				"message":   msg,
			})
		}
	}
	// 最新的排在前面,便于阅读。
	sort.SliceStable(lines, func(i, j int) bool {
		return lines[i]["timestamp"].(string) > lines[j]["timestamp"].(string)
	})

	summary := fmt.Sprintf("%s/%s 日志命中 %d 行(ERROR %d、WARN %d),查询:%s。",
		ns(scope), orAll(liveResource(scope)), len(lines), levels["ERROR"], levels["WARN"], truncate(query, 80))
	if len(lines) == 0 {
		summary = fmt.Sprintf("%s/%s 在该时间窗内无匹配日志(查询:%s)。",
			ns(scope), orAll(liveResource(scope)), truncate(query, 80))
	}
	raw := map[string]any{
		"cluster_id": scope.ClusterID,
		"namespace":  ns(scope),
		"resource":   liveResource(scope),
		"query":      query,
		"matched":    len(lines),
		"by_level":   levels,
		"lines":      lines,
	}
	return Result{Source: "loki", Summary: summary, Raw: raw, Freshness: "live"}, nil
}

// detectLogLevel 对日志行做低成本的严重级别分类。
func detectLogLevel(msg string) string {
	u := strings.ToUpper(msg)
	switch {
	case strings.Contains(u, "FATAL"), strings.Contains(u, "PANIC"):
		return "FATAL"
	case strings.Contains(u, "ERROR"), strings.Contains(u, "ERR "):
		return "ERROR"
	case strings.Contains(u, "WARN"):
		return "WARN"
	case strings.Contains(u, "DEBUG"):
		return "DEBUG"
	default:
		return "INFO"
	}
}
