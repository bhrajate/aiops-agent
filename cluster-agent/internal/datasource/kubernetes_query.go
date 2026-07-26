package datasource

// kubernetes_query.go: the read-only query methods used by Live's
// Kubernetes-backed tools. Every method uses only Get/List.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// maxEventList bounds the Events().List page size so a namespace with a huge
// event backlog cannot force the agent to buffer an unbounded response.
const maxEventList int64 = 500

// workloadState reports Deployment / ReplicaSet / Pod health for the resource.
func (k *kubeReader) workloadState(ctx context.Context, scope Scope) (Result, error) {
	namespace := ns(scope)
	name := liveResource(scope)
	if name == "" {
		return unavailable("kubernetes", namespace, "", "scope.resource.name 为空,无法定位工作负载"), nil
	}

	dep, err := k.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return unavailable("kubernetes", namespace, name, fmt.Sprintf("Deployment %s/%s 不存在", namespace, name)), nil
		}
		return Result{}, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}

	sel, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		sel = labels.Everything()
	}
	podList, err := k.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return Result{}, fmt.Errorf("list pods %s: %w", namespace, err)
	}

	pods := make([]map[string]any, 0, len(podList.Items))
	ready := 0
	for i := range podList.Items {
		p := &podList.Items[i]
		podReady, restarts := podReadyRestarts(p)
		if podReady {
			ready++
		}
		pods = append(pods, map[string]any{
			"name":          p.Name,
			"phase":         string(p.Status.Phase),
			"ready":         podReady,
			"restart_count": restarts,
			"node":          p.Spec.NodeName,
			"image":         primaryImage(p.Spec.Containers),
		})
	}

	desired := int32(0)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	summary := fmt.Sprintf("%s/%s(Deployment)期望副本 %d,就绪 %d,Pod 总数 %d;镜像 %s。",
		namespace, name, desired, ready, len(pods), primaryImage(dep.Spec.Template.Spec.Containers))
	raw := map[string]any{
		"cluster_id":       scope.ClusterID,
		"namespace":        namespace,
		"kind":             "Deployment",
		"name":             name,
		"desired_replicas": desired,
		"ready_replicas":   dep.Status.ReadyReplicas,
		"available":        dep.Status.AvailableReplicas,
		"updated_replicas": dep.Status.UpdatedReplicas,
		"image":            primaryImage(dep.Spec.Template.Spec.Containers),
		"pods":             pods,
	}
	return Result{Source: "kubernetes", Summary: summary, Raw: raw, Freshness: "live"}, nil
}

// events returns recent Kubernetes events for the resource (or namespace).
func (k *kubeReader) events(ctx context.Context, scope Scope) (Result, error) {
	namespace := ns(scope)
	name := liveResource(scope)

	evList, err := k.client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{Limit: maxEventList})
	if err != nil {
		return Result{}, fmt.Errorf("list events %s: %w", namespace, err)
	}

	events := make([]map[string]any, 0, len(evList.Items))
	warnings := 0
	for i := range evList.Items {
		e := &evList.Items[i]
		// Match the target resource exactly, plus its child objects (Pods /
		// ReplicaSets named "<resource>-..."), instead of a loose substring
		// contains, which would also catch unrelated siblings.
		if name != "" && !matchesResource(e.InvolvedObject.Name, name) {
			continue
		}
		if e.Type == corev1.EventTypeWarning {
			warnings++
		}
		events = append(events, map[string]any{
			"type":      e.Type,
			"reason":    e.Reason,
			"object":    e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
			"message":   e.Message,
			"count":     e.Count,
			"last_seen": eventLastSeen(e),
		})
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i]["last_seen"].(string) > events[j]["last_seen"].(string)
	})

	summary := fmt.Sprintf("%s/%s 命中 %d 条事件,其中 Warning %d 条。",
		namespace, orAll(name), len(events), warnings)
	raw := map[string]any{
		"cluster_id": scope.ClusterID,
		"namespace":  namespace,
		"resource":   name,
		"warnings":   warnings,
		"events":     events,
	}
	return Result{Source: "kubernetes", Summary: summary, Raw: raw, Freshness: "live"}, nil
}

// matchesResource reports whether an involvedObject name belongs to the target
// resource: an exact match, or a child object named "<resource>-<suffix>"
// (Pods / ReplicaSets under a Deployment). This replaces a loose substring
// match, which would also catch unrelated siblings sharing a prefix.
func matchesResource(objName, resource string) bool {
	return objName == resource || strings.HasPrefix(objName, resource+"-")
}
