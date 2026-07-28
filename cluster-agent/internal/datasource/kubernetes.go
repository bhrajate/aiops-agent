package datasource

// kubernetes.go:通过 client-go 进行只读 Kubernetes 访问。
//
// **只读**:本文件只会在强类型 clientset 上调用 Get / List,绝不调用
// Create/Update/Patch/Delete,也不触碰 exec/attach/portforward 等子资源,
// 更不会包装任何写动词。rest.Config 仅用于构建只读客户端。

import (
	"fmt"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// kubeReader 包装一个只读的 Kubernetes clientset。字段类型取接口,便于测试注入
// fake clientset。
type kubeReader struct {
	client kubernetes.Interface
}

// newKubeReader 按以下顺序构建只读客户端:显式 kubeconfig 路径、集群内配置、
// 默认的 ~/.kube/config。若没有可用配置则返回 error,让调用方优雅降级。
func newKubeReader(kubeconfig string) (*kubeReader, error) {
	cfg, err := restConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	// 防御性的只读姿态:无论如何都不会发出写动词,同时由于这是纯查询客户端,
	// QPS 也保持在温和水平。显式设置 QPS/Burst 是在客户端侧限速,避免 agent 冲垮
	// API Server(纵深防御),而不是依赖 client-go 的默认值。
	cfg.QPS = 20
	cfg.Burst = 40
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &kubeReader{client: cs}, nil
}

// newKubeReaderWithClient 是给 fake clientset 预留的测试注入点。
func newKubeReaderWithClient(c kubernetes.Interface) *kubeReader {
	return &kubeReader{client: c}
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	if home := homedir.HomeDir(); home != "" {
		def := filepath.Join(home, ".kube", "config")
		return clientcmd.BuildConfigFromFlags("", def)
	}
	return nil, fmt.Errorf("no in-cluster config and no kubeconfig available")
}
