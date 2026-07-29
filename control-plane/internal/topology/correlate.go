// Package topology 维护服务依赖拓扑,并用它做 incident 关联。
//
// 解决的问题:相关性合并只按 tenant|cluster|namespace,调用链上的故障传播识别不了。
// checkout 挂了导致 payment-api 超时,值班人员看到两个互不相关的 incident,
// 而根因只有一个 —— 他们得自己在脑子里把这两条连起来。
//
// 两件事:
//  1. **回填 `incidents.topology_refs`**(此前恒为 '[]')。它经 getContext 下发给
//     planner,规划器因此知道"这个服务的上游是谁",能把查询指向调用链而非只看自己。
//  2. **链接拓扑相邻的活跃 incident**,并标明方向 —— 上游那个更可能是根因。
//
// 刻意不合并 incident,理由见 store/incidentrelations.go:拆分比合并难得多。
package topology

import (
	"context"
	"log/slog"

	"github.com/aiops/control-plane/internal/model"
	"github.com/aiops/control-plane/internal/store"
)

// Config 关联阈值。
type Config struct {
	// MaxEdgeAgeSec 边的最大年龄。服务下线后边停止刷新,继续用它做关联会产出
	// "疑似与一个早已不存在的服务同源"这种误导结论。
	MaxEdgeAgeSec int
	// MinConfidence 参与关联的最低置信度。
	//
	// 这个阈值是**误报与漏报的分界**,取值有实质影响:K8s Service selector 推导的
	// 边只表达入口关系(confidence 0.7),用它链接 incident 会把"同一 Service 后面
	// 的两个无关工作负载"判为同源。Tempo service graph 是真实调用(0.9)。
	// 默认 0.8 即"只用真实调用关系做链接",selector 边仍进 topology_refs 供
	// planner 参考,但不足以下关联结论。
	MinConfidence float64
	// MinLinkConfidence 链接 incident 的最低置信度(高于 MinConfidence)。
	// 回填 topology_refs 比链接 incident 宽松:前者只是给 planner 更多上下文,
	// 错了代价小;后者会出现在值班人员的界面上,错了会误导排查方向。
	MinLinkConfidence float64
}

// DefaultConfig 默认阈值。
func DefaultConfig() Config {
	return Config{
		MaxEdgeAgeSec:     3600,
		MinConfidence:     0.5,
		MinLinkConfidence: 0.8,
	}
}

// Correlator 用拓扑丰富 incident。
type Correlator struct {
	store *store.Store
	cfg   Config
	log   *slog.Logger
}

func New(s *store.Store, cfg Config, log *slog.Logger) *Correlator {
	if cfg.MaxEdgeAgeSec <= 0 {
		cfg = DefaultConfig()
	}
	return &Correlator{store: s, cfg: cfg, log: log}
}

// Enrich 为一个 incident 回填拓扑上下文并链接相邻的活跃 incident。
//
// 幂等:重复调用得到相同结果(链接用 upsert,refs 是整体覆盖)。
// 任何一步失败只记日志不返回错误 —— 拓扑关联是**增强**,它失败不该让信号处理失败。
// 这是刻意的取舍:丢一次关联远好过丢一条告警。
func (c *Correlator) Enrich(ctx context.Context, inc model.Incident) {
	svc := primaryService(inc)
	if svc == "" {
		return
	}
	ns := primaryNamespace(inc)

	up, down, err := c.store.NeighborsOf(ctx, inc.TenantID, inc.ClusterID, svc,
		c.cfg.MaxEdgeAgeSec, c.cfg.MinConfidence)
	if err != nil {
		c.log.Warn("topology lookup failed (incident 处理不受影响)",
			"incident_id", inc.IncidentID, "service", svc, "err", err)
		return
	}
	if len(up) == 0 && len(down) == 0 {
		return
	}

	// 1) 回填 topology_refs
	refs := make([]any, 0, len(up)+len(down))
	for _, e := range up {
		refs = append(refs, edgeRef(e, "upstream", e.FromService, e.FromNamespace))
	}
	for _, e := range down {
		refs = append(refs, edgeRef(e, "downstream", e.ToService, e.ToNamespace))
	}
	if err := c.store.SetIncidentTopologyRefs(ctx, inc.IncidentID, refs); err != nil {
		c.log.Warn("write topology_refs failed", "incident_id", inc.IncidentID, "err", err)
	}

	// 2) 链接拓扑相邻的活跃 incident
	linked := 0
	for _, e := range up {
		if c.link(ctx, inc, e, "upstream", e.FromService, e.FromNamespace, ns) {
			linked++
		}
	}
	for _, e := range down {
		if c.link(ctx, inc, e, "downstream", e.ToService, e.ToNamespace, ns) {
			linked++
		}
	}
	if linked > 0 {
		c.log.Info("incident linked via topology", "incident_id", inc.IncidentID,
			"service", svc, "linked", linked)
	}
}

// link 尝试把 inc 与邻居服务上的活跃 incident 关联。返回是否建立了关联。
func (c *Correlator) link(ctx context.Context, inc model.Incident, e store.TopologyEdge,
	relation, neighborSvc, neighborNS, ownNS string) bool {
	if e.Confidence < c.cfg.MinLinkConfidence {
		return false // 置信度不足以下关联结论,但已进 topology_refs 供 planner 参考
	}
	// 邻居的 namespace 未知时(Tempo 侧常见)退回本 incident 的 namespace:
	// 跨 namespace 的调用存在,但同 namespace 是更可能的情形,
	// 且这里宁可漏关联也不要错关联。
	if neighborNS == "" {
		neighborNS = ownNS
	}
	relatedID, ok, err := c.store.ActiveIncidentByService(ctx, inc.TenantID, inc.ClusterID,
		neighborNS, neighborSvc)
	if err != nil || !ok || relatedID == inc.IncidentID {
		return false
	}
	via := map[string]any{
		"from": e.FromService, "to": e.ToService, "kind": e.Kind,
		"source": e.Source, "request_rate": e.RequestRate,
	}
	if err := c.store.LinkIncidents(ctx, inc.TenantID, inc.IncidentID, relatedID,
		relation, via, e.Confidence); err != nil {
		c.log.Warn("link incidents failed", "incident_id", inc.IncidentID,
			"related", relatedID, "err", err)
		return false
	}
	return true
}

func edgeRef(e store.TopologyEdge, direction, service, namespace string) map[string]any {
	return map[string]any{
		"direction":    direction,
		"service":      service,
		"namespace":    namespace,
		"kind":         e.Kind,
		"source":       e.Source,
		"confidence":   e.Confidence,
		"request_rate": e.RequestRate,
	}
}

// primaryService 取 incident 的主服务名(裸名,不带 namespace 前缀)。
//
// 用 model.WorkloadName 而不是裸 resource name:拓扑里的名字是工作负载/服务级的,
// 而 incident 的资源可能是 Pod(checkout-7d9f-abc12)。不归约就永远匹配不上,
// 关联静默失效 —— 与 F3 修过的 blast_radius 同一类坑。
//
// 也不用 ServiceKey:它带 "namespace/" 前缀,而 service_topology 存的是裸名
// (Tempo 的 client/server 标签就是裸名)。
func primaryService(inc model.Incident) string {
	for _, r := range inc.AffectedResources {
		if k := model.WorkloadName(r); k != "" {
			return k
		}
	}
	return ""
}

func primaryNamespace(inc model.Incident) string {
	for _, r := range inc.AffectedResources {
		if r.Namespace != "" {
			return r.Namespace
		}
	}
	return ""
}
