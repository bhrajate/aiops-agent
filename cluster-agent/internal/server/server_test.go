package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiops/cluster-agent/internal/datasource"
	"github.com/aiops/cluster-agent/internal/tools"
)

func newTestServer() http.Handler {
	reg := tools.NewRegistry(datasource.NewMock())
	return New("prod-cn-1", reg, nil).Handler()
}

func TestHealthz(t *testing.T) {
	rr := httptest.NewRecorder()
	newTestServer().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body map[string]string
	mustJSON(t, rr.Body, &body)
	if body["status"] != "ok" {
		t.Errorf("status body = %v", body)
	}
}

func TestListTools(t *testing.T) {
	rr := httptest.NewRecorder()
	newTestServer().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/tools", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Schema      map[string]any `json:"input_schema"`
		} `json:"tools"`
	}
	mustJSON(t, rr.Body, &body)
	want := []string{
		"get_workload_state", "get_kubernetes_events",
		"list_recent_changes", "inspect_dependencies",
	}
	if len(body.Tools) != len(want) {
		t.Fatalf("expected %d tools, got %d", len(want), len(body.Tools))
	}
	for i, name := range want {
		if body.Tools[i].Name != name {
			t.Errorf("tool[%d] = %s, want %s", i, body.Tools[i].Name, name)
		}
		if body.Tools[i].Description == "" || body.Tools[i].Schema == nil {
			t.Errorf("tool %s missing description/schema", name)
		}
	}
}

func TestInvokeToolSuccess(t *testing.T) {
	reqBody := `{"arguments":{},"scope":{"cluster_id":"prod-cn-1","namespace":"payment","resource":{"name":"checkout"}}}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tools/get_workload_state", strings.NewReader(reqBody))
	newTestServer().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var res datasource.Result
	mustJSON(t, rr.Body, &res)
	if res.Source != "kubernetes" || res.Summary == "" || res.Raw == nil {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestInvokeScopeInjection(t *testing.T) {
	// 故意不带 cluster_id:服务端必须注入自己配置的默认值。
	reqBody := `{"scope":{"namespace":"payment","resource":{"name":"checkout"}}}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tools/get_workload_state", strings.NewReader(reqBody))
	newTestServer().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var res datasource.Result
	mustJSON(t, rr.Body, &res)
	raw := res.Raw.(map[string]any)
	if raw["cluster_id"] != "prod-cn-1" {
		t.Errorf("default cluster_id not injected: %v", raw["cluster_id"])
	}
}

func TestInvokeUnknownTool(t *testing.T) {
	reqBody := `{"scope":{"namespace":"payment"}}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tools/does_not_exist", strings.NewReader(reqBody))
	newTestServer().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
	var body map[string]any
	mustJSON(t, rr.Body, &body)
	if _, ok := body["error"]; !ok {
		t.Errorf("expected error body, got %v", body)
	}
}

func TestInvokeMissingNamespace(t *testing.T) {
	reqBody := `{"scope":{"cluster_id":"prod-cn-1"}}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tools/get_kubernetes_events", strings.NewReader(reqBody))
	newTestServer().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestInvokeBadJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tools/get_workload_state", bytes.NewReader([]byte(`{not json`)))
	newTestServer().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// TestMetricsUnknownToolLabel 验证未注册的工具名绝不会变成 Prometheus 标签值
// (高基数 DoS 守卫):/metrics 抓取结果里必须是 tool="unknown",而不是攻击者
// 传入的名字。
func TestMetricsUnknownToolLabel(t *testing.T) {
	reg := tools.NewRegistry(datasource.NewMock())
	srv := New("prod-cn-1", reg, nil)
	h := srv.Handler()

	attacker := "pwn-9f8a7b6c5d"
	body := `{"scope":{"namespace":"payment"}}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tools/"+attacker, strings.NewReader(body)))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown tool, got %d", rr.Code)
	}

	mrr := httptest.NewRecorder()
	h.ServeHTTP(mrr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	scrape := mrr.Body.String()
	if strings.Contains(scrape, attacker) {
		t.Errorf("attacker tool name leaked into metrics labels:\n%s", scrape)
	}
	if !strings.Contains(scrape, `tool="unknown"`) {
		t.Errorf("expected tool=\"unknown\" label in metrics, got:\n%s", scrape)
	}
}

// TestInvokeBodyTooLarge 验证请求体大小受到限制。
func TestInvokeBodyTooLarge(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"arguments":{"expr":"`)
	for i := 0; i < (2 << 20); i++ { // 约 2 MiB,超过 1 MiB 上限
		b.WriteByte('a')
	}
	b.WriteString(`"},"scope":{"namespace":"payment"}}`)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tools/get_workload_state", strings.NewReader(b.String()))
	newTestServer().ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func mustJSON(t *testing.T, r io.Reader, v any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
