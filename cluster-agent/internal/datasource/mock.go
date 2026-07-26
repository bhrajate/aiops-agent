package datasource

import (
	"context"
	"fmt"
	"time"
)

// Mock is a deterministic, read-only DataSource. Given the same scope it always
// returns the same self-consistent evidence, so tests are reproducible and the
// tools together narrate one coherent fault story.
//
// It performs no I/O and never touches a real cluster.
type Mock struct {
	// now anchors relative timestamps; defaults to a fixed instant for
	// reproducibility when zero.
	now time.Time
}

// NewMock returns a Mock anchored at a fixed, reproducible instant.
func NewMock() *Mock {
	return &Mock{now: time.Date(2026, 7, 26, 10, 5, 0, 0, time.UTC)}
}

// anchor returns the scenario "current time". If the scope carries a time
// range end, it is used; otherwise the fixed anchor is used.
func (m *Mock) anchor(scope Scope) time.Time {
	if scope.TimeRange != nil && scope.TimeRange.To != "" {
		if t, err := time.Parse(time.RFC3339, scope.TimeRange.To); err == nil {
			return t
		}
	}
	if m.now.IsZero() {
		return time.Date(2026, 7, 26, 10, 5, 0, 0, time.UTC)
	}
	return m.now
}

func (m *Mock) ts(scope Scope, offset time.Duration) string {
	return m.anchor(scope).Add(offset).Format(time.RFC3339)
}

// ns returns the effective namespace, defaulting to "default".
func ns(scope Scope) string {
	if scope.Namespace == "" {
		return "default"
	}
	return scope.Namespace
}

var _ DataSource = (*Mock)(nil)

func (m *Mock) GetWorkloadState(_ context.Context, scope Scope, _ map[string]any) (Result, error) {
	s := resolveScenario(scope)
	oldPods := s.replicas - s.newPods
	ready := s.replicas
	summary := ""

	pods := make([]map[string]any, 0, s.replicas)
	for i := 0; i < s.replicas; i++ {
		version := s.oldVersion
		phase := "Running"
		restarts := 0
		podReady := true
		if i < s.newPods {
			version = s.newVersion
		}
		if s.category == CatPodCrashLoop {
			phase = "CrashLoopBackOff"
			restarts = 7 + i
			podReady = false
			ready = 0
		}
		pods = append(pods, map[string]any{
			"name":          fmt.Sprintf("%s-%s-%d", s.service, shortHash(version), i),
			"version":       version,
			"phase":         phase,
			"ready":         podReady,
			"restart_count": restarts,
			"node":          fmt.Sprintf("node-%d", i%3),
		})
	}

	switch s.category {
	case CatReleaseRegression:
		summary = fmt.Sprintf(
			"%s/%s(%s)期望副本 %d,其中 %d 个已滚动到新版本 %s、%d 个仍为 %s;新版本实例整体 Ready 但错误率异常,疑似发布回归。",
			ns(scope), s.service, s.kind, s.replicas, s.newPods, s.newVersion, oldPods, s.oldVersion)
	case CatPodCrashLoop:
		summary = fmt.Sprintf(
			"%s/%s(%s)期望副本 %d,全部处于 CrashLoopBackOff,重启次数持续增长,当前 Ready 副本 0。",
			ns(scope), s.service, s.kind, s.replicas)
	case CatResourceBottle:
		summary = fmt.Sprintf(
			"%s/%s(%s)副本 %d 全部 Running/Ready,但实例 CPU 接近 limit 上限,存在资源瓶颈风险。",
			ns(scope), s.service, s.kind, s.replicas)
	default:
		summary = fmt.Sprintf(
			"%s/%s(%s)副本 %d 全部 Running/Ready,工作负载自身健康,异常可能来自下游依赖。",
			ns(scope), s.service, s.kind, s.replicas)
	}

	raw := map[string]any{
		"cluster_id":       scope.ClusterID,
		"namespace":        ns(scope),
		"kind":             s.kind,
		"name":             s.service,
		"fault_category":   s.category,
		"desired_replicas": s.replicas,
		"ready_replicas":   ready,
		"current_version":  s.newVersion,
		"previous_version": s.oldVersion,
		"image":            s.image,
		"pods":             pods,
		"observed_at":      m.ts(scope, 0),
	}
	return Result{Source: "kubernetes", Summary: summary, Raw: raw, Freshness: "5s"}, nil
}
