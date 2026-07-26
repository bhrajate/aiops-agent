package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/aiops/control-plane/internal/httpx"
	"github.com/aiops/control-plane/internal/store"
	"github.com/aiops/control-plane/internal/trigger"
)

// Signaler 抽象向工作流发信号/取消(便于降级:Temporal 不可用时仍可更新 DB)。
type Signaler interface {
	Signal(ctx context.Context, workflowID, signalName string, payload any) error
	Cancel(ctx context.Context, workflowID string) error
}

// PublicAPI 面向前端与外部 webhook。
type PublicAPI struct {
	store   *store.Store
	ingress *Ingress
	orch    *trigger.Orchestrator
	tempo   Signaler
	log     *slog.Logger
}

func NewPublicAPI(s *store.Store, ingress *Ingress, orch *trigger.Orchestrator, tempo Signaler, log *slog.Logger) *PublicAPI {
	return &PublicAPI{store: s, ingress: ingress, orch: orch, tempo: tempo, log: log}
}

func (a *PublicAPI) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		st := "ok"
		if err := a.store.Health(r.Context()); err != nil {
			st = "degraded"
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": st})
	})

	mux.HandleFunc("POST /v1/signals", a.ingress.PostSignal)
	mux.HandleFunc("GET /v1/incidents", a.listIncidents)
	mux.HandleFunc("GET /v1/incidents/{id}", a.getIncident)
	mux.HandleFunc("POST /v1/incidents/{id}/investigations", a.startInvestigation)
	mux.HandleFunc("GET /v1/investigations/{id}", a.getInvestigation)
	mux.HandleFunc("GET /v1/investigations/{id}/events", a.streamEvents)
	mux.HandleFunc("POST /v1/investigations/{id}/cancel", a.cancelInvestigation)
	mux.HandleFunc("POST /v1/investigations/{id}/feedback", a.postFeedback)
	mux.HandleFunc("GET /v1/evidence/{id}", a.getEvidence)
	mux.HandleFunc("GET /v1/knowledge", a.searchKnowledge)

	return httpx.CORS(httpx.Logging(a.log, mux))
}

func (a *PublicAPI) listIncidents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	incs, err := a.store.ListIncidents(r.Context(), q.Get("status"), q.Get("severity"), limit)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"incidents": incs})
}

func (a *PublicAPI) getIncident(w http.ResponseWriter, r *http.Request) {
	inc, err := a.store.GetIncident(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}
	invs, _ := a.store.ListInvestigationsByIncident(r.Context(), inc.IncidentID)
	httpx.JSON(w, http.StatusOK, map[string]any{"incident": inc, "investigations": invs})
}

func (a *PublicAPI) startInvestigation(w http.ResponseWriter, r *http.Request) {
	incidentID := r.PathValue("id")
	// Idempotency-Key(文档 17.2):同 key 重复请求不重复启动
	idemKey := r.Header.Get("Idempotency-Key")
	triggeredBy := userOrDefault(r)

	var body struct {
		Reason string `json:"reason"`
	}
	_ = httpx.Decode(w, r, &body) // body 可选

	reason := body.Reason
	if reason == "" {
		reason = "manual_request"
	}
	// 简易幂等:若已存在活跃调查,直接返回它
	inv, err := a.orch.StartInvestigation(r.Context(), incidentID, triggeredBy, reason, nil)
	if err != nil {
		if se, ok := err.(*trigger.StopError); ok {
			// 已有同版本活跃调查:返回现有的(幂等语义)
			invs, _ := a.store.ListInvestigationsByIncident(r.Context(), incidentID)
			for _, iv := range invs {
				if iv.Phase != "closed" && iv.Phase != "cancelled" {
					httpx.JSON(w, http.StatusOK, iv)
					return
				}
			}
			httpx.Error(w, http.StatusConflict, "stopped", se.Reason)
			return
		}
		httpx.Error(w, http.StatusBadRequest, "start_failed", err.Error())
		return
	}
	_ = idemKey
	httpx.JSON(w, http.StatusAccepted, inv)
}

func (a *PublicAPI) getInvestigation(w http.ResponseWriter, r *http.Request) {
	inv, err := a.store.GetInvestigation(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "investigation not found")
		return
	}
	hyps, _ := a.store.ListHypotheses(r.Context(), inv.InvestigationID)
	evs, _ := a.store.ListEvidenceByInvestigation(r.Context(), inv.InvestigationID)
	fbs, _ := a.store.ListFeedback(r.Context(), inv.InvestigationID)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"investigation": inv,
		"hypotheses":    hyps,
		"evidence":      evs,
		"feedback":      fbs,
	})
}

func (a *PublicAPI) getEvidence(w http.ResponseWriter, r *http.Request) {
	ev, err := a.store.GetEvidence(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "evidence not found")
		return
	}
	httpx.JSON(w, http.StatusOK, ev)
}

func (a *PublicAPI) searchKnowledge(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.SearchKnowledge(r.Context(), r.URL.Query().Get("q"), 10)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func userOrDefault(r *http.Request) string {
	// 首版:从 header 取用户名(生产接 OIDC)。缺省 system。
	if u := r.Header.Get("X-User"); u != "" {
		return u
	}
	return "operator"
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(i)
		}
	}
	return hex.EncodeToString(b)
}

func hashStr(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

var _ = time.Now
