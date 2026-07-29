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

	accepted, duplicate := 0, 0
	for _, sig := range signals {
		inserted, err := i.store.InsertSignalWithOutbox(r.Context(), sig)
		if err != nil {
			i.log.Warn("persist signal failed", "signal_id", sig.SignalID, "err", err)
			continue
		}
		if !inserted {
			// 重复投递(Alertmanager 至少一次投递,这是预期行为而非错误)。
			// 不计入 signals_ingested:否则计数器会随重投递虚增,
			// 与它要度量的"进来了多少信号"不符 —— 与 F5 修的是同一类问题。
			duplicate++
			continue
		}
		if i.metrics != nil {
			i.metrics.IncSignal(sig.Source)
		}
		accepted++
	}
	i.store.Audit(r.Context(), i.tenant, "ingress", "signal_ingest", "signal", "", "ok",
		map[string]any{"cluster": i.clusterID},
		map[string]any{"accepted": accepted, "duplicate": duplicate, "total": len(signals)})

	// 快速返回,不等待后续 Incident/RCA。
	// 重复投递仍返回 202:调用方重投的目的已经达成(信号已在系统里),
	// 报错会让 Alertmanager 继续重试同一条。duplicate 单独回报便于排查投递配置。
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"accepted": accepted, "duplicate": duplicate, "total": len(signals)})
}

func (i *Ingress) fromNative(raw map[string]json.RawMessage) []model.Signal {
	var sig model.Signal
	b, _ := json.Marshal(rawToMap(raw))
	if err := json.Unmarshal(b, &sig); err != nil {
		return nil
	}
	// 原生格式:调用方可显式给 signal_id(那时 fill 不覆盖)。没给则用
	// signal_type + starts_at 参与身份,让同一资源的 firing/resolved 区分开;
	// 无 fingerprint 时基础是 payload 哈希(见 signalid.go)。
	ident := model.SignalIdentity{Status: sig.SignalType}
	if sig.StartsAt != nil {
		ident.StartsAt = *sig.StartsAt
	}
	i.fill(&sig, b, ident)
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
			Source:      "alertmanager",
			SignalType:  sigType,
			ClusterID:   cluster,
			Severity:    al.Labels["severity"],
			ResourceRef: resourceFromAlertLabels(al.Labels),
			Labels:      al.Labels,
		}
		if !al.StartsAt.IsZero() {
			sig.StartsAt = &al.StartsAt
		}
		if !al.EndsAt.IsZero() && al.Status == "resolved" {
			sig.EndsAt = &al.EndsAt
		}
		payloadBytes, _ := json.Marshal(al)
		// fingerprint 此前被解析出来却直接丢弃 —— 它正是 Alertmanager 提供的
		// 稳定身份。status/startsAt 一并带上以区分 firing/resolved 与不同故障轮次。
		i.fill(&sig, payloadBytes, model.SignalIdentity{
			Fingerprint: al.Fingerprint,
			Status:      al.Status,
			StartsAt:    al.StartsAt,
		})
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
//
// ident 提供推导 signal_id 的稳定身份(见 signalid.go)。零值 ident 表示
// 调用方没有更好的身份来源,此时只用 payload 哈希。
func (i *Ingress) fill(sig *model.Signal, payload []byte, ident model.SignalIdentity) {
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
		// 幂等:**不含随机成分**。旧实现附加 randHex(4),使每次重投递都得到
		// 新 ID,ON CONFLICT 永不冲突 —— 重复行虚增 signal_count(F5)。
		ident.PayloadHash = sig.PayloadHash
		sig.SignalID = model.DeriveSignalID(ident)
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
