// Package server implements the read-only Cluster Agent HTTP interface
// defined in docs/INTEGRATION.md ("Cluster Agent 工具协议").
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aiops/cluster-agent/internal/datasource"
	"github.com/aiops/cluster-agent/internal/tools"
)

// Server wires the tool registry to an http.Handler.
type Server struct {
	clusterID string
	reg       *tools.Registry
	log       *slog.Logger
	mux       *http.ServeMux
}

// New constructs a Server. clusterID is the default cluster id injected when a
// request omits scope.cluster_id.
func New(clusterID string, reg *tools.Registry, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{clusterID: clusterID, reg: reg, log: log, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler exposes the router (with access logging) as an http.Handler.
func (s *Server) Handler() http.Handler { return s.logging(s.mux) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /tools", s.handleListTools)
	s.mux.HandleFunc("POST /tools/{tool_name}", s.handleInvoke)
}

// toolRequest is the POST /tools/{tool_name} body.
type toolRequest struct {
	Arguments map[string]any   `json:"arguments"`
	Scope     datasource.Scope `json:"scope"`
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListTools(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": s.reg.List()})
}

func (s *Server) handleInvoke(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("tool_name")

	var req toolRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && err.Error() != "EOF" {
			writeError(w, http.StatusBadRequest, "invalid_request", "无法解析请求体: "+err.Error())
			return
		}
	}

	// Scope injection: the Tool Gateway is the source of truth for cluster_id.
	// Fall back to the agent's configured cluster when omitted.
	if strings.TrimSpace(req.Scope.ClusterID) == "" {
		req.Scope.ClusterID = s.clusterID
	}
	if strings.TrimSpace(req.Scope.Namespace) == "" {
		writeError(w, http.StatusBadRequest, "invalid_scope", "scope.namespace 不能为空")
		return
	}

	res, err := s.reg.Invoke(r.Context(), name, req.Scope, req.Arguments)
	if err != nil {
		if _, ok := err.(tools.ErrUnknownTool); ok {
			writeError(w, http.StatusNotFound, "unknown_tool", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "tool_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]string{"code": errCode, "message": msg},
	})
}
