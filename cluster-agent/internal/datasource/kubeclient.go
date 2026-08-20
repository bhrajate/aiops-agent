package datasource

// 把 live 数据源里的 Kubernetes 客户端暴露给同进程的其他组件(event watch)。
//
// 为什么不让 event watch 自己 newKubeReader:那会在同一进程里建**第二个**
// clientset,于是 QPS/Burst 限速(20/40,纵深防御,避免 agent 冲垮 API Server)
// 变成两份各限一份 —— 实际压力翻倍,而配置上看不出来。

import (
	"errors"

	"k8s.io/client-go/kubernetes"
)

// ErrNoKubeClient 表示当前数据源没有可用的 Kubernetes 客户端。
//
// live 模式下 NewLive 是"尽力构建":拿不到 kubeconfig 时 kube 为 nil,
// 各工具按粒度降级返回 unavailable。event watch 没有降级形态
// (它要么在 watch 要么没在),所以调用方应据此 fail-fast。
var ErrNoKubeClient = errors.New(
	"没有可用的 Kubernetes 客户端(检查 AIOPS_KUBECONFIG 或集群内 ServiceAccount)")

// KubeClient 返回数据源持有的 Kubernetes 客户端。
// 只有 live 数据源有;mock 返回 ErrNoKubeClient。
func KubeClient(ds DataSource) (kubernetes.Interface, error) {
	live, ok := ds.(*Live)
	if !ok || live.kube == nil || live.kube.client == nil {
		return nil, ErrNoKubeClient
	}
	return live.kube.client, nil
}
