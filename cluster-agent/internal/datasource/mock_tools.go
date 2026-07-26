package datasource

import (
	"context"
	"fmt"
	"time"
)

func (m *Mock) GetKubernetesEvents(_ context.Context, scope Scope, _ map[string]any) (Result, error) {
	s := resolveScenario(scope)
	events := make([]map[string]any, 0, 4)

	add := func(offset time.Duration, typ, reason, obj, msg string, count int) {
		events = append(events, map[string]any{
			"type":      typ,
			"reason":    reason,
			"object":    obj,
			"message":   msg,
			"count":     count,
			"last_seen": m.ts(scope, offset),
		})
	}

	switch s.category {
	case CatReleaseRegression:
		add(-8*time.Minute, "Normal", "ScalingReplicaSet", s.kind+"/"+s.service,
			fmt.Sprintf("Scaled up replica set %s-%s to %d", s.service, shortHash(s.newVersion), s.newPods), 1)
		add(-7*time.Minute, "Normal", "SuccessfulCreate", "ReplicaSet/"+s.service+"-"+shortHash(s.newVersion),
			fmt.Sprintf("Created pod running %s", s.newVersion), s.newPods)
		add(-4*time.Minute, "Warning", "Unhealthy", "Pod/"+s.service+"-"+shortHash(s.newVersion)+"-0",
			"Readiness probe intermittently failing: upstream request timeout", 12)
	case CatPodCrashLoop:
		add(-6*time.Minute, "Warning", "BackOff", "Pod/"+s.service+"-0",
			"Back-off restarting failed container", 9)
		add(-6*time.Minute, "Warning", "OOMKilling", "Pod/"+s.service+"-0",
			"Container exceeded memory limit (256Mi) and was OOMKilled", 7)
		add(-2*time.Minute, "Warning", "Unhealthy", "Pod/"+s.service+"-1",
			"Liveness probe failed: container not started", 5)
	case CatResourceBottle:
		add(-9*time.Minute, "Warning", "CPUThrottlingHigh", s.kind+"/"+s.service,
			"Container CPU throttled >75% of periods", 40)
		add(-3*time.Minute, "Normal", "NotEnoughResources", "HorizontalPodAutoscaler/"+s.service,
			"desired replica count limited by max; CPU utilization 96%", 3)
	default:
		add(-5*time.Minute, "Warning", "Unhealthy", "Pod/"+s.service+"-0",
			"Readiness probe failed: dependency "+s.dependency+" timeout", 15)
	}

	summary := fmt.Sprintf("%s/%s 最近产生 %d 类 Kubernetes 事件,关键告警:%s。",
		ns(scope), s.service, len(events), keyEventReason(s.category))
	raw := map[string]any{
		"cluster_id": scope.ClusterID,
		"namespace":  ns(scope),
		"resource":   s.service,
		"events":     events,
	}
	return Result{Source: "kubernetes", Summary: summary, Raw: raw, Freshness: "8s"}, nil
}

func (m *Mock) QueryMetrics(_ context.Context, scope Scope, args map[string]any) (Result, error) {
	s := resolveScenario(scope)
	expr, _ := args["expr"].(string)
	if expr == "" {
		expr = defaultMetricExpr(s.category, s.service)
	}

	// Series split by version so the LLM can see the regression is version-scoped.
	series := []map[string]any{
		{
			"metric": map[string]string{"version": s.newVersion, "surface": "new"},
			"points": ramp(m, scope, s.baselineErr, s.peakErr),
			"unit":   "ratio",
		},
		{
			"metric": map[string]string{"version": s.oldVersion, "surface": "old"},
			"points": flat(m, scope, s.baselineErr),
			"unit":   "ratio",
		},
	}
	if s.category != CatReleaseRegression {
		// Non-release faults are not version-scoped: whole workload degrades.
		series = []map[string]any{{
			"metric": map[string]string{"surface": "all"},
			"points": ramp(m, scope, s.baselineErr, s.peakErr),
			"unit":   "ratio",
		}}
	}

	var summary string
	switch s.category {
	case CatReleaseRegression:
		summary = fmt.Sprintf("仅新版本 %s 实例 5xx 错误率从 %s 升至 %s,旧版本 %s 保持 %s,错误随版本聚集,支持发布回归假设。",
			s.newVersion, pct(s.baselineErr), pct(s.peakErr), s.oldVersion, pct(s.baselineErr))
	case CatPodCrashLoop:
		summary = fmt.Sprintf("%s 错误率升至 %s,与 Pod 反复重启导致的可用实例不足吻合。", s.service, pct(s.peakErr))
	case CatResourceBottle:
		summary = fmt.Sprintf("%s CPU 利用率达 96%% 并被 throttle,p99 由 %dms 升至 %dms。", s.service, s.baselineP99, s.peakP99)
	default:
		summary = fmt.Sprintf("%s 错误率升至 %s、p99 升至 %dms,曲线与下游 %s 延迟同步,提示依赖超时传播。",
			s.service, pct(s.peakErr), s.peakP99, s.dependency)
	}

	raw := map[string]any{
		"cluster_id":  scope.ClusterID,
		"namespace":   ns(scope),
		"expr":        expr,
		"time_range":  effectiveRange(m, scope),
		"series":      series,
		"latency_p99": map[string]int{"baseline_ms": s.baselineP99, "peak_ms": s.peakP99},
	}
	return Result{Source: "prometheus", Summary: summary, Raw: raw, Freshness: "10s"}, nil
}

func (m *Mock) SearchLogs(_ context.Context, scope Scope, args map[string]any) (Result, error) {
	s := resolveScenario(scope)
	query, _ := args["query"].(string)
	lines := make([]map[string]any, 0, 4)

	add := func(offset time.Duration, level, pod, msg string) {
		lines = append(lines, map[string]any{
			"timestamp": m.ts(scope, offset),
			"level":     level,
			"pod":       pod,
			"message":   msg,
		})
	}

	switch s.category {
	case CatReleaseRegression:
		np := s.service + "-" + shortHash(s.newVersion) + "-0"
		add(-4*time.Minute, "ERROR", np, fmt.Sprintf("upstream %s request timeout after 2000ms (max_idle_conns=20 exhausted)", s.dependency))
		add(-3*time.Minute, "WARN", np, "connection pool exhausted, 47 requests waiting for idle connection")
		add(-2*time.Minute, "ERROR", np, "POST /checkout 502 Bad Gateway: context deadline exceeded")
	case CatPodCrashLoop:
		add(-6*time.Minute, "FATAL", s.service+"-0", "java.lang.OutOfMemoryError: Java heap space (-Xmx256m)")
		add(-6*time.Minute, "ERROR", s.service+"-0", "container received SIGKILL (OOMKilled), exit code 137")
	case CatResourceBottle:
		add(-5*time.Minute, "WARN", s.service+"-1", "request queue depth 512, worker pool saturated (cpu throttled)")
		add(-1*time.Minute, "ERROR", s.service+"-1", "GET /stock 504 Gateway Timeout: handler exceeded 4s budget")
	default:
		add(-5*time.Minute, "ERROR", s.service+"-0", fmt.Sprintf("call to %s timed out after 3000ms", s.dependency))
		add(-2*time.Minute, "ERROR", s.service+"-0", "circuit breaker open for "+s.dependency)
	}

	summary := fmt.Sprintf("%s/%s 日志命中 %d 条关键错误,最显著模式:%s。",
		ns(scope), s.service, len(lines), keyLogPattern(s.category, s.dependency))
	raw := map[string]any{
		"cluster_id": scope.ClusterID,
		"namespace":  ns(scope),
		"resource":   s.service,
		"query":      query,
		"matched":    len(lines),
		"lines":      lines,
	}
	return Result{Source: "loki", Summary: summary, Raw: raw, Freshness: "12s"}, nil
}

func (m *Mock) GetTraces(_ context.Context, scope Scope, _ map[string]any) (Result, error) {
	s := resolveScenario(scope)
	spans := []map[string]any{
		{"span": "ingress", "service": s.service, "duration_ms": s.peakP99, "status": "error"},
		{"span": "handler", "service": s.service, "duration_ms": s.peakP99 - 100, "status": "error"},
		{"span": "downstream-call", "service": s.dependency, "duration_ms": s.depLatency, "status": statusFor(s.category)},
	}
	traces := []map[string]any{{
		"trace_id":      "trace-" + shortHash(s.service+s.newVersion),
		"root_service":  s.service,
		"total_ms":      s.peakP99,
		"error":         true,
		"bottleneck":    s.dependency,
		"bottleneck_ms": s.depLatency,
		"spans":         spans,
	}}

	summary := fmt.Sprintf("采样 trace 显示 %s 请求耗时 %dms,其中对下游 %s 的调用占 %dms,是主要瓶颈。",
		s.service, s.peakP99, s.dependency, s.depLatency)
	if s.category == CatPodCrashLoop {
		summary = fmt.Sprintf("%s 大量请求因实例不可用直接失败,trace 采样稀疏。", s.service)
	}
	raw := map[string]any{
		"cluster_id": scope.ClusterID,
		"namespace":  ns(scope),
		"resource":   s.service,
		"traces":     traces,
	}
	return Result{Source: "tempo", Summary: summary, Raw: raw, Freshness: "15s"}, nil
}

func (m *Mock) ListRecentChanges(_ context.Context, scope Scope, _ map[string]any) (Result, error) {
	s := resolveScenario(scope)
	changes := []map[string]any{
		{
			"change_id":   "chg-" + shortHash(s.service+s.newVersion),
			"type":        s.changeKind,
			"at":          m.ts(scope, -9*time.Minute),
			"actor":       s.changeActor,
			"summary":     s.changeDesc,
			"correlation": "在告警发生前 4 分钟,时间相关但需机制证据佐证",
		},
	}
	if s.category == CatReleaseRegression {
		changes = append([]map[string]any{{
			"change_id": "chg-" + shortHash(s.service+"deploy"),
			"type":      "deploy",
			"at":        m.ts(scope, -10*time.Minute),
			"actor":     s.changeActor,
			"summary":   fmt.Sprintf("将 %s/%s 从 %s 滚动发布到 %s(镜像 %s)", ns(scope), s.service, s.oldVersion, s.newVersion, s.image),
		}}, changes...)
	}

	summary := fmt.Sprintf("%s/%s 近 10 分钟有 %d 条变更,最相关:%s。",
		ns(scope), s.service, len(changes), s.changeDesc)
	raw := map[string]any{
		"cluster_id": scope.ClusterID,
		"namespace":  ns(scope),
		"resource":   s.service,
		"changes":    changes,
	}
	return Result{Source: "change-intel", Summary: summary, Raw: raw, Freshness: "30s"}, nil
}

func (m *Mock) InspectDependencies(_ context.Context, scope Scope, _ map[string]any) (Result, error) {
	s := resolveScenario(scope)
	edges := []map[string]any{
		{
			"from":       s.service,
			"to":         s.dependency,
			"kind":       "downstream",
			"protocol":   "http",
			"error_rate": pct(s.peakErr),
			"latency_ms": s.depLatency,
			"source":     "trace",
			"confidence": 0.9,
			"last_seen":  m.ts(scope, -1*time.Minute),
		},
		{
			"from":       "api-gateway",
			"to":         s.service,
			"kind":       "upstream",
			"protocol":   "http",
			"error_rate": pct(s.peakErr),
			"latency_ms": s.peakP99,
			"source":     "kubernetes-service",
			"confidence": 0.75,
			"last_seen":  m.ts(scope, -1*time.Minute),
		},
	}
	summary := fmt.Sprintf("%s 上游为 api-gateway,下游关键依赖为 %s(当前延迟 %dms);拓扑来自 trace 与 Service,置信度中高。",
		s.service, s.dependency, s.depLatency)
	raw := map[string]any{
		"cluster_id":   scope.ClusterID,
		"namespace":    ns(scope),
		"resource":     s.service,
		"edges":        edges,
		"blast_radius": map[string]int{"services": len(edges) + 1, "namespaces": 1},
	}
	return Result{Source: "topology", Summary: summary, Raw: raw, Freshness: "20s"}, nil
}
