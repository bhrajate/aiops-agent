package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/aiops/control-plane/internal/httpx"
	"github.com/aiops/control-plane/internal/model"
	"github.com/aiops/control-plane/internal/store"
	"github.com/aiops/control-plane/internal/webhookauth"
)

// Ingress 是 Signal Ingress(文档 6.1):鉴权、快速 2xx、标准化、持久化+outbox。
// Webhook 是信号入口,不是 RCA 触发器 —— 这里绝不等待模型或调查。
// SignalMetrics nil-safe 信号计数。
type SignalMetrics interface {
	IncSignal(source string)
	IncIngressThrottled(tenant string)
}

type Ingress struct {
	store         *store.Store
	clusterID     string
	tenant        string
	webhookSecret string
	metrics       SignalMetrics
	limiter       httpx.RateLimiter // 可为 nil(不限流)
	log           *slog.Logger
}

func NewIngress(s *store.Store, clusterID, tenant, webhookSecret string, metrics SignalMetrics, limiter httpx.RateLimiter, log *slog.Logger) *Ingress {
	return &Ingress{
		store: s, clusterID: clusterID, tenant: tenant, webhookSecret: webhookSecret,
		metrics: metrics, limiter: limiter, log: log,
	}
}

// PostSignal 接收信号。支持两种载荷:
//  1. 原生 Signal(带 signal_type 字段);
//  2. Alertmanager webhook(带 alerts 数组)。
func (i *Ingress) PostSignal(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "cannot read body")
		return
	}

	// Webhook HMAC 签名校验(SECURITY §4)
	ok, checked := webhookauth.Verify(i.webhookSecret, r.Header.Get("X-AIOPS-Signature"), rawBody)
	if !ok {
		i.store.Audit(r.Context(), i.tenant, "ingress", "signal_ingest", "signal", "", "denied",
			map[string]any{"cluster": i.clusterID}, map[string]any{"reason": "bad_webhook_signature"})
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "invalid webhook signature")
		return
	}
	if !checked {
		i.log.Warn("webhook signature not verified (no secret configured) — set AIOPS_WEBHOOK_SECRET for production")
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &raw); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "invalid json")
		return
	}

	var signals []model.Signal
	if _, ok := raw["alerts"]; ok {
		signals = i.fromAlertmanager(raw)
	} else {
		signals = i.fromNative(raw)
	}
	if len(signals) == 0 {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "no signals parsed")
		return
	}

	// 限流:按**信号条数**计费而不是请求数——一个 Alertmanager webhook 可以带
	// 几百条告警,按请求计费挡不住告警风暴。在写库之前判定,保护 DB 与 outbox。
	// 按租户分桶,一个租户的风暴不影响其他租户。
	if i.limiter != nil {
		tenant := signals[0].TenantID
		if tenant == "" {
			tenant = i.tenant
		}
		if ok, retry := i.limiter.Allow(tenant, len(signals)); !ok {
			if i.metrics != nil {
				i.metrics.IncIngressThrottled(tenant)
			}
			i.store.Audit(r.Context(), tenant, "ingress", "signal_ingest", "signal", "", "denied",
				map[string]any{"cluster": i.clusterID},
				map[string]any{"reason": "rate_limited", "signals": len(signals)})
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			httpx.Error(w, http.StatusTooManyRequests, "rate_limited",
				"signal ingest rate limit exceeded")
			return
		}
	}

	accepted := 0
	for _, sig := range signals {
		if err := i.store.InsertSignalWithOutbox(r.Context(), sig); err != nil {
			i.log.Warn("persist signal failed", "signal_id", sig.SignalID, "err", err)
			continue
		}
		if i.metrics != nil {
			i.metrics.IncSignal(sig.Source)
		}
		accepted++
	}
	i.store.Audit(r.Context(), i.tenant, "ingress", "signal_ingest", "signal", "", "ok",
		map[string]any{"cluster": i.clusterID}, map[string]any{"accepted": accepted, "total": len(signals)})

	// 快速返回,不等待后续 Incident/RCA
	httpx.JSON(w, http.StatusAccepted, map[string]any{"accepted": accepted, "total": len(signals)})
}

func (i *Ingress) fromNative(raw map[string]json.RawMessage) []model.Signal {
	var sig model.Signal
	b, _ := json.Marshal(rawToMap(raw))
	if err := json.Unmarshal(b, &sig); err != nil {
		return nil
	}
	i.fill(&sig, b)
	if sig.SignalType == "" {
		return nil
	}
	return []model.Signal{sig}
}

// Alertmanager webhook 格式解析。
func (i *Ingress) fromAlertmanager(raw map[string]json.RawMessage) []model.Signal {
	var payload struct {
		Alerts []struct {
			Status      string            `json:"status"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			StartsAt    time.Time         `json:"startsAt"`
			EndsAt      time.Time         `json:"endsAt"`
			Fingerprint string            `json:"fingerprint"`
		} `json:"alerts"`
	}
	b, _ := json.Marshal(rawToMap(raw))
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil
	}
	var out []model.Signal
	for _, al := range payload.Alerts {
		sigType := "alert"
		if al.Status == "resolved" {
			sigType = "resolved"
		}
		cluster := al.Labels["cluster"]
		if cluster == "" {
			cluster = i.clusterID
		}
		sig := model.Signal{
			Source:     "alertmanager",
			SignalType: sigType,
			ClusterID:  cluster,
			Severity:   al.Labels["severity"],
			ResourceRef: resourceFromAlertLabels(al.Labels),
			Labels: al.Labels,
		}
		if !al.StartsAt.IsZero() {
			sig.StartsAt = &al.StartsAt
		}
		if !al.EndsAt.IsZero() && al.Status == "resolved" {
			sig.EndsAt = &al.EndsAt
		}
		payloadBytes, _ := json.Marshal(al)
		i.fill(&sig, payloadBytes)
		out = append(out, sig)
	}
	return out
}

// resourceFromAlertLabels 从 Alertmanager 标签推导资源引用。
//
// Kind 必须与 Name 的来源一致:旧实现把 Kind 固定为 "Deployment",但 Name 可能取自
// `pod` 标签,于是一个 Pod 被标成 Deployment —— 下游 model.ServiceKey 因此不会把
// Pod 名归约到工作负载,同一服务的多个 Pod 会被算成多个服务,虚高 blast_radius。
//
// 优先取服务级标签(deployment/statefulset/…),取不到才退到 pod;
// 显式 `kind` 标签优先级最高(上游已明确告知类型)。
func resourceFromAlertLabels(l map[string]string) model.ResourceRef {
	ref := model.ResourceRef{Namespace: l["namespace"]}
	// 服务级优先,最后才是 pod —— 顺序即优先级。
	for _, c := range []struct{ label, kind string }{
		{"deployment", "Deployment"},
		{"statefulset", "StatefulSet"},
		{"daemonset", "DaemonSet"},
		{"job", "Job"},
		{"service", "Service"},
		{"pod", "Pod"},
		{"node", "Node"},
	} {
		if v := l[c.label]; v != "" {
			ref.Name, ref.Kind = v, c.kind
			break
		}
	}
	if k := l["kind"]; k != "" {
		ref.Kind = k // 上游显式声明的类型优先
	}
	if ref.Kind == "" {
		ref.Kind = "Deployment" // 无任何线索时的历史缺省
	}
	return ref
}

// fill 填充缺省字段:signal_id、tenant、cluster、labels、payload_hash、received_at。
func (i *Ingress) fill(sig *model.Signal, payload []byte) {
	if sig.TenantID == "" {
		sig.TenantID = i.tenant
	}
	if sig.ClusterID == "" {
		sig.ClusterID = i.clusterID
	}
	if sig.Labels == nil {
		sig.Labels = map[string]string{}
	}
	sig.ReceivedAt = time.Now().UTC()
	h := sha256.Sum256(payload)
	sig.PayloadHash = "sha256:" + hex.EncodeToString(h[:])
	if sig.SignalID == "" {
		// 幂等:同一 payload 短时间内重复投递生成相同前缀 + 时间片
		sig.SignalID = "sig-" + hex.EncodeToString(h[:8]) + "-" + randHex(4)
	}
}

func rawToMap(raw map[string]json.RawMessage) map[string]any {
	m := make(map[string]any, len(raw))
	for k, v := range raw {
		var val any
		_ = json.Unmarshal(v, &val)
		m[k] = val
	}
	return m
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

var _ = hashStr
