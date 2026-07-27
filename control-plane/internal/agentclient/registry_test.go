package agentclient

import "testing"

func TestParseAgentMap(t *testing.T) {
	m, err := ParseAgentMap("prod-cn-1=https://a:9100, edge-eu-2=https://b:9100")
	if err != nil {
		t.Fatal(err)
	}
	if m["prod-cn-1"] != "https://a:9100" || m["edge-eu-2"] != "https://b:9100" {
		t.Errorf("parse mismatch: %+v", m)
	}
	// 空串 = 未配置多集群
	if m2, err := ParseAgentMap(""); err != nil || len(m2) != 0 {
		t.Errorf("empty should yield empty map, got %+v err=%v", m2, err)
	}
	// 非法项必须报错(不能静默忽略,否则会退化成单集群误路由)
	for _, bad := range []string{"noequals", "=url", "cluster="} {
		if _, err := ParseAgentMap(bad); err == nil {
			t.Errorf("invalid entry %q should error", bad)
		}
	}
}

func TestRegistryRoutesByCluster(t *testing.T) {
	a := New("http://a:9100")
	b := New("http://b:9100")
	r := NewRegistry(map[string]*Client{"prod-cn-1": a, "edge-eu-2": b}, nil)

	got, err := r.For("prod-cn-1")
	if err != nil || got != a {
		t.Errorf("prod-cn-1 应路由到 a, err=%v", err)
	}
	got, err = r.For("edge-eu-2")
	if err != nil || got != b {
		t.Errorf("edge-eu-2 应路由到 b, err=%v", err)
	}
}

func TestRegistryRefusesUnknownCluster(t *testing.T) {
	// 隔离红线:未配置的集群必须拒绝,不得回退到别的 Agent(否则跨集群越权读)
	fallback := New("http://fallback:9100")
	r := NewRegistry(map[string]*Client{"prod-cn-1": New("http://a:9100")}, fallback)
	if _, err := r.For("unknown-cluster"); err == nil {
		t.Fatal("未配置的集群必须拒绝,不能回退")
	} else if _, ok := err.(*ErrNoAgent); !ok {
		t.Errorf("应返回 ErrNoAgent, got %T", err)
	}
}

func TestRegistrySingleClusterFallback(t *testing.T) {
	// 完全未配置 per-cluster 映射时,允许单集群兼容模式
	fb := New("http://only:9100")
	r := NewRegistry(nil, fb)
	got, err := r.For("any-cluster")
	if err != nil || got != fb {
		t.Errorf("单集群模式应回退到 fallback, err=%v", err)
	}
}

func TestRegistryClusters(t *testing.T) {
	r := NewRegistry(map[string]*Client{"b": New("u"), "a": New("u")}, nil)
	cs := r.Clusters()
	if len(cs) != 2 || cs[0] != "a" || cs[1] != "b" {
		t.Errorf("Clusters 应排序返回, got %v", cs)
	}
}
