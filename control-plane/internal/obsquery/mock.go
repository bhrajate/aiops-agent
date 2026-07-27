package obsquery

// mock.go: 确定性的 mock 观测数据源。
//
// 为什么需要:观测查询迁到控制面后,若未配置 AIOPS_PROM_URL/LOKI_URL/TEMPO_URL,
// metrics/logs/traces 三个工具会被拒绝——这会打断"零基础设施即可端到端演示"
// 的入门路径(README 快速开始 / scripts/prod-e2e.sh 依赖它)。
// 因此提供一个 mock:输出确定性、可复现,且与 cluster-agent mock 的故障剧本
// 保持一致(发布回归:新版本实例错误率飙升、连接池配置变更、下游依赖排队)。
//
// 通过 AIOPS_OBS_DATASOURCE=mock 显式启用,或在未配置任何后端 URL 时自动回退。

import (
	"context"
	"fmt"
)

// Mock 是确定性 mock 实现,签名与 Client 的三个查询方法一致。
type Mock struct{}

// NewMock 构造 mock 观测数据源。
func NewMock() *Mock { return &Mock{} }

// mockScenario 依据 namespace/resource 推导一个稳定的故障剧本,
// 使同一 incident 的多次查询互相自洽(同一版本号、同一依赖名)。
type mockScenario struct {
	service     string
	newVersion  string
	oldVersion  string
	baselineErr float64
	peakErr     float64
	peakP99     int
	dependency  string
}

func resolveMockScenario(scope Scope) mockScenario {
	svc := liveResource(scope)
	if svc == "" {
		svc = "checkout"
	}
	s := mockScenario{
		service: svc, newVersion: "v2.3.0", oldVersion: "v2.2.4",
		baselineErr: 0.001, peakErr: 0.082, peakP99: 2100, dependency: "auth-service",
	}
	// 按命名空间给出不同剧本,便于演示多类故障(与 cluster-agent mock 对齐)。
	switch ns(scope) {
	case "inventory":
		s.dependency = "inventory-db"
		s.peakErr, s.peakP99 = 0.041, 1350
	case "orders":
		s.dependency = "payment-gateway"
		s.peakErr, s.peakP99 = 0.065, 1800
	}
	return s
}

// QueryMetrics 返回按版本拆分的错误率序列:新版本升高、旧版本平稳,
// 让推理能看出异常随版本聚集(发布回归的机制证据)。
func (m *Mock) QueryMetrics(_ context.Context, scope Scope, args map[string]any) (Result, error) {
	s := resolveMockScenario(scope)
	expr, _ := args["expr"].(string)
	if expr == "" {
		expr = fmt.Sprintf(
			`sum(rate(http_requests_total{namespace=%q,service=%q,code=~"5.."}[5m]))`+
				` / sum(rate(http_requests_total{namespace=%q,service=%q}[5m]))`,
			ns(scope), s.service, ns(scope), s.service)
	}
	newPoints := make([]map[string]any, 0, 6)
	oldPoints := make([]map[string]any, 0, 6)
	step := (s.peakErr - s.baselineErr) / 5
	for i := 0; i < 6; i++ {
		t := fmt.Sprintf("2026-07-27T09:%02d:00Z", 55+i)
		newPoints = append(newPoints, map[string]any{"t": t, "v": round4(s.baselineErr + step*float64(i))})
		oldPoints = append(oldPoints, map[string]any{"t": t, "v": s.baselineErr})
	}
	raw := map[string]any{
		"cluster_id": scope.ClusterID,
		"namespace":  ns(scope),
		"expr":       expr,
		"series": []map[string]any{
			{"metric": map[string]string{"version": s.newVersion, "surface": "new"}, "points": newPoints, "unit": "ratio"},
			{"metric": map[string]string{"version": s.oldVersion, "surface": "old"}, "points": oldPoints, "unit": "ratio"},
		},
		"latency_p99": map[string]any{"baseline_ms": 240, "peak_ms": s.peakP99},
	}
	summary := fmt.Sprintf(
		"仅新版本 %s 实例 5xx 错误率从 %s 升至 %s,旧版本 %s 保持 %s,错误随版本聚集,支持发布回归假设。",
		s.newVersion, pct(s.baselineErr), pct(s.peakErr), s.oldVersion, pct(s.baselineErr))
	return Result{Source: "prometheus/mock", Summary: summary, Raw: raw, Freshness: "10s"}, nil
}

// SearchLogs 返回与剧本一致的错误日志(连接池耗尽 → 下游排队)。
func (m *Mock) SearchLogs(_ context.Context, scope Scope, args map[string]any) (Result, error) {
	s := resolveMockScenario(scope)
	query, _ := args["query"].(string)
	if query == "" {
		query = fmt.Sprintf(`{namespace=%q,app=%q} |= "ERROR"`, ns(scope), s.service)
	}
	lines := []map[string]any{
		{"ts": "2026-07-27T10:00:01Z", "level": "ERROR", "version": s.newVersion,
			"msg": fmt.Sprintf("connection pool exhausted while calling %s (waited 2000ms)", s.dependency)},
		{"ts": "2026-07-27T10:00:03Z", "level": "ERROR", "version": s.newVersion,
			"msg": fmt.Sprintf("upstream request to %s timed out after %dms", s.dependency, s.peakP99)},
		{"ts": "2026-07-27T10:00:05Z", "level": "WARN", "version": s.newVersion,
			"msg": "pool wait queue depth 64 exceeds configured maxWait"},
	}
	raw := map[string]any{
		"cluster_id": scope.ClusterID, "namespace": ns(scope),
		"query": query, "lines": lines, "error_count": 2, "warn_count": 1,
	}
	summary := fmt.Sprintf(
		"%s/%s 日志命中 3 行(ERROR 2、WARN 1):连接池耗尽并在调用 %s 时超时,错误集中在新版本 %s。",
		ns(scope), s.service, s.dependency, s.newVersion)
	return Result{Source: "loki/mock", Summary: summary, Raw: raw, Freshness: "5s"}, nil
}

// GetTraces 返回最慢 trace 与其最慢 span(定位下游瓶颈)。
func (m *Mock) GetTraces(_ context.Context, scope Scope, _ map[string]any) (Result, error) {
	s := resolveMockScenario(scope)
	spans := []map[string]any{
		{"name": "POST /checkout", "service": s.service, "duration_ms": s.peakP99},
		{"name": "db.query", "service": s.dependency, "duration_ms": s.peakP99 - 200},
		{"name": "cache.get", "service": s.service, "duration_ms": 12},
	}
	raw := map[string]any{
		"cluster_id": scope.ClusterID, "namespace": ns(scope), "resource": s.service,
		"traces": []map[string]any{
			{"trace_id": "mocktrace0001", "root_service": s.service,
				"root_span": "POST /checkout", "total_ms": s.peakP99},
		},
		"slowest_ms": s.peakP99, "slowest_trace": "mocktrace0001",
		"slowest_spans": spans,
		"slowest_span": map[string]any{
			"name": "db.query", "service": s.dependency, "duration_ms": s.peakP99 - 200,
		},
	}
	summary := fmt.Sprintf(
		"%s/%s 采样到 1 条 trace,最慢 %dms。最慢 span:db.query@%s 耗时 %dms(下游瓶颈)。",
		ns(scope), s.service, s.peakP99, s.dependency, s.peakP99-200)
	return Result{Source: "tempo/mock", Summary: summary, Raw: raw, Freshness: "12s"}, nil
}
