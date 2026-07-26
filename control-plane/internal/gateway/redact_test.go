package gateway

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct {
		in           string
		mustNotHave  string
		wantRedacted bool
	}{
		{"authorization: Bearer abc123secret", "abc123secret", true},
		{"password=hunter2 in config", "hunter2", true},
		{"token: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig", "payload.sig", true},
		{"contact ops@example.com for help", "ops@example.com", true},
		{"pod ip 10.1.2.3 restarted", "10.1.2.3", true},
		{"checkout 5xx 错误率升高", "", false}, // 正常内容不动
	}
	for _, c := range cases {
		out, red := Redact(c.in)
		if c.mustNotHave != "" && strings.Contains(out, c.mustNotHave) {
			t.Errorf("Redact(%q) 未脱敏敏感值 %q, got %q", c.in, c.mustNotHave, out)
		}
		if red != c.wantRedacted {
			t.Errorf("Redact(%q) redacted=%v want %v", c.in, red, c.wantRedacted)
		}
	}
}

func TestRedactAllowedToolsCoverage(t *testing.T) {
	// 保证首版 8 个工具都在白名单
	want := []string{
		"get_workload_state", "get_kubernetes_events", "query_metrics", "search_logs",
		"get_traces", "list_recent_changes", "inspect_dependencies", "retrieve_runbook",
	}
	for _, tool := range want {
		if !allowedTools[tool] {
			t.Errorf("工具 %q 应在白名单内", tool)
		}
	}
	if allowedTools["kubectl_exec"] {
		t.Error("写操作类工具绝不能在白名单")
	}
}
