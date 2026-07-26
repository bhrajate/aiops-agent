package datasource

// kubernetes_topology.go: read-only change history (ReplicaSet revisions) and
// dependency topology (Service selectors) plus shared pod/container helpers.

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// (helpers below are shared by the read-only Kubernetes query methods)

// recentChanges derives deploy history from ReplicaSet revisions owned by the
// Deployment (each revision = one rollout / image change).
func (k *kubeReader) recentChanges(ctx context.Context, scope Scope) (Result, error) {
	namespace := ns(scope)
	name := liveResource(scope)
	if name == "" {
		return unavailable("change-intel", namespace, "", "scope.resource.name 为空,无法定位变更历史"), nil
	}

	rsList, err := k.client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("list replicasets %s: %w", namespace, err)
	}

	type rev struct {
		revision int
		rs       *appsv1.ReplicaSet
	}
	revs := make([]rev, 0)
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if !ownedBy(rs.OwnerReferences, "Deployment", name) {
			continue
		}
		r, _ := strconv.Atoi(rs.Annotations["deployment.kubernetes.io/revision"])
		revs = append(revs, rev{revision: r, rs: rs})
	}
	sort.SliceStable(revs, func(i, j int) bool { return revs[i].revision > revs[j].revision })

	changes := make([]map[string]any, 0, len(revs))
	for _, rv := range revs {
		changes = append(changes, map[string]any{
			"change_id": fmt.Sprintf("rs/%s@rev%d", rv.rs.Name, rv.revision),
			"type":      "deploy",
			"revision":  rv.revision,
			"at":        rv.rs.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z07:00"),
			"image":     primaryImage(rv.rs.Spec.Template.Spec.Containers),
			"replicas":  rv.rs.Status.Replicas,
		})
	}

	summary := fmt.Sprintf("%s/%s 共 %d 个 ReplicaSet 版本;最新 revision 镜像 %s。",
		namespace, name, len(changes), latestImage(changes))
	raw := map[string]any{
		"cluster_id": scope.ClusterID,
		"namespace":  namespace,
		"resource":   name,
		"changes":    changes,
	}
	return Result{Source: "change-intel", Summary: summary, Raw: raw, Freshness: "live"}, nil
}

// dependencies maps the Deployment's pod labels to the Services that select it,
// yielding upstream edges (Service -> workload). Confidence is medium because
// Kubernetes Service selectors only express ingress, not the full call graph.
func (k *kubeReader) dependencies(ctx context.Context, scope Scope) (Result, error) {
	namespace := ns(scope)
	name := liveResource(scope)
	if name == "" {
		return unavailable("topology", namespace, "", "scope.resource.name 为空,无法定位依赖拓扑"), nil
	}

	dep, err := k.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return unavailable("topology", namespace, name, fmt.Sprintf("Deployment %s/%s 不存在", namespace, name)), nil
		}
		return Result{}, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	podLabels := labels.Set(dep.Spec.Template.Labels)

	svcList, err := k.client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("list services %s: %w", namespace, err)
	}

	edges := make([]map[string]any, 0)
	for i := range svcList.Items {
		svc := &svcList.Items[i]
		if len(svc.Spec.Selector) == 0 {
			continue
		}
		if labels.SelectorFromSet(svc.Spec.Selector).Matches(podLabels) {
			edges = append(edges, map[string]any{
				"from":       svc.Name,
				"to":         name,
				"kind":       "service-frontend",
				"source":     "kubernetes-service",
				"confidence": 0.7,
			})
		}
	}

	summary := fmt.Sprintf("%s/%s 由 %d 个 Service 选中(上游入口);拓扑来自 Kubernetes Service selector,置信度中。",
		namespace, name, len(edges))
	raw := map[string]any{
		"cluster_id":   scope.ClusterID,
		"namespace":    namespace,
		"resource":     name,
		"edges":        edges,
		"blast_radius": map[string]int{"services": len(edges), "namespaces": 1},
	}
	return Result{Source: "topology", Summary: summary, Raw: raw, Freshness: "live"}, nil
}

// --- shared read-only helpers ---

func podReadyRestarts(p *corev1.Pod) (ready bool, restarts int32) {
	for _, c := range p.Status.ContainerStatuses {
		restarts += c.RestartCount
	}
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	return ready, restarts
}

func primaryImage(cs []corev1.Container) string {
	if len(cs) == 0 {
		return ""
	}
	return cs[0].Image
}

func ownedBy(refs []metav1.OwnerReference, kind, name string) bool {
	for _, r := range refs {
		if r.Kind == kind && r.Name == name {
			return true
		}
	}
	return false
}

func eventLastSeen(e *corev1.Event) string {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return e.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z07:00")
}

func latestImage(changes []map[string]any) string {
	if len(changes) == 0 {
		return "(无)"
	}
	if img, ok := changes[0]["image"].(string); ok && img != "" {
		return img
	}
	return "(未知)"
}
