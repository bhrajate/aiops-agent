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
