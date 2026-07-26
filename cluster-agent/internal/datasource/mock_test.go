package datasource

import (
	"context"
	"reflect"
	"testing"
)

// toolMethods enumerates every DataSource method for table-driven coverage.
func toolMethods(ds DataSource) map[string]func(context.Context, Scope, map[string]any) (Result, error) {
	return map[string]func(context.Context, Scope, map[string]any) (Result, error){
		"get_workload_state":    ds.GetWorkloadState,
		"get_kubernetes_events": ds.GetKubernetesEvents,
		"query_metrics":         ds.QueryMetrics,
		"search_logs":           ds.SearchLogs,
		"get_traces":            ds.GetTraces,
		"list_recent_changes":   ds.ListRecentChanges,
		"inspect_dependencies":  ds.InspectDependencies,
	}
}

func TestMockAllToolsNonEmpty(t *testing.T) {
	ds := NewMock()
	scopes := []Scope{
		{ClusterID: "prod-cn-1", Namespace: "payment", Resource: ResourceRef{Name: "checkout"}},
		{ClusterID: "prod-cn-1", Namespace: "cart", Resource: ResourceRef{Name: "cart-session"}},
		{ClusterID: "prod-cn-1", Namespace: "inventory", Resource: ResourceRef{Name: "stock-api"}},
		{ClusterID: "prod-cn-1", Namespace: "orders", Resource: ResourceRef{Name: "orders-api"}},
	}
	for _, scope := range scopes {
		for name, fn := range toolMethods(ds) {
			res, err := fn(context.Background(), scope, nil)
			if err != nil {
				t.Fatalf("%s/%s: unexpected error: %v", scope.Namespace, name, err)
			}
			if res.Source == "" {
				t.Errorf("%s/%s: empty source", scope.Namespace, name)
			}
			if res.Summary == "" {
				t.Errorf("%s/%s: empty summary", scope.Namespace, name)
			}
			if res.Freshness == "" {
				t.Errorf("%s/%s: empty freshness", scope.Namespace, name)
			}
			if res.Raw == nil {
				t.Errorf("%s/%s: nil raw", scope.Namespace, name)
			}
		}
	}
}

func TestMockDeterministic(t *testing.T) {
	scope := Scope{ClusterID: "prod-cn-1", Namespace: "payment", Resource: ResourceRef{Name: "checkout"}}
	a, _ := NewMock().QueryMetrics(context.Background(), scope, nil)
	b, _ := NewMock().QueryMetrics(context.Background(), scope, nil)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("mock output is not deterministic for identical scope")
	}
}

func TestScopeInjectionIntoRaw(t *testing.T) {
	scope := Scope{ClusterID: "edge-eu-2", Namespace: "payment", Resource: ResourceRef{Name: "checkout"}}
	res, err := NewMock().GetWorkloadState(context.Background(), scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := res.Raw.(map[string]any)
	if !ok {
		t.Fatalf("raw is not a map: %T", res.Raw)
	}
	if raw["cluster_id"] != "edge-eu-2" {
		t.Errorf("cluster_id not injected into raw: got %v", raw["cluster_id"])
	}
	if raw["namespace"] != "payment" {
		t.Errorf("namespace not injected into raw: got %v", raw["namespace"])
	}
}

func TestReleaseRegressionStoryIsVersionScoped(t *testing.T) {
	scope := Scope{ClusterID: "prod-cn-1", Namespace: "payment", Resource: ResourceRef{Name: "checkout"}}
	res, err := NewMock().QueryMetrics(context.Background(), scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := res.Raw.(map[string]any)
	series, ok := raw["series"].([]map[string]any)
	if !ok || len(series) != 2 {
		t.Fatalf("expected 2 version-scoped series, got %#v", raw["series"])
	}
	// Old-version series must stay at baseline; new-version series must peak higher.
	var oldPeak, newPeak float64
	for _, s := range series {
		pts := s["points"].([]map[string]any)
		last := pts[len(pts)-1]["v"].(float64)
		if s["metric"].(map[string]string)["surface"] == "old" {
			oldPeak = last
		} else {
			newPeak = last
		}
	}
	if !(newPeak > oldPeak) {
		t.Errorf("release regression should concentrate errors on new version: new=%v old=%v", newPeak, oldPeak)
	}
}

func TestFaultCategoriesCovered(t *testing.T) {
	want := map[string]string{
		"payment":   CatReleaseRegression,
		"cart":      CatPodCrashLoop,
		"inventory": CatResourceBottle,
		"orders":    CatDependencyTimeout,
	}
	for nsName, cat := range want {
		s := resolveScenario(Scope{Namespace: nsName})
		if s.category != cat {
			t.Errorf("namespace %s: expected category %s, got %s", nsName, cat, s.category)
		}
	}
}
