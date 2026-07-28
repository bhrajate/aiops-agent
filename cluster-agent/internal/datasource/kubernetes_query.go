package datasource

// kubernetes_query.go:Live 中基于 Kubernetes 的工具所使用的只读查询方法。
// 所有方法只使用 Get/List。

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

// maxEventList 限制 Events().List 的分页大小,避免事件积压极多的命名空间迫使
// agent 缓冲无界的响应。
const maxEventList int64 = 500

// workloadState 报告该资源的 Deployment / ReplicaSet / Pod 健康状况。
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

// events 返回该资源(或命名空间)近期的 Kubernetes 事件。
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
		// 精确匹配目标资源,外加其子对象(名为 "<resource>-..." 的 Pod /
		// ReplicaSet),而不是用宽松的子串包含——那样会把无关的同级对象也捞进来。
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

// matchesResource 判断 involvedObject 的名字是否属于目标资源:完全相同,或是名为
// "<resource>-<后缀>" 的子对象(Deployment 下的 Pod / ReplicaSet)。它取代了宽松的
// 子串匹配——后者会把共享前缀的无关同级对象也算进来。
func matchesResource(objName, resource string) bool {
	return objName == resource || strings.HasPrefix(objName, resource+"-")
}
