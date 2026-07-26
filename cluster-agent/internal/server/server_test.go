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
		"get_workload_state", "get_kubernetes_events", "query_metrics", "search_logs",
		"get_traces", "list_recent_changes", "inspect_dependencies",
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
	req := httptest.NewRequest(http.MethodPost, "/tools/query_metrics", strings.NewReader(reqBody))
	newTestServer().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var res datasource.Result
	mustJSON(t, rr.Body, &res)
	if res.Source != "prometheus" || res.Summary == "" || res.Raw == nil {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestInvokeScopeInjection(t *testing.T) {
	// Omit cluster_id: the server must inject its configured default.
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
	req := httptest.NewRequest(http.MethodPost, "/tools/get_traces", strings.NewReader(reqBody))
	newTestServer().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestInvokeBadJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tools/query_metrics", bytes.NewReader([]byte(`{not json`)))
	newTestServer().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func mustJSON(t *testing.T, r io.Reader, v any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
