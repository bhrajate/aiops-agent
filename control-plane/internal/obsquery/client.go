package obsquery

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config 配置共享观测后端。URL 为空表示该后端未接入,对应工具优雅降级。
type Config struct {
	PrometheusURL string
	LokiURL       string
	TempoURL      string
	// ClusterLabel 是后端中区分集群的 label / tag 名(cluster / cluster_id /
	// k8s_cluster;Tempo 侧常为 k8s.cluster.name)。
	// **多集群共用一套后端时必须设置**,否则只按 namespace 过滤会读到其他集群
	// 的同名 namespace。为空表示后端为单集群专用,不做集群维度约束。
	ClusterLabel string
	// HTTPTimeout 约束每次上游调用。零值 -> 15s。
	HTTPTimeout time.Duration
}

// ConfigFromEnv 从 AIOPS_* 环境变量读取配置。
func ConfigFromEnv() Config {
	return Config{
		PrometheusURL: os.Getenv("AIOPS_PROM_URL"),
		LokiURL:       os.Getenv("AIOPS_LOKI_URL"),
		TempoURL:      os.Getenv("AIOPS_TEMPO_URL"),
		ClusterLabel:  os.Getenv("AIOPS_CLUSTER_LABEL"),
	}
}

// Querier 是 Tool Gateway 使用的观测查询接口(真实后端或 mock 均实现它)。
type Querier interface {
	QueryMetrics(ctx context.Context, scope Scope, args map[string]any) (Result, error)
	SearchLogs(ctx context.Context, scope Scope, args map[string]any) (Result, error)
	GetTraces(ctx context.Context, scope Scope, args map[string]any) (Result, error)
}

// FromEnv 选择观测数据源并返回模式标签(供启动日志):
//   - AIOPS_OBS_DATASOURCE=mock            → 强制 mock
//   - 配置了任一后端 URL                    → live(直连真实后端)
//   - 未配置任何后端(且未强制 live)         → mock,保证零基础设施也能端到端演示
//
// 说明:观测查询迁到控制面后,若此处直接拒绝,会打断 README 快速开始与
// scripts/prod-e2e.sh 的零依赖演示路径,故保留 mock 回退。
func FromEnv() (Querier, string) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("AIOPS_OBS_DATASOURCE")))
	cfg := ConfigFromEnv()
	switch mode {
	case "mock":
		return NewMock(), "mock"
	case "live":
		return New(cfg), "live"
	}
	if c := New(cfg); c.Configured() {
		return c, "live"
	}
	return NewMock(), "mock"
}

// Client 查询共享观测后端。控制面的 Tool Gateway 直接使用它,
// 不再绕经任何集群内 agent。
type Client struct {
	prom         *promClient
	loki         *lokiClient
	tempo        *tempoClient
	clusterLabel string
	now          func() time.Time
}

// New 构造 Client。缺失的后端只会禁用对应工具,不会硬失败。
func New(cfg Config) *Client {
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	hc := &http.Client{Timeout: timeout}
	c := &Client{now: time.Now, clusterLabel: strings.TrimSpace(cfg.ClusterLabel)}
	if u := strings.TrimSpace(cfg.PrometheusURL); u != "" {
		c.prom = &promClient{base: strings.TrimRight(u, "/"), hc: hc}
	}
	if u := strings.TrimSpace(cfg.LokiURL); u != "" {
		c.loki = &lokiClient{base: strings.TrimRight(u, "/"), hc: hc}
	}
	if u := strings.TrimSpace(cfg.TempoURL); u != "" {
		c.tempo = &tempoClient{base: strings.TrimRight(u, "/"), hc: hc}
	}
	return c
}

// Configured 报告是否至少接入了一个后端(供启动日志与路由决策)。
func (c *Client) Configured() bool {
	return c != nil && (c.prom != nil || c.loki != nil || c.tempo != nil)
}

// Backends 返回已接入的后端名(启动日志用)。
func (c *Client) Backends() []string {
	var out []string
	if c.prom != nil {
		out = append(out, "prometheus")
	}
	if c.loki != nil {
		out = append(out, "loki")
	}
	if c.tempo != nil {
		out = append(out, "tempo")
	}
	return out
}

// clusterScope 返回集群维度约束(未配置 clusterLabel 时返回零值=不强制)。
func (c *Client) clusterScope(scope Scope) ScopeLabel {
	if c.clusterLabel == "" {
		return ScopeLabel{}
	}
	return ScopeLabel{Name: c.clusterLabel, Value: scope.ClusterID}
}

// QueryMetrics 执行 query_metrics 工具。
func (c *Client) QueryMetrics(ctx context.Context, scope Scope, args map[string]any) (Result, error) {
	if c.prom == nil {
		return unavailable("prometheus", ns(scope), liveResource(scope),
			"未配置 AIOPS_PROM_URL,指标查询降级"), nil
	}
	from, to := window(scope, c.now)
	return c.prom.queryRange(ctx, scope, args, from, to, c.clusterScope(scope))
}

// SearchLogs 执行 search_logs 工具。
func (c *Client) SearchLogs(ctx context.Context, scope Scope, args map[string]any) (Result, error) {
	if c.loki == nil {
		return unavailable("loki", ns(scope), liveResource(scope),
			"未配置 AIOPS_LOKI_URL,日志查询降级"), nil
	}
	from, to := window(scope, c.now)
	return c.loki.queryRange(ctx, scope, args, from, to, c.clusterScope(scope))
}

// GetTraces 执行 get_traces 工具。
func (c *Client) GetTraces(ctx context.Context, scope Scope, args map[string]any) (Result, error) {
	if c.tempo == nil {
		return unavailable("tempo", ns(scope), liveResource(scope),
			"未配置 AIOPS_TEMPO_URL,链路查询降级"), nil
	}
	from, to := window(scope, c.now)
	return c.tempo.search(ctx, scope, args, from, to, c.clusterScope(scope))
}
