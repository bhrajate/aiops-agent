package datasource

// kubernetes.go: read-only Kubernetes access via client-go.
//
// READ-ONLY: this file only ever calls Get / List on the typed clientset.
// It never calls Create/Update/Patch/Delete or any exec/attach/portforward
// subresource, and it never wraps a write verb. The rest.Config is used
// solely to build a read client.

import (
	"fmt"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// kubeReader wraps a read-only Kubernetes clientset. The field type is the
// interface so tests can inject a fake clientset.
type kubeReader struct {
	client kubernetes.Interface
}

// newKubeReader builds a read client from (in order): explicit kubeconfig path,
// in-cluster config, then the default ~/.kube/config. It returns an error when
// no usable config exists, letting the caller degrade gracefully.
func newKubeReader(kubeconfig string) (*kubeReader, error) {
	cfg, err := restConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	// Defensive read-only posture: no write verbs are ever issued regardless,
	// but we also keep QPS modest since this is a query-only client. Explicit
	// QPS/Burst caps the client-side rate so the agent cannot storm the API
	// server (defense-in-depth), instead of relying on client-go's defaults.
	cfg.QPS = 20
	cfg.Burst = 40
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &kubeReader{client: cs}, nil
}

// newKubeReaderWithClient is the test seam for fake clientsets.
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
