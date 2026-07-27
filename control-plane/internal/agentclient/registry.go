package agentclient

import (
	"fmt"
	"sort"
	"strings"
)

// Registry 按 cluster_id 路由到对应集群的 Cluster Agent(每集群一个 Agent)。
//
// 多集群语义:Tool Gateway 必须把工具调用发到 Incident 所属集群的 Agent。
// 未配置该集群时**明确拒绝**,而不是回退到某个默认 Agent —— 打错集群等于
// 跨集群越权读取,属于隔离事故。
type Registry struct {
	byCluster map[string]*Client
	fallback  *Client // 单集群兼容:仅当未配置任何 per-cluster 映射时使用
}

// ErrNoAgent 表示该集群没有配置 Agent。
type ErrNoAgent struct{ ClusterID string }

func (e *ErrNoAgent) Error() string {
	return fmt.Sprintf("no cluster-agent configured for cluster %q", e.ClusterID)
}

// NewRegistry 用 cluster_id→Client 映射构造。fallback 可为 nil。
func NewRegistry(byCluster map[string]*Client, fallback *Client) *Registry {
	if byCluster == nil {
		byCluster = map[string]*Client{}
	}
	return &Registry{byCluster: byCluster, fallback: fallback}
}

// For 返回目标集群的 Agent 客户端。
func (r *Registry) For(clusterID string) (*Client, error) {
	if c, ok := r.byCluster[clusterID]; ok {
		return c, nil
	}
	// 仅在完全没有 per-cluster 映射时,才允许单集群 fallback(开发/单集群部署)。
	if len(r.byCluster) == 0 && r.fallback != nil {
		return r.fallback, nil
	}
	return nil, &ErrNoAgent{ClusterID: clusterID}
}

// Clusters 返回已配置的集群列表(便于启动日志与健康检查)。
func (r *Registry) Clusters() []string {
	out := make([]string, 0, len(r.byCluster))
	for k := range r.byCluster {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ParseAgentMap 解析 "cluster1=https://a:9100,cluster2=https://b:9100" 形式的配置。
// 返回 cluster_id→URL。空串返回空 map(表示未配置 per-cluster 映射)。
func ParseAgentMap(s string) (map[string]string, error) {
	out := map[string]string{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out, nil
	}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("invalid AIOPS_CLUSTER_AGENTS entry %q (want cluster_id=url)", pair)
		}
		cluster := strings.TrimSpace(kv[0])
		url := strings.TrimSpace(kv[1])
		if cluster == "" || url == "" {
			return nil, fmt.Errorf("invalid AIOPS_CLUSTER_AGENTS entry %q", pair)
		}
		out[cluster] = url
	}
	return out, nil
}
