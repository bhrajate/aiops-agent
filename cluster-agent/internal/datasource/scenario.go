package datasource

import (
	"fmt"
	"strings"
)

// 故障类别(与架构设计 §1 及 contracts.md 的 fault_category 对齐)。
const (
	CatReleaseRegression = "release_regression"
	CatPodCrashLoop      = "pod_crashloop"
	CatResourceBottle    = "resource_bottleneck"
	CatDependencyTimeout = "dependency_timeout"
)

// scenario 是一个确定性且自洽的故障剧本。所有工具都从同一个 scenario 推导输出,
// 因此产出的证据能串成一条前后一致的链条。
type scenario struct {
	category   string // Cat* 常量之一
	service    string // 工作负载 / 服务名
	kind       string // Kubernetes 资源类型,例如 Deployment
	replicas   int    // 期望副本数
	oldVersion string // 上一个发布版本
	newVersion string // 当前发布版本(引入回归的版本)
	newPods    int    // 已切到新版本的 Pod 数
	image      string // 容器镜像(新版本)

	peakErr float64 // 受影响面的 5xx 峰值比例
	peakP99 int     // p99 延迟峰值(毫秒)

	dependency  string // 下游依赖(服务或数据存储)
	depLatency  int    // 观测到的依赖延迟(毫秒)
	changeKind  string // deploy | config | infra
	changeDesc  string // 触发变更的人类可读描述
	changeActor string // 变更执行者

}

// resolveScenario 把 scope 映射到确定性的 scenario。主线场景
// namespace=payment / resource=checkout 复现设计文档中
// 「发布回归 -> 依赖超时」的剧本。其他命名空间映射到剩余三类故障,
// 以便完整演示故障分类体系。
func resolveScenario(scope Scope) scenario {
	ns := strings.ToLower(scope.Namespace)
	res := strings.ToLower(scope.ResourceName())
	if res == "" {
		res = defaultResourceFor(ns)
	}
	svc := scope.ResourceName()
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
			peakErr:     0.082,
			peakP99:     2100,
			dependency:  "payment-gateway",
			depLatency:  1850,
			changeKind:  "config",
			changeDesc:  "发布 v2.3.0 同时将 HTTP 连接池 max_idle_conns 由 200 下调为 20,导致对 payment-gateway 的请求排队",
			changeActor: "ci-bot@release",
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
			peakErr:     0.35,
			peakP99:     180,
			dependency:  "redis-cart",
			depLatency:  35,
			changeKind:  "config",
			changeDesc:  "ConfigMap cart-config 将 JVM -Xmx 由 1024m 改为 256m,容器启动即 OOMKilled",
			changeActor: "alice@platform",
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
			peakErr:     0.02,
			peakP99:     4200,
			dependency:  "postgres-inventory",
			depLatency:  120,
			changeKind:  "infra",
			changeDesc:  "大促流量增长 3 倍,CPU 长期处于 limit 上限并被大量 throttle,未扩容",
			changeActor: "traffic-surge",
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
			peakErr:     0.06,
			peakP99:     3200,
			dependency:  "auth-service",
			depLatency:  3000,
			changeKind:  "infra",
			changeDesc:  "下游 auth-service 因数据库连接耗尽响应变慢,错误沿调用链向上传播",
			changeActor: "auth-service-owner",
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

// pct 把小数渲染为百分比字符串,例如 0.082 -> "8.2%"。
func pct(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }
