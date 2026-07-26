// Package server implements the read-only Cluster Agent HTTP interface
// defined in docs/INTEGRATION.md ("Cluster Agent 工具协议").
package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aiops/cluster-agent/internal/datasource"
	"github.com/aiops/cluster-agent/internal/tools"
)

// maxRequestBody bounds the POST /tools body so a single request cannot force
// unbounded memory allocation during JSON decode (defense-in-depth DoS guard).
const maxRequestBody = 1 << 20 // 1 MiB

// Server wires the tool registry to an http.Handler.
type Server struct {
	clusterID string
	reg       *tools.Registry
	log       *slog.Logger
	mux       *http.ServeMux
	metrics   *metrics
}

// New constructs a Server. clusterID is the default cluster id injected when a
// request omits scope.cluster_id.
func New(clusterID string, reg *tools.Registry, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{clusterID: clusterID, reg: reg, log: log, mux: http.NewServeMux(), metrics: newMetrics()}
	s.routes()
	return s
}

// Handler exposes the router (with access logging) as an http.Handler.
func (s *Server) Handler() http.Handler { return s.logging(s.mux) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /tools", s.handleListTools)
	s.mux.HandleFunc("POST /tools/{tool_name}", s.handleInvoke)
	s.mux.Handle("GET /metrics", s.metrics.handler()) // 架构 §16
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

	// metricTool keeps the Prometheus "tool" label bounded: only registered tool
	// names become label values, everything else collapses to a fixed constant so
	// an anonymous caller cannot explode label cardinality via random tool names.
	metricTool := "unknown"
	if s.reg.Has(name) {
		metricTool = name
	}

	var req toolRequest
	if r.Body != nil {
		// Cap the body so a large payload cannot force unbounded allocation.
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				s.metrics.observe(metricTool, "error", 0)
				writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求体超过大小上限")
				return
			}
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

	start := time.Now()
	res, err := s.reg.Invoke(r.Context(), name, req.Scope, req.Arguments)
	elapsed := time.Since(start).Seconds()
	if err != nil {
		if errors.As(err, &tools.ErrUnknownTool{}) {
			s.metrics.observe(metricTool, "unknown", elapsed)
			writeError(w, http.StatusNotFound, "unknown_tool", err.Error())
			return
		}
		s.metrics.observe(metricTool, "error", elapsed)
		writeError(w, http.StatusInternalServerError, "tool_error", err.Error())
		return
	}
	s.metrics.observe(metricTool, "ok", elapsed)
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
