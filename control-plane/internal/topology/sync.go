package topology

// 从 Tempo service graph 指标同步服务依赖边。
//
// 为什么用 Tempo 的 service graph 而不是 Kubernetes Service selector:
// selector 只表达**入口**关系(哪个 Service 选中了哪个工作负载),不是调用图 ——
// 它回答不了"checkout 调用了谁"。而 Tempo 的 metrics-generator 从 trace 的父子
// span 推导真实调用关系,导出 traces_service_graph_request_total{client,server,
// connection_type},每条时间序列就是一条边。
//
// 这些指标落在 Prometheus 里,而控制面已经连了 Prometheus ——
// 所以这条同步不需要任何新的基础设施,也不需要 cluster-agent 参与。
//
// 降级:未启用 metrics-generator 时该指标不存在,同步查到 0 条边并**记录一次告警**,
// 而不是静默空转 —— 否则"拓扑关联没生效"会毫无线索。

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/aiops/control-plane/internal/obsquery"
	"github.com/aiops/control-plane/internal/store"
)

// serviceGraphExpr 是硬编码的 PromQL(**不接受外部输入**,见 obsquery/internalquery.go)。
//
// 用 5 分钟窗口的 rate 而非 increase:窗口太短会漏掉低频调用(定时任务类),
// 太长会让已下线的依赖迟迟不消失。5 分钟是常见的折中,也与 Tempo 文档示例一致。
// 只保留 rate > 0 的边:rate=0 表示窗口内没有调用,那条依赖此刻不成立。
const serviceGraphExpr = `sum by (client, server, connection_type) ` +
	`(rate(traces_service_graph_request_total[5m])) > 0`

// SourceTempo 标记边来自 Tempo service graph。
const SourceTempo = "tempo-service-graph"

// tempoConfidence 是 Tempo 边的置信度。
//
// 取 0.9 而非 1.0:service graph 由 span 的父子关系推导,存在两类已知偏差 ——
// 未插桩的中间服务会被跳过(A→C 实际经过 B),以及 virtual_node 表示的
// 推断节点。0.9 高于 MinLinkConfidence(0.8),所以它足以支撑 incident 关联;
// 而 K8s selector 边(0.7)不足以。
const tempoConfidence = 0.9

// Querier 是同步所需的最小查询能力(便于测试替换)。
type Querier interface {
	InternalInstantQuery(ctx context.Context, expr, clusterID string) ([]obsquery.InstantSample, error)
	HasPrometheus() bool
}

// SyncMetrics 记录同步结果。
type SyncMetrics interface {
	ObserveTopologySync(edges int, err error)
}

// Syncer 周期性把 service graph 同步进 service_topology。
type Syncer struct {
	store     *store.Store
	q         Querier
	tenantID  string
	clusterID string
	interval  time.Duration
	metrics   SyncMetrics // 可为 nil
	log       *slog.Logger
	// warnedEmpty 保证"查到 0 条边"的告警只在状态变化时打一次,
	// 否则每个周期一条,会把日志淹掉。
	warnedEmpty bool
}

func NewSyncer(s *store.Store, q Querier, tenantID, clusterID string,
	interval time.Duration, log *slog.Logger) *Syncer {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Syncer{store: s, q: q, tenantID: tenantID, clusterID: clusterID,
		interval: interval, log: log}
}

// WithMetrics 注入指标记录器。
func (s *Syncer) WithMetrics(m SyncMetrics) *Syncer {
	s.metrics = m
	return s
}

// Run 阻塞运行同步循环,直到 ctx 取消。启动时立即同步一次 ——
// 否则首个周期内(默认 5 分钟)的 incident 拿不到任何拓扑上下文。
func (s *Syncer) Run(ctx context.Context) {
	s.log.Info("topology syncer started", "interval", s.interval,
		"source", SourceTempo)
	s.syncOnce(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.syncOnce(ctx)
		}
	}
}

func (s *Syncer) syncOnce(ctx context.Context) {
	// 单次同步自带超时:Prometheus 慢查询不该让同步循环卡死,
	// 下个周期重试即可(拓扑是幂等 upsert)。
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	samples, err := s.q.InternalInstantQuery(qctx, serviceGraphExpr, s.clusterID)
	if err != nil {
		s.log.Warn("topology sync query failed (下个周期重试)", "err", err)
		if s.metrics != nil {
			s.metrics.ObserveTopologySync(0, err)
		}
		return
	}

	edges := s.toEdges(samples)
	if len(edges) == 0 {
		if !s.warnedEmpty {
			// 只在状态变化时告警一次。这条告警很重要:未启用 metrics-generator 时
			// 拓扑关联会完全不生效,而那在别处看不出任何异常 ——
			// incident 照常创建、诊断照常产出,只是少了调用链上下文。
			s.log.Warn("service graph 查到 0 条边:拓扑关联不会生效。" +
				"确认 Tempo 已启用 metrics-generator 的 service-graphs 处理器," +
				"且其指标已写入本控制面所连的 Prometheus")
			s.warnedEmpty = true
		}
		if s.metrics != nil {
			s.metrics.ObserveTopologySync(0, nil)
		}
		return
	}
	s.warnedEmpty = false

	n, err := s.store.UpsertTopologyEdges(ctx, edges)
	if err != nil {
		s.log.Warn("topology upsert failed", "err", err)
		if s.metrics != nil {
			s.metrics.ObserveTopologySync(0, err)
		}
		return
	}
	s.log.Info("topology synced", "edges", n)
	if s.metrics != nil {
		s.metrics.ObserveTopologySync(n, nil)
	}
}

// toEdges 把 service graph 样本转成拓扑边。
func (s *Syncer) toEdges(samples []obsquery.InstantSample) []store.TopologyEdge {
	out := make([]store.TopologyEdge, 0, len(samples))
	now := time.Now().UTC()
	for _, sm := range samples {
		client := strings.TrimSpace(sm.Labels["client"])
		server := strings.TrimSpace(sm.Labels["server"])
		if client == "" || server == "" || client == server {
			// 自环无意义(同服务内部 span),残缺边在关联时匹配不上任何资源。
			continue
		}
		out = append(out, store.TopologyEdge{
			TenantID:    s.tenantID,
			ClusterID:   s.clusterID,
			FromService: client,
			ToService:   server,
			// Tempo 侧不带 namespace(service.name 是裸名)。留空,
			// 关联时退回 incident 自身的 namespace —— 见 correlate.go 的 link()。
			Kind:        connectionKind(sm.Labels["connection_type"]),
			Source:      SourceTempo,
			Confidence:  tempoConfidence,
			RequestRate: sm.Value,
			LastSeen:    now,
		})
	}
	return out
}

// connectionKind 把 Tempo 的 connection_type 映射为本系统的边类型。
//
// Tempo 取值:unset / virtual_node / messaging_system / database。
// virtual_node 表示对端未插桩、由 Tempo 推断出来的节点 —— 单独标出来,
// 因为它比真实节点更可能是幻影(实际可能是若干个服务被折叠成一个)。
func connectionKind(ct string) string {
	switch strings.TrimSpace(strings.ToLower(ct)) {
	case "database":
		return "database"
	case "messaging_system":
		return "messaging"
	case "virtual_node":
		return "virtual"
	default:
		return "call"
	}
}
