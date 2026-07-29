package obsquery

// 内部(**非工具**)Prometheus 查询路径。
//
// 为什么需要单独一条路径:`Client.QueryMetrics` 是**工具**路径,它强制注入
// namespace + cluster 约束,因为那条路上的表达式可能来自模型。而拓扑同步与 SLO
// 燃尽率查询是**服务端自己发起**的:表达式写死在代码里,查询范围本来就是全集群
// (拓扑边天生跨 namespace),硬套 namespace 约束只会让它查不到任何东西。
//
// 安全边界:本文件的方法**绝不能**被 Tool Gateway 触达。它们不接受调用方提供的
// 表达式 —— 参数只有时间窗与集群 ID,PromQL 由调用方在代码里硬编码。
// 这是刻意的设计:一旦允许传表达式,它就变成了一条绕过范围注入的后门。
//
// 集群维度仍然强制:共享后端下不带 cluster 会把别的集群的拓扑混进来,
// 而那个错误在诊断结论里看不出来 —— 拓扑图看着完整、逻辑自洽,只是画的是别人的集群。

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// InstantSample 一条即时查询结果(一个 label 集 + 一个值)。
type InstantSample struct {
	Labels map[string]string
	Value  float64
}

// promInstantResponse 建模 /api/v1/query 的响应(即时查询,非 range)。
type promInstantResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// InternalInstantQuery 执行一次即时查询,供服务端自身使用。
//
// expr 必须由调用方硬编码,**不得**来自任何外部输入(见文件头)。
// clusterID 非空且已配置 Prometheus 集群 label 时,会追加集群约束。
func (c *Client) InternalInstantQuery(ctx context.Context, expr, clusterID string) ([]InstantSample, error) {
	if c == nil || c.prom == nil {
		return nil, fmt.Errorf("prometheus 未配置")
	}
	if strings.TrimSpace(expr) == "" {
		return nil, fmt.Errorf("空表达式")
	}
	// 集群维度约束:与工具路径同一套 AST 注入,复用而非另写一份 ——
	// 另写会让两条路径的行为漂移。
	if label := c.clusterLabels.For(BackendPrometheus); label != "" && clusterID != "" {
		if err := validateDNS1123("cluster", clusterID); err != nil {
			return nil, err
		}
		scoped, err := scopePromQL(expr, ScopeLabel{Name: label, Value: clusterID})
		if err != nil {
			return nil, fmt.Errorf("集群范围注入: %w", err)
		}
		expr = scoped
	}

	q := url.Values{}
	q.Set("query", expr)
	q.Set("time", strconv.FormatInt(time.Now().Unix(), 10))
	endpoint := c.prom.base + "/api/v1/query?" + q.Encode()

	var pr promInstantResponse
	if err := httpGetJSON(ctx, c.prom.hc, endpoint, &pr); err != nil {
		return nil, fmt.Errorf("prometheus query: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus error: %s: %s", pr.ErrorType, pr.Error)
	}

	out := make([]InstantSample, 0, len(pr.Data.Result))
	for _, r := range pr.Data.Result {
		if len(r.Value) != 2 {
			continue
		}
		v, err := parsePromValue(r.Value[1])
		if err != nil {
			continue // 单条样本解析失败不该让整次同步失败
		}
		out = append(out, InstantSample{Labels: r.Metric, Value: v})
	}
	return out, nil
}

// HasPrometheus 报告是否可用(供调用方决定是否启用依赖它的后台循环)。
func (c *Client) HasPrometheus() bool {
	return c != nil && c.prom != nil
}
