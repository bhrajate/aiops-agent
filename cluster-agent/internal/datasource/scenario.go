package datasource

import (
	"fmt"
	"strings"
)

// Fault categories (aligned with 架构设计 §1 and contracts.md fault_category).
const (
	CatReleaseRegression = "release_regression"
	CatPodCrashLoop      = "pod_crashloop"
	CatResourceBottle    = "resource_bottleneck"
	CatDependencyTimeout = "dependency_timeout"
)

// scenario is a deterministic, self-consistent fault story. Every tool derives
// its output from the same scenario so the evidence forms one coherent chain.
type scenario struct {
	category   string // one of the Cat* constants
	service    string // workload / service name
	kind       string // Kubernetes kind, e.g. Deployment
	replicas   int    // desired replicas
	oldVersion string // previous rollout version
	newVersion string // current rollout version (regression carrier)
	newPods    int    // pods already on the new version
	image      string // container image (new version)

	baselineErr float64 // baseline 5xx ratio (fraction, e.g. 0.001)
	peakErr     float64 // peak 5xx ratio on the affected surface
	baselineP99 int      // baseline p99 latency ms
	peakP99     int      // peak p99 latency ms

	dependency  string // downstream dependency (service or datastore)
	depLatency  int    // observed dependency latency ms
	changeKind  string // deploy | config | infra
	changeDesc  string // human description of the triggering change
	changeActor string // who made the change

	// story is a one-line narrative used to keep summaries consistent.
	story string
}

// resolveScenario maps a scope to a deterministic scenario. The flagship
// namespace=payment / resource=checkout reproduces the design doc's
// release-regression -> dependency-timeout story. Other namespaces map to the
// remaining three fault categories so the full taxonomy is demonstrable.
func resolveScenario(scope Scope) scenario {
	ns := strings.ToLower(scope.Namespace)
	res := strings.ToLower(scope.Resource)
	if res == "" {
		res = defaultResourceFor(ns)
	}
	svc := scope.Resource
	if svc == "" {
		svc = defaultResourceFor(ns)
	}

	switch {
	case ns == "payment" || res == "checkout":
		return scenario{
			category:    CatReleaseRegression,
			service:     svc,
			kind:        "Deployment",
			replicas:    6,
			oldVersion:  "v2.2.4",
			newVersion:  "v2.3.0",
			newPods:     3,
			image:       "registry.internal/payment/" + svc + ":v2.3.0",
			baselineErr: 0.001,
			peakErr:     0.082,
			baselineP99: 240,
			peakP99:     2100,
			dependency:  "payment-gateway",
			depLatency:  1850,
			changeKind:  "config",
			changeDesc:  "发布 v2.3.0 同时将 HTTP 连接池 max_idle_conns 由 200 下调为 20,导致对 payment-gateway 的请求排队",
			changeActor: "ci-bot@release",
			story:       "checkout 新版本 v2.3.0 上线后仅新实例 5xx 升高,根因指向连接池配置变更引发对 payment-gateway 的依赖超时",
		}
	case ns == "cart" || strings.Contains(res, "session"):
		return scenario{
			category:    CatPodCrashLoop,
			service:     svc,
			kind:        "Deployment",
			replicas:    4,
			oldVersion:  "v1.8.1",
			newVersion:  "v1.8.1",
			newPods:     0,
			image:       "registry.internal/cart/" + svc + ":v1.8.1",
			baselineErr: 0.002,
			peakErr:     0.35,
			baselineP99: 180,
			peakP99:     180,
			dependency:  "redis-cart",
			depLatency:  35,
			changeKind:  "config",
			changeDesc:  "ConfigMap cart-config 将 JVM -Xmx 由 1024m 改为 256m,容器启动即 OOMKilled",
			changeActor: "alice@platform",
			story:       svc + " 反复 CrashLoopBackOff,容器因内存上限被下调触发 OOMKilled",
		}
	case ns == "inventory" || strings.Contains(res, "stock"):
		return scenario{
			category:    CatResourceBottle,
			service:     svc,
			kind:        "Deployment",
			replicas:    3,
			oldVersion:  "v3.1.0",
			newVersion:  "v3.1.0",
			newPods:     0,
			image:       "registry.internal/inventory/" + svc + ":v3.1.0",
			baselineErr: 0.001,
			peakErr:     0.02,
			baselineP99: 300,
			peakP99:     4200,
			dependency:  "postgres-inventory",
			depLatency:  120,
			changeKind:  "infra",
			changeDesc:  "大促流量增长 3 倍,CPU 长期处于 limit 上限并被大量 throttle,未扩容",
			changeActor: "traffic-surge",
			story:       svc + " CPU 打满并被 throttle,p99 延迟随负载升高,属资源瓶颈",
		}
	default:
		return scenario{
			category:    CatDependencyTimeout,
			service:     svc,
			kind:        "Deployment",
			replicas:    4,
			oldVersion:  "v4.0.2",
			newVersion:  "v4.0.2",
			newPods:     0,
			image:       "registry.internal/" + ns + "/" + svc + ":v4.0.2",
			baselineErr: 0.001,
			peakErr:     0.06,
			baselineP99: 260,
			peakP99:     3200,
			dependency:  "auth-service",
			depLatency:  3000,
			changeKind:  "infra",
			changeDesc:  "下游 auth-service 因数据库连接耗尽响应变慢,错误沿调用链向上传播",
			changeActor: "auth-service-owner",
			story:       svc + " 自身无变更,延迟与错误来自下游 auth-service 依赖超时的传播",
		}
	}
}

func defaultResourceFor(ns string) string {
	switch ns {
	case "payment":
		return "checkout"
	case "cart":
		return "cart-session"
	case "inventory":
		return "stock-api"
	case "":
		return "app"
	default:
		return ns + "-api"
	}
}

// pct renders a fraction as a percentage string, e.g. 0.082 -> "8.2%".
func pct(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }
