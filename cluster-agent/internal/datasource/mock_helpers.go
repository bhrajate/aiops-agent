package datasource

import (
	"crypto/sha1"
	"encoding/hex"
	"time"
)

// shortHash produces a deterministic 6-char id fragment from s.
func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:6]
}

// ramp returns 6 evenly spaced points climbing from base to peak.
func ramp(m *Mock, scope Scope, base, peak float64) []map[string]any {
	const n = 6
	pts := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		f := float64(i) / float64(n-1)
		v := base + (peak-base)*f
		pts = append(pts, map[string]any{
			"t": m.ts(scope, time.Duration(-(n-1-i))*time.Minute),
			"v": round4(v),
		})
	}
	return pts
}

// flat returns 6 points holding a constant baseline value.
func flat(m *Mock, scope Scope, base float64) []map[string]any {
	const n = 6
	pts := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		pts = append(pts, map[string]any{
			"t": m.ts(scope, time.Duration(-(n-1-i))*time.Minute),
			"v": round4(base),
		})
	}
	return pts
}

func round4(f float64) float64 {
	return float64(int(f*10000+0.5)) / 10000
}

func effectiveRange(m *Mock, scope Scope) TimeRange {
	if scope.TimeRange != nil && scope.TimeRange.From != "" && scope.TimeRange.To != "" {
		return *scope.TimeRange
	}
	return TimeRange{From: m.ts(scope, -5*time.Minute), To: m.ts(scope, 0)}
}

func defaultMetricExpr(category, service string) string {
	switch category {
	case CatResourceBottle:
		return `rate(container_cpu_usage_seconds_total{service="` + service + `"}[5m])`
	default:
		return `sum(rate(http_requests_total{service="` + service + `",code=~"5.."}[5m])) / sum(rate(http_requests_total{service="` + service + `"}[5m]))`
	}
}

func keyEventReason(category string) string {
	switch category {
	case CatReleaseRegression:
		return "新版本 Pod Readiness 探针间歇失败(上游超时)"
	case CatPodCrashLoop:
		return "OOMKilling / BackOff"
	case CatResourceBottle:
		return "CPUThrottlingHigh"
	default:
		return "依赖超时导致 Readiness 失败"
	}
}

func keyLogPattern(category, dependency string) string {
	switch category {
	case CatReleaseRegression:
		return "连接池耗尽 + 对 " + dependency + " 的请求超时"
	case CatPodCrashLoop:
		return "OutOfMemoryError + SIGKILL(137)"
	case CatResourceBottle:
		return "worker 池饱和 + 504 超时"
	default:
		return "对 " + dependency + " 的调用超时 + 熔断打开"
	}
}

func statusFor(category string) string {
	if category == CatResourceBottle {
		return "ok"
	}
	return "error"
}
