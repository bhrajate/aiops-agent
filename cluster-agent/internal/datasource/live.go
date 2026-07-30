package datasource

// live.go: 真实数据源(live 模式)。
//
// **职责已收窄为纯 Kubernetes 只读代理。**
// 可观测性后端(Prometheus / Loki / Tempo)通常是多集群共用的中心服务,
// 不在任何一个 K8s 集群内。由每集群的 agent 代理它们只会带来无谓的网络绕行、
// N 份重复凭据,以及"某集群 agent 挂掉即同时失去 metrics/logs/traces"的
// 可用性风险。因此这部分查询已迁到控制面
// (control-plane/internal/obsquery,由 Tool Gateway 直连),
// cluster-agent 只保留它真正必须在集群内做的事:访问该集群的 Kubernetes API。
//
// 本文件中的 QueryMetrics / SearchLogs / GetTraces 保留为接口占位,
// 统一返回 unavailable —— 它们不再由本组件服务(Gateway 也不会路由到这里)。

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

// Live 是基于集群内 Kubernetes API 的只读数据源。
type Live struct {
	kube *kubeReader
	now  func() time.Time
}

var _ DataSource = (*Live)(nil)

// LiveConfig 配置 live 数据源。
type LiveConfig struct {
	// Kubeconfig 为空表示使用集群内配置(in-cluster)。
	Kubeconfig string
}

// NewLive 构造 live 数据源。K8s 客户端尽力构建:拿不到配置时 kube 为 nil,
// 对应工具优雅降级而非崩溃。
func NewLive(cfg LiveConfig) *Live {
	l := &Live{now: time.Now}
	if kr, err := newKubeReader(cfg.Kubeconfig); err == nil {
		l.kube = kr
	}
	return l
}

// LiveConfigFromEnv 读取 AIOPS_* live 模式配置。
func LiveConfigFromEnv() LiveConfig {
	return LiveConfig{
		Kubeconfig: os.Getenv("AIOPS_KUBECONFIG"),
	}
}

// ErrMockInProduction 表示生产模式下解析出了 mock 数据源。
//
// 这是本组件唯一的 fail-fast 配置错误,理由和控制面拒绝
// AIOPS_OBS_DATASOURCE=mock 完全一致:mock 返回的是**虚构但自洽**的故障数据,
// 它会被 Tool Gateway 冻结成 Evidence、拿到 Evidence ID、进入诊断结论,
// 而 evidence-grounding 只校验"结论是否引用了证据",不校验证据是否真实。
// 于是值班人员看到一份"有据可查"的根因,底下全是编造的 —— 这种错误在结论里
// 看不出来,只能在启动时挡住。
//
// 默认值是 mock,所以漏配 AIOPS_DATASOURCE 与显式配 mock 是同一种失败,
// 这里一并拒绝(此前二者都静默通过,而部署清单恰好从没设过这一项)。
var ErrMockInProduction = errors.New(
	"AIOPS_ENV=production 下不允许 mock 数据源(会产出虚假 K8s 证据):" +
		"请设 AIOPS_DATASOURCE=live,或仅在非生产环境使用 mock")

// isProduction 与 control-plane 的 config.IsProduction 保持同一口径。
func isProduction() bool {
	env := strings.TrimSpace(os.Getenv("AIOPS_ENV"))
	return strings.EqualFold(env, "production") || strings.EqualFold(env, "prod")
}

// FromEnv 依据 AIOPS_DATASOURCE(mock | live;默认 mock)选择实现,
// 返回模式标签供启动日志使用。
//
// 生产模式(AIOPS_ENV=production|prod)下解析出 mock 即返回 ErrMockInProduction,
// 由调用方 fail-fast —— 带着假数据源跑起来比起不来危险得多。
func FromEnv() (DataSource, string, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AIOPS_DATASOURCE"))) {
	case "live":
		return NewLive(LiveConfigFromEnv()), "live", nil
	default:
		if isProduction() {
			return nil, "mock", ErrMockInProduction
		}
		return NewMock(), "mock", nil
	}
}

// unavailable 为未接入的后端构造良性 Result,使工具优雅降级。
func unavailable(source, namespace, resource, why string) Result {
	return Result{
		Source:  source + "/unavailable",
		Summary: why,
		Raw: map[string]any{
			"available": false,
			"namespace": namespace,
			"resource":  resource,
			"reason":    why,
		},
		Freshness: "n/a",
	}
}

// liveResource 返回作用域资源名。
func liveResource(scope Scope) string { return scope.ResourceName() }

// orAll 用于摘要:空资源名显示为 "*"。
func orAll(s string) string {
	if strings.TrimSpace(s) == "" {
		return "*"
	}
	return s
}

// ---- Kubernetes 只读工具(本组件的核心职责)----

func (l *Live) GetWorkloadState(ctx context.Context, scope Scope, _ map[string]any) (Result, error) {
	if l.kube == nil {
		return unavailable("kubernetes", ns(scope), liveResource(scope),
			"未获得集群内 Kubernetes 配置,工作负载查询降级"), nil
	}
	return l.kube.workloadState(ctx, scope)
}

func (l *Live) GetKubernetesEvents(ctx context.Context, scope Scope, _ map[string]any) (Result, error) {
	if l.kube == nil {
		return unavailable("kubernetes", ns(scope), liveResource(scope),
			"未获得集群内 Kubernetes 配置,事件查询降级"), nil
	}
	return l.kube.events(ctx, scope)
}

func (l *Live) ListRecentChanges(ctx context.Context, scope Scope, _ map[string]any) (Result, error) {
	if l.kube == nil {
		return unavailable("kubernetes", ns(scope), liveResource(scope),
			"未获得集群内 Kubernetes 配置,变更查询降级"), nil
	}
	return l.kube.recentChanges(ctx, scope)
}

func (l *Live) InspectDependencies(ctx context.Context, scope Scope, _ map[string]any) (Result, error) {
	if l.kube == nil {
		return unavailable("kubernetes", ns(scope), liveResource(scope),
			"未获得集群内 Kubernetes 配置,依赖查询降级"), nil
	}
	return l.kube.dependencies(ctx, scope)
}
