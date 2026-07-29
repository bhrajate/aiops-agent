// Package api 提供公共 API(:8080)与内部 API(:8090)。
package api

import (
	"net/http"

	"github.com/aiops/control-plane/internal/auth"
	"github.com/aiops/control-plane/internal/gateway"
	"github.com/aiops/control-plane/internal/httpx"
	"github.com/aiops/control-plane/internal/model"
	"github.com/aiops/control-plane/internal/store"
	"log/slog"
)

// InternalAPI 是 AI Worker 唯一的回写入口(保证业务库为单一事实源)。
type InternalAPI struct {
	store        *store.Store
	gateway      *gateway.Gateway
	token        string // 共享密钥(SECURITY §2)
	requireToken bool   // 生产模式:token 为空也拒绝
	metricsHTTP  http.Handler
	log          *slog.Logger
}

func NewInternalAPI(s *store.Store, gw *gateway.Gateway, token string, requireToken bool, metricsHTTP http.Handler, log *slog.Logger) *InternalAPI {
	return &InternalAPI{store: s, gateway: gw, token: token, requireToken: requireToken, metricsHTTP: metricsHTTP, log: log}
}

func (a *InternalAPI) Routes() http.Handler {
	mux := http.NewServeMux()
	// liveness / readiness 语义同公共 API(见 httpx/health.go)。
	mux.HandleFunc("GET /healthz", httpx.HealthzHandler())
	mux.HandleFunc("GET /readyz", httpx.ReadyzHandler(httpx.HealthChecker{
		Name: "database", Check: a.store.Health, Critical: true,
	}))
	// Prometheus 指标(架构第 16 节)。放在 token 校验之外,供采集器抓取。
	if a.metricsHTTP != nil {
		mux.Handle("GET /metrics", a.metricsHTTP)
	}
	mux.HandleFunc("POST /internal/tools/invoke", a.invokeTool)
	mux.HandleFunc("GET /internal/investigations/{id}/context", a.getContext)
	mux.HandleFunc("POST /internal/investigations/{id}/phase", a.setPhase)
	mux.HandleFunc("POST /internal/investigations/{id}/events", a.addEvent)
	mux.HandleFunc("POST /internal/investigations/{id}/hypotheses", a.setHypotheses)
	mux.HandleFunc("POST /internal/investigations/{id}/diagnosis", a.setDiagnosis)
	mux.HandleFunc("POST /internal/investigations/{id}/usage", a.setUsage)
	// 内部 API 共享密钥保护(仅集群内可达)
	return auth.RequireInternalToken(a.token, a.requireToken, httpx.Logging(a.log, mux))
}

func (a *InternalAPI) invokeTool(w http.ResponseWriter, r *http.Request) {
	var req gateway.InvokeRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	res, err := a.gateway.Invoke(r.Context(), req)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "tool_error", err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// getContext 返回调查上下文:incident + signals + topology + changes。
func (a *InternalAPI) getContext(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inv, err := a.store.GetInvestigation(r.Context(), id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "investigation not found")
		return
	}
	inc, err := a.store.GetIncident(r.Context(), inv.IncidentID)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"incident":      inc,
		"investigation": inv,
		"signals":       []any{}, // 首版从 incident 派生,signals 明细可扩展
		"topology":      inc.TopologyRefs,
		"changes":       inc.ChangeRefs,
	})
}

func (a *InternalAPI) setPhase(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Phase string `json:"phase"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	if err := a.store.SetInvestigationPhase(r.Context(), id, body.Phase); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	_, _ = a.store.AppendEvent(r.Context(), id, "phase_changed", map[string]any{"phase": body.Phase})
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *InternalAPI) addEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		EventType string         `json:"event_type"`
		Payload   map[string]any `json:"payload"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	seq, err := a.store.AppendEvent(r.Context(), id, body.EventType, body.Payload)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "ok", "seq": seq})
}

func (a *InternalAPI) setHypotheses(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Hypotheses []model.Hypothesis `json:"hypotheses"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	for i := range body.Hypotheses {
		if body.Hypotheses[i].HypothesisID == "" {
			body.Hypotheses[i].HypothesisID = "hyp-" + randHex(8)
		}
		body.Hypotheses[i].InvestigationID = id
	}
	if err := a.store.ReplaceHypotheses(r.Context(), id, body.Hypotheses); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	_, _ = a.store.AppendEvent(r.Context(), id, "hypothesis_updated",
		map[string]any{"count": len(body.Hypotheses)})
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *InternalAPI) setDiagnosis(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Diagnosis model.DiagnosisResult `json:"diagnosis"`
		Phase     string                `json:"phase"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	// 安全约束:首版默认只读,强制 remediation_proposal 为 null
	body.Diagnosis.RemediationProposal = nil
	phase := body.Phase
	if phase == "" {
		phase = "concluded"
	}
	if err := a.store.SetInvestigationDiagnosis(r.Context(), id, body.Diagnosis, phase); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	_, _ = a.store.AppendEvent(r.Context(), id, "diagnosis_published",
		map[string]any{"status": body.Diagnosis.Status, "hypotheses": len(body.Diagnosis.Hypotheses)})
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *InternalAPI) setUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Usage model.Usage `json:"usage"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	if err := a.store.SetInvestigationUsage(r.Context(), id, body.Usage); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
