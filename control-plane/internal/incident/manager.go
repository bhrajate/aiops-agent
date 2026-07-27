// Package incident 实现 Incident Manager:消费 signals,归一化、去重聚合为 Incident。
package incident

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aiops/control-plane/internal/model"
	"github.com/aiops/control-plane/internal/store"
)

type Manager struct {
	store                *store.Store
	correlationWindowSec int
	log                  *slog.Logger
}

func New(s *store.Store, correlationWindowSec int, log *slog.Logger) *Manager {
	if correlationWindowSec <= 0 {
		correlationWindowSec = 900 // 默认 15 分钟相关窗口
	}
	return &Manager{store: s, correlationWindowSec: correlationWindowSec, log: log}
}

// HandleSignal 处理一条 signals 消息(bus.Handler 签名)。幂等。
func (m *Manager) HandleSignal(ctx context.Context, _ []byte, value []byte) error {
	var sig model.Signal
	if err := json.Unmarshal(value, &sig); err != nil {
		m.log.Warn("bad signal payload", "err", err)
		return nil // 脏消息直接丢弃,不阻塞消费
	}

	// resolved 信号:尝试解决对应 incident
	groupingKey := GroupingKey(sig)
	if sig.SignalType == "resolved" {
		return m.handleResolved(ctx, sig, groupingKey)
	}

	// 两层聚合(优化②):先入去重单元 alert_group(按 grouping_key,含 resource),
	// 再按 correlation_key(tenant/cluster/namespace)find-or-create 相关性单元 incident,
	// 最后由该 incident 下所有活跃 group 重算 affected_resources / blast_radius /
	// severity / signal_count —— 影响面扩大天然可见,无需事后修补。
	tenant := orDefault(sig.TenantID)
	ts := nowOr(sig.StartsAt)
	agg, created, err := m.store.UpsertAlertGroupAndCorrelate(ctx, store.AlertGroupInput{
		GroupID:       "grp-" + randHex(10),
		TenantID:      tenant,
		ClusterID:     sig.ClusterID,
		GroupingKey:   groupingKey,
		Namespace:     sig.ResourceRef.Namespace,
		ResourceRef:   sig.ResourceRef,
		Severity:      NormalizeSeverity(sig.Severity),
		FaultCategory: ClassifyFault(sig),
		Title:         buildTitle(sig),
	}, ts, ts)
	if err != nil {
		return fmt.Errorf("upsert alert group / correlate incident: %w", err)
	}
	if err := m.store.AttachSignalToIncident(ctx, sig.SignalID, agg.IncidentID); err != nil {
		m.log.Warn("attach signal failed", "err", err)
	}
	if svc, _ := agg.BlastRadius["services"].(float64); svc > 1 {
		m.log.Info("blast radius expanded", "incident_id", agg.IncidentID,
			"services", agg.BlastRadius["services"], "namespaces", agg.BlastRadius["namespaces"])
	}
	m.store.Audit(ctx, agg.TenantID, "system", "incident_upsert", "incident", agg.IncidentID, "ok",
		map[string]any{"cluster": agg.ClusterID}, map[string]any{"created": created, "version": agg.Version})
	if created {
		m.log.Info("incident created", "incident_id", agg.IncidentID, "severity", agg.Severity, "category", agg.FaultCategory)
	} else {
		m.log.Info("incident updated", "incident_id", agg.IncidentID, "version", agg.Version)
	}
	return nil
}

// handleResolved 解决的是**去重单元**(alert_group),而非整个 incident:
// 只有当 incident 下再无活跃 group 时,incident 才随之 resolved。
// 这修正了旧行为——一条 resolved 信号会误关闭包含多个资源故障的整个 incident。
func (m *Manager) handleResolved(ctx context.Context, sig model.Signal, groupingKey string) error {
	incidentID, incidentResolved, err := m.store.ResolveAlertGroup(ctx, groupingKey, orDefault(sig.TenantID))
	if err != nil {
		return nil // 无此 group,忽略
	}
	if incidentResolved {
		m.log.Info("incident resolved (all alert groups resolved)", "incident_id", incidentID)
	} else if incidentID != "" {
		m.log.Info("alert group resolved (incident still active)", "incident_id", incidentID)
	}
	return nil
}

// ---- 归一化 / 分类 逻辑(确定性)----

// GroupingKey 幂等/聚合键(文档 6.2):
// tenant / cluster / namespace / resource_uid(或 name) / signal_type / rule_id
func GroupingKey(s model.Signal) string {
	res := s.ResourceRef.UID
	if res == "" {
		res = s.ResourceRef.Namespace + "/" + s.ResourceRef.Kind + "/" + s.ResourceRef.Name
	}
	ruleID := s.Labels["rule_id"]
	if ruleID == "" {
		ruleID = s.Labels["alertname"]
	}
	// change/event 类信号不带 rule,用 source 归组以便与告警关联到同一资源
	raw := strings.Join([]string{
		orDefault(s.TenantID), s.ClusterID, s.ResourceRef.Namespace, res,
		normalizeSignalTypeForGrouping(s.SignalType), ruleID,
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return "grp-" + hex.EncodeToString(sum[:])[:20]
}

// change / event / alert 归到同一资源的同一组,便于变更与告警关联。
func normalizeSignalTypeForGrouping(t string) string {
	switch t {
	// resolved 必须与其 firing 对应物落到同一 grouping_key,否则恢复信号找不到
	// 要关闭的 alert_group(两层模型下表现为 group 永不 resolved)。
	case "change", "event", "alert", "resolved":
		return "incident"
	default:
		return t
	}
}

// NormalizeSeverity 原始严重级别 → P1..P4。
func NormalizeSeverity(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "critical", "crit", "p1", "sev1", "fatal":
		return "P1"
	case "error", "high", "major", "p2", "sev2":
		return "P2"
	case "warning", "warn", "medium", "p3", "sev3":
		return "P3"
	default:
		return "P4"
	}
}

// ClassifyFault 基于标签/资源/来源做首版四类故障分类(文档第 1 节)。
func ClassifyFault(s model.Signal) string {
	blob := strings.ToLower(s.SignalType + " " + s.ResourceRef.Kind + " " + labelBlob(s.Labels))
	switch {
	case s.SignalType == "change" || strings.Contains(blob, "deploy") ||
		strings.Contains(blob, "release") || strings.Contains(blob, "rollout") ||
		strings.Contains(blob, "version"):
		return "release_regression"
	case strings.Contains(blob, "crashloop") || strings.Contains(blob, "oomkill") ||
		strings.Contains(blob, "pod") || strings.Contains(blob, "restart") ||
		strings.Contains(blob, "notready") || strings.Contains(blob, "probe"):
		return "pod_workload"
	case strings.Contains(blob, "cpu") || strings.Contains(blob, "memory") ||
		strings.Contains(blob, "throttl") || strings.Contains(blob, "resource") ||
		strings.Contains(blob, "oom"):
		return "resource"
	case strings.Contains(blob, "timeout") || strings.Contains(blob, "latency") ||
		strings.Contains(blob, "5xx") || strings.Contains(blob, "error_rate") ||
		strings.Contains(blob, "dependency") || strings.Contains(blob, "upstream"):
		return "dependency"
	default:
		return "pod_workload"
	}
}

func labelBlob(l map[string]string) string {
	var b strings.Builder
	for k, v := range l {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte(' ')
	}
	return b.String()
}

func buildTitle(s model.Signal) string {
	name := s.Labels["alertname"]
	if name == "" {
		name = s.SignalType
	}
	res := s.ResourceRef.Name
	if res == "" {
		res = s.ResourceRef.Namespace
	}
	if res == "" {
		return name
	}
	return fmt.Sprintf("%s @ %s", name, res)
}

func orDefault(s string) string {
	if s == "" {
		return "default"
	}
	return s
}

func nowOr(t *time.Time) time.Time {
	if t != nil {
		return *t
	}
	return time.Now().UTC()
}
