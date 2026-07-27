package obsquery

import (
	"context"
	"strings"
	"testing"
)

func mockScope() Scope {
	return Scope{ClusterID: "prod-cn-1", Namespace: "payment",
		Resource: ResourceRef{Kind: "Deployment", Name: "checkout"}}
}

func TestMockIsDeterministic(t *testing.T) {
	m := NewMock()
	ctx := context.Background()
	a, _ := m.QueryMetrics(ctx, mockScope(), nil)
	b, _ := m.QueryMetrics(ctx, mockScope(), nil)
	if a.Summary != b.Summary {
		t.Error("mock 指标查询应确定性(两次结果一致)")
	}
	l1, _ := m.SearchLogs(ctx, mockScope(), nil)
	l2, _ := m.SearchLogs(ctx, mockScope(), nil)
	if l1.Summary != l2.Summary {
		t.Error("mock 日志查询应确定性")
	}
	t1, _ := m.GetTraces(ctx, mockScope(), nil)
	t2, _ := m.GetTraces(ctx, mockScope(), nil)
	if t1.Summary != t2.Summary {
		t.Error("mock 链路查询应确定性")
	}
}

func TestMockTellsCoherentStory(t *testing.T) {
	m := NewMock()
	ctx := context.Background()
	met, _ := m.QueryMetrics(ctx, mockScope(), nil)
	logs, _ := m.SearchLogs(ctx, mockScope(), nil)
	tr, _ := m.GetTraces(ctx, mockScope(), nil)
	// 同一剧本:版本号与下游依赖需在三类证据间自洽
	if !strings.Contains(met.Summary, "v2.3.0") || !strings.Contains(logs.Summary, "v2.3.0") {
		t.Error("指标与日志应引用同一新版本号")
	}
	if !strings.Contains(logs.Summary, "auth-service") || !strings.Contains(tr.Summary, "auth-service") {
		t.Error("日志与链路应引用同一下游依赖")
	}
	for _, r := range []Result{met, logs, tr} {
		if !strings.HasSuffix(r.Source, "/mock") {
			t.Errorf("mock 结果 source 应带 /mock 后缀,便于识别非真实证据: %q", r.Source)
		}
	}
}

func TestMockScenarioVariesByNamespace(t *testing.T) {
	m := NewMock()
	ctx := context.Background()
	pay, _ := m.SearchLogs(ctx, Scope{ClusterID: "c", Namespace: "payment"}, nil)
	inv, _ := m.SearchLogs(ctx, Scope{ClusterID: "c", Namespace: "inventory"}, nil)
	if pay.Summary == inv.Summary {
		t.Error("不同 namespace 应给出不同故障剧本")
	}
}

func TestFromEnvFallsBackToMock(t *testing.T) {
	// 未配置任何后端 → mock(保住零基础设施演示路径)
	t.Setenv("AIOPS_OBS_DATASOURCE", "")
	t.Setenv("AIOPS_PROM_URL", "")
	t.Setenv("AIOPS_LOKI_URL", "")
	t.Setenv("AIOPS_TEMPO_URL", "")
	q, mode := FromEnv()
	if mode != "mock" {
		t.Errorf("mode = %q, want mock", mode)
	}
	if _, ok := q.(*Mock); !ok {
		t.Errorf("应回退到 *Mock, got %T", q)
	}
}

func TestFromEnvUsesLiveWhenConfigured(t *testing.T) {
	t.Setenv("AIOPS_OBS_DATASOURCE", "")
	t.Setenv("AIOPS_PROM_URL", "http://prom:9090")
	q, mode := FromEnv()
	if mode != "live" {
		t.Errorf("mode = %q, want live", mode)
	}
	if _, ok := q.(*Client); !ok {
		t.Errorf("应使用 *Client, got %T", q)
	}
}

func TestFromEnvMockForced(t *testing.T) {
	// 显式 mock 优先于已配置的后端
	t.Setenv("AIOPS_OBS_DATASOURCE", "mock")
	t.Setenv("AIOPS_PROM_URL", "http://prom:9090")
	_, mode := FromEnv()
	if mode != "mock" {
		t.Errorf("显式 mock 应生效, got %q", mode)
	}
}
