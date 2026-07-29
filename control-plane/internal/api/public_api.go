package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aiops/control-plane/internal/auth"
	"github.com/aiops/control-plane/internal/httpx"
	"github.com/aiops/control-plane/internal/model"
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
	store       *store.Store
	ingress     *Ingress
	orch        *trigger.Orchestrator
	tempo       Signaler
	authn       *auth.Authenticator
	devUsers    map[string]auth.DevUser
	agentScope  auth.AgentServiceScope
	corsOrigins []string
	// tenant 是进程级租户(Principal 上没有租户,租户由部署决定)。
	tenant      string
	feedbackMet FeedbackMetrics // 可为 nil(降级)
	goldenMet   GoldenMetrics   // 可为 nil(降级)
	log         *slog.Logger
}

// GoldenMetrics 记录评测用例提升(反馈闭环)。
type GoldenMetrics interface {
	IncGoldenCasePromoted()
}

// FeedbackMetrics 记录人工反馈(F10 采纳率)。窄接口。
type FeedbackMetrics interface {
	IncHumanFeedback(action string)
}

// WithFeedbackMetrics 注入反馈指标记录器。分开设置而非加构造参数:
// NewPublicAPI 已有 8 个参数。
func (a *PublicAPI) WithFeedbackMetrics(m FeedbackMetrics) *PublicAPI {
	a.feedbackMet = m
	return a
}

// WithGoldenMetrics 注入评测用例指标记录器。
func (a *PublicAPI) WithGoldenMetrics(m GoldenMetrics) *PublicAPI {
	a.goldenMet = m
	return a
}

// WithTenant 设置进程级租户(用于没有 investigation 上下文的端点,如评测用例审核)。
func (a *PublicAPI) WithTenant(t string) *PublicAPI {
	if t != "" {
		a.tenant = t
	}
	return a
}

func NewPublicAPI(s *store.Store, ingress *Ingress, orch *trigger.Orchestrator, tempo Signaler,
	authn *auth.Authenticator, agentScope auth.AgentServiceScope, corsOrigins []string, log *slog.Logger) *PublicAPI {
	return &PublicAPI{
		store: s, ingress: ingress, orch: orch, tempo: tempo,
		authn: authn, devUsers: auth.DefaultDevUsers(), agentScope: agentScope,
		corsOrigins: corsOrigins, log: log,
	}
}

func (a *PublicAPI) Routes() http.Handler {
	mux := http.NewServeMux()
	// liveness:恒 200,不查依赖。数据库挂了重启进程修不了数据库(见 httpx/health.go)。
	mux.HandleFunc("GET /healthz", httpx.HealthzHandler())
	// readiness:DB 不可用返 503,让 kubelet 把本副本摘出 Service endpoints。
	// 此前两个探针共用 /healthz 且状态码恒 200,断连副本会继续接流量然后每请求 500。
	mux.HandleFunc("GET /readyz", httpx.ReadyzHandler(httpx.HealthChecker{
		Name: "database", Check: a.store.Health, Critical: true,
	}))

	// 认证端点(仅 hs256 开发模式;SECURITY §1)
	mux.HandleFunc("POST /v1/auth/login", a.login)
	mux.HandleFunc("GET /v1/auth/me", a.me)

	mux.HandleFunc("POST /v1/signals", a.ingress.PostSignal) // 自有 webhook 鉴权
	mux.HandleFunc("GET /v1/incidents", a.listIncidents)
	mux.HandleFunc("GET /v1/incidents/{id}", a.getIncident)
	mux.HandleFunc("POST /v1/incidents/{id}/investigations", a.startInvestigation)
	mux.HandleFunc("GET /v1/investigations/{id}", a.getInvestigation)
	mux.HandleFunc("GET /v1/investigations/{id}/events", a.streamEvents)
	mux.HandleFunc("POST /v1/investigations/{id}/cancel", a.cancelInvestigation)
	mux.HandleFunc("POST /v1/investigations/{id}/feedback", a.postFeedback)
	mux.HandleFunc("GET /v1/evidence/{id}", a.getEvidence)
	mux.HandleFunc("GET /v1/knowledge", a.searchKnowledge)
	// 反馈闭环:待审队列与审核(仅 sre/admin,见 auth.ActionReviewGolden)
	mux.HandleFunc("GET /v1/golden-cases", a.listGoldenCases)
	mux.HandleFunc("POST /v1/golden-cases/{id}/review", a.reviewGoldenCase)

	// 认证中间件:跳过 healthz / 登录 / signals(webhook 自有鉴权)
	skip := func(r *http.Request) bool {
		p := r.URL.Path
		return p == "/healthz" || p == "/readyz" || p == "/v1/auth/login" || p == "/v1/signals"
	}
	return httpx.CORS(a.corsOrigins, httpx.Logging(a.log, a.authn.Middleware(skip, mux)))
}

// --- 认证端点 ---

func (a *PublicAPI) login(w http.ResponseWriter, r *http.Request) {
	if a.authn.Mode() != auth.ModeHS256 {
		httpx.Error(w, http.StatusNotFound, "not_found", "login endpoint disabled (use IdP)")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	p, ok := auth.VerifyDevUser(a.devUsers, body.Username, body.Password)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}
	tok, err := a.authn.Issue(p, time.Hour)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "token_error", err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"token": tok, "expires_in": 3600, "user": p})
}

func (a *PublicAPI) me(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "no principal")
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (a *PublicAPI) listIncidents(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if !p.Can(auth.ActionReadIncident) {
		httpx.Error(w, http.StatusForbidden, "forbidden", "missing read permission")
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	incs, err := a.store.ListIncidents(r.Context(), q.Get("status"), q.Get("severity"), limit)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// ABAC:过滤到用户可见的集群/命名空间
	visible := incs[:0]
	for _, inc := range incs {
		ns := ""
		if len(inc.AffectedResources) > 0 {
			ns = inc.AffectedResources[0].Namespace
		}
		if p.InScope(inc.ClusterID, ns) {
			visible = append(visible, inc)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"incidents": visible})
}

// authorizeIncident 校验读权限 + ABAC 范围(用户∩Agent∩Incident),返回是否放行。
func (a *PublicAPI) authorizeIncident(w http.ResponseWriter, r *http.Request, inc model.Incident, action auth.Action) bool {
	p, _ := auth.FromContext(r.Context())
	if !p.Can(action) {
		httpx.Error(w, http.StatusForbidden, "forbidden", "missing permission for action")
		return false
	}
	ns := ""
	if len(inc.AffectedResources) > 0 {
		ns = inc.AffectedResources[0].Namespace
	}
	if !auth.EffectiveAccess(p, a.agentScope, inc.ClusterID, ns) {
		a.store.Audit(r.Context(), inc.TenantID, p.Subject, string(action), "incident", inc.IncidentID, "denied",
			map[string]any{"cluster": inc.ClusterID, "namespace": ns}, map[string]any{"reason": "out_of_scope"})
		httpx.Error(w, http.StatusForbidden, "forbidden", "incident out of your access scope")
		return false
	}
	return true
}

func (a *PublicAPI) getIncident(w http.ResponseWriter, r *http.Request) {
	inc, err := a.store.GetIncident(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}
	if !a.authorizeIncident(w, r, inc, auth.ActionReadIncident) {
		return
	}
	invs, _ := a.store.ListInvestigationsByIncident(r.Context(), inc.IncidentID)
	// 两层模型:alert_groups 是该 incident 下的去重单元明细(哪些资源/规则在告警)
	groups, _ := a.store.ListAlertGroups(r.Context(), inc.IncidentID)
	// 拓扑关联:值班人员据此看到"疑似与 inc-x 同源,它在调用链上游" ——
	// 不合并 incident 而是链接,两边各自保留独立时间线(见 store/incidentrelations.go)。
	relations, _ := a.store.RelationsOf(r.Context(), inc.IncidentID)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"incident": inc, "investigations": invs, "alert_groups": groups,
		"relations": relations,
	})
}

func (a *PublicAPI) startInvestigation(w http.ResponseWriter, r *http.Request) {
	incidentID := r.PathValue("id")
	p, _ := auth.FromContext(r.Context())

	inc, err := a.store.GetIncident(r.Context(), incidentID)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}
	// RBAC + ABAC(用户∩Agent∩Incident)
	if !a.authorizeIncident(w, r, inc, auth.ActionStartInvestig) {
		return
	}

	// Idempotency-Key(SECURITY §5):同 key 返回首次结果,不重复启动
	idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idemKey != "" {
		if resultID, found, e := a.store.GetIdempotentResult(r.Context(), idemKey); e == nil && found {
			if inv, ge := a.store.GetInvestigation(r.Context(), resultID); ge == nil {
				httpx.JSON(w, http.StatusOK, inv)
				return
			}
		}
	}

	// body 可选:容忍空 body,不写错误响应(避免与后续响应重复)
	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // 忽略 EOF/空 body
	}
	reason := body.Reason
	if reason == "" {
		reason = "manual_request"
	}

	inv, err := a.orch.StartInvestigation(r.Context(), incidentID, p.Subject, reason, nil)
	if err != nil {
		if se, ok := err.(*trigger.StopError); ok {
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
	if idemKey != "" {
		_, _ = a.store.PutIdempotentResult(r.Context(), idemKey, "start_investigation", incidentID, inv.InvestigationID)
	}
	httpx.JSON(w, http.StatusAccepted, inv)
}

func (a *PublicAPI) getInvestigation(w http.ResponseWriter, r *http.Request) {
	inv, err := a.store.GetInvestigation(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "investigation not found")
		return
	}
	// fail-closed:无法解析 incident 范围则拒绝(不可 fail-open)
	inc, e := a.store.GetIncident(r.Context(), inv.IncidentID)
	if e != nil {
		httpx.Error(w, http.StatusForbidden, "forbidden", "cannot resolve incident scope")
		return
	}
	if !a.authorizeIncident(w, r, inc, auth.ActionReadIncident) {
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
	// 经证据所属调查的 Incident 做 ABAC(fail-closed)
	inv, e := a.store.GetInvestigation(r.Context(), ev.InvestigationID)
	if e != nil {
		httpx.Error(w, http.StatusForbidden, "forbidden", "cannot resolve evidence scope")
		return
	}
	inc, e2 := a.store.GetIncident(r.Context(), inv.IncidentID)
	if e2 != nil {
		httpx.Error(w, http.StatusForbidden, "forbidden", "cannot resolve incident scope")
		return
	}
	if !a.authorizeIncident(w, r, inc, auth.ActionReadEvidence) {
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
