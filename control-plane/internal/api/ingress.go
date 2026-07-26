package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/aiops/control-plane/internal/httpx"
	"github.com/aiops/control-plane/internal/model"
	"github.com/aiops/control-plane/internal/store"
	"github.com/aiops/control-plane/internal/webhookauth"
)

// Ingress 是 Signal Ingress(文档 6.1):鉴权、快速 2xx、标准化、持久化+outbox。
// Webhook 是信号入口,不是 RCA 触发器 —— 这里绝不等待模型或调查。
type Ingress struct {
	store         *store.Store
	clusterID     string
	tenant        string
	webhookSecret string
	log           *slog.Logger
}

func NewIngress(s *store.Store, clusterID, tenant, webhookSecret string, log *slog.Logger) *Ingress {
	return &Ingress{store: s, clusterID: clusterID, tenant: tenant, webhookSecret: webhookSecret, log: log}
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

	accepted := 0
	for _, sig := range signals {
		if err := i.store.InsertSignalWithOutbox(r.Context(), sig); err != nil {
			i.log.Warn("persist signal failed", "signal_id", sig.SignalID, "err", err)
			continue
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
			ResourceRef: model.ResourceRef{
				Namespace: al.Labels["namespace"],
				Kind:      firstNonEmpty(al.Labels["kind"], "Deployment"),
				Name:      firstNonEmpty(al.Labels["deployment"], al.Labels["pod"], al.Labels["service"], al.Labels["job"]),
			},
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
