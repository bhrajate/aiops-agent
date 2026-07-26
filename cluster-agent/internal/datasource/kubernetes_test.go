package datasource

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func i32(v int32) *int32 { return &v }

func fakeReader() *kubeReader {
	labelsSel := map[string]string{"app": "checkout"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "payment"},
		Spec: appsv1.DeploymentSpec{
			Replicas: i32(3),
			Selector: &metav1.LabelSelector{MatchLabels: labelsSel},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labelsSel},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry/checkout:v2.3.0"}}},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 2, AvailableReplicas: 2, UpdatedReplicas: 3},
	}
	rsOld := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-old", Namespace: "payment",
			Annotations:       map[string]string{"deployment.kubernetes.io/revision": "1"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
			OwnerReferences:   []metav1.OwnerReference{{Kind: "Deployment", Name: "checkout"}},
		},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "registry/checkout:v2.2.4"}}}}},
	}
	rsNew := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-new", Namespace: "payment",
			Annotations:       map[string]string{"deployment.kubernetes.io/revision": "2"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute)),
			OwnerReferences:   []metav1.OwnerReference{{Kind: "Deployment", Name: "checkout"}},
		},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "registry/checkout:v2.3.0"}}}}},
	}
	podReady := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-0", Namespace: "payment", Labels: labelsSel},
		Spec:       corev1.PodSpec{NodeName: "node-0", Containers: []corev1.Container{{Image: "registry/checkout:v2.3.0"}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{RestartCount: 0}},
		},
	}
	podBad := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1", Namespace: "payment", Labels: labelsSel},
		Spec:       corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Image: "registry/checkout:v2.3.0"}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			ContainerStatuses: []corev1.ContainerStatus{{RestartCount: 5}},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-svc", Namespace: "payment"},
		Spec:       corev1.ServiceSpec{Selector: labelsSel},
	}
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "ev1", Namespace: "payment"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "checkout-1"},
		Type:           corev1.EventTypeWarning,
		Reason:         "Unhealthy",
		Message:        "Readiness probe failed",
		Count:          12,
		LastTimestamp:  metav1.NewTime(time.Now().Add(-2 * time.Minute)),
	}
	cs := fake.NewSimpleClientset(dep, rsOld, rsNew, podReady, podBad, svc, ev)
	return newKubeReaderWithClient(cs)
}

func TestKubeWorkloadState(t *testing.T) {
	res, err := fakeReader().workloadState(context.Background(), liveScope())
	if err != nil {
		t.Fatalf("workloadState: %v", err)
	}
	if res.Source != "kubernetes" {
		t.Errorf("source = %q", res.Source)
	}
	raw := res.Raw.(map[string]any)
	pods := raw["pods"].([]map[string]any)
	if len(pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(pods))
	}
	if raw["image"].(string) != "registry/checkout:v2.3.0" {
		t.Errorf("image = %v", raw["image"])
	}
}

func TestKubeEventsFilteredByResource(t *testing.T) {
	res, err := fakeReader().events(context.Background(), liveScope())
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	raw := res.Raw.(map[string]any)
	if raw["warnings"].(int) != 1 {
		t.Errorf("warnings = %v", raw["warnings"])
	}
	events := raw["events"].([]map[string]any)
	if len(events) != 1 {
		t.Fatalf("expected 1 event for checkout, got %d", len(events))
	}
}

func TestKubeRecentChangesRevisionOrder(t *testing.T) {
	res, err := fakeReader().recentChanges(context.Background(), liveScope())
	if err != nil {
		t.Fatalf("recentChanges: %v", err)
	}
	raw := res.Raw.(map[string]any)
	changes := raw["changes"].([]map[string]any)
	if len(changes) != 2 {
		t.Fatalf("expected 2 revisions, got %d", len(changes))
	}
	// Newest revision (2) first, carrying the v2.3.0 image.
	if changes[0]["revision"].(int) != 2 {
		t.Errorf("first revision = %v, want 2", changes[0]["revision"])
	}
	if changes[0]["image"].(string) != "registry/checkout:v2.3.0" {
		t.Errorf("latest image = %v", changes[0]["image"])
	}
}

func TestKubeDependenciesMatchService(t *testing.T) {
	res, err := fakeReader().dependencies(context.Background(), liveScope())
	if err != nil {
		t.Fatalf("dependencies: %v", err)
	}
	raw := res.Raw.(map[string]any)
	edges := raw["edges"].([]map[string]any)
	if len(edges) != 1 {
		t.Fatalf("expected 1 service edge, got %d", len(edges))
	}
	if edges[0]["from"].(string) != "checkout-svc" {
		t.Errorf("edge.from = %v", edges[0]["from"])
	}
}

func TestKubeWorkloadStateNotFound(t *testing.T) {
	// Empty clientset: the Deployment does not exist -> unavailable, not error.
	kr := newKubeReaderWithClient(fake.NewSimpleClientset())
	res, err := kr.workloadState(context.Background(), liveScope())
	if err != nil {
		t.Fatalf("expected graceful unavailable, got error: %v", err)
	}
	if res.Source != "kubernetes/unavailable" {
		t.Errorf("source = %q, want kubernetes/unavailable", res.Source)
	}
	if res.Raw.(map[string]any)["available"].(bool) {
		t.Error("expected available=false for missing deployment")
	}
}

func TestKubeDependenciesNotFound(t *testing.T) {
	kr := newKubeReaderWithClient(fake.NewSimpleClientset())
	res, err := kr.dependencies(context.Background(), liveScope())
	if err != nil {
		t.Fatalf("expected graceful unavailable, got error: %v", err)
	}
	if res.Source != "topology/unavailable" {
		t.Errorf("source = %q, want topology/unavailable", res.Source)
	}
}

func TestKubeConformsToDataSourceViaLive(t *testing.T) {
	// Ensure the fake-backed reader plugs into Live and the interface holds.
	l := &Live{kube: fakeReader(), now: time.Now}
	var _ DataSource = l
	if _, err := l.GetWorkloadState(context.Background(), liveScope(), nil); err != nil {
		t.Fatalf("Live.GetWorkloadState via fake: %v", err)
	}
}
