package gateway

import "testing"

// Gateway 是策略边界:即使 Worker 侧已过滤,这里也必须独立收窄调用方参数。
// 放行的只是"问什么"(expr/query/service),范围类参数一律丢弃。
func TestSanitizeToolArgs(t *testing.T) {
	long := make([]byte, maxToolArgLen+1)
	for i := range long {
		long[i] = 'x'
	}

	cases := []struct {
		name string
		tool string
		in   map[string]any
		want map[string]any
	}{
		{
			name: "允许的表达式放行",
			tool: "query_metrics",
			in:   map[string]any{"expr": "sum(rate(x[5m]))"},
			want: map[string]any{"expr": "sum(rate(x[5m]))"},
		},
		{
			name: "范围类参数被丢弃(scope 由服务端注入)",
			tool: "query_metrics",
			in:   map[string]any{"expr": "up", "namespace": "other", "cluster": "staging"},
			want: map[string]any{"expr": "up"},
		},
		{
			name: "analyzer 标签不是工具参数,不透传给后端",
			tool: "search_logs",
			in:   map[string]any{"analyzer": "logs", "query": `{} |= "err"`},
			want: map[string]any{"query": `{} |= "err"`},
		},
		{
			name: "K8s 工具不接受任何调用方参数",
			tool: "get_workload_state",
			in:   map[string]any{"expr": "up", "namespace": "other"},
			want: map[string]any{},
		},
		{
			name: "未知工具不接受参数",
			tool: "not_a_tool",
			in:   map[string]any{"expr": "up"},
			want: map[string]any{},
		},
		{
			name: "超长值被丢弃",
			tool: "query_metrics",
			in:   map[string]any{"expr": string(long)},
			want: map[string]any{},
		},
		{
			name: "非字符串值被丢弃",
			tool: "query_metrics",
			in:   map[string]any{"expr": 42},
			want: map[string]any{},
		},
		{
			name: "空白值被丢弃(降级为后端默认查询)",
			tool: "query_metrics",
			in:   map[string]any{"expr": "   "},
			want: map[string]any{},
		},
		{
			name: "trace service 放行",
			tool: "get_traces",
			in:   map[string]any{"service": "auth-service"},
			want: map[string]any{"service": "auth-service"},
		},
		{
			name: "nil 参数安全",
			tool: "query_metrics",
			in:   nil,
			want: map[string]any{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeToolArgs(tc.tool, tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("键数量不符: got %v want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("键 %q: got %v want %v", k, got[k], v)
				}
			}
		})
	}
}

// 与 ai-worker TOOL_ARG_KEYS 保持同构:观测三工具可参数化,其余不可。
func TestToolArgKeysCoverOnlyObservabilityTools(t *testing.T) {
	for tool := range toolArgKeys {
		if !observabilityTools[tool] {
			t.Errorf("%s 不是观测类工具,不应接受调用方参数", tool)
		}
	}
	for tool := range observabilityTools {
		if _, ok := toolArgKeys[tool]; !ok {
			t.Errorf("观测类工具 %s 缺少参数白名单(将永远只跑默认查询)", tool)
		}
	}
}
