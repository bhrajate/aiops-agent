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

	inc := model.Incident{
		IncidentID:        newIncidentID(),
		TenantID:          orDefault(sig.TenantID),
		ClusterID:         sig.ClusterID,
		GroupingKey:       groupingKey,
		Severity:          NormalizeSeverity(sig.Severity),
		Title:             buildTitle(sig),
		FaultCategory:     ClassifyFault(sig),
		AffectedResources: []model.ResourceRef{sig.ResourceRef},
		BlastRadius:       map[string]any{"namespaces": 1, "resources": 1},
		TopologyRefs:      []any{},
		ChangeRefs:        []any{},
		LastSeen:          nowOr(sig.StartsAt),
	}

	agg, created, err := m.store.UpsertIncidentWithOutbox(ctx, inc)
	if err != nil {
		return fmt.Errorf("upsert incident: %w", err)
	}
	if err := m.store.AttachSignalToIncident(ctx, sig.SignalID, agg.IncidentID); err != nil {
		m.log.Warn("attach signal failed", "err", err)
	}

	// 相关性影响面(文档 6.2):在 grouping_key 单资源去重之上,按 tenant/cluster/namespace +
	// 时间窗聚合活跃 incident,算出真实 services/namespaces,写回 incident 行。
	// 这让"影响面扩大"能被 worker 的深度 RCA 闸门(policy.py: blast.services>1)捕获。
	ns := ""
	if len(agg.AffectedResources) > 0 {
		ns = agg.AffectedResources[0].Namespace
	}
	if br, berr := m.store.ComputeCorrelatedBlastRadius(ctx, agg.TenantID, agg.ClusterID, ns, m.correlationWindowSec); berr != nil {
		m.log.Warn("compute blast radius failed", "err", berr)
	} else if err := m.store.SetIncidentBlastRadius(ctx, agg.IncidentID, br); err != nil {
		m.log.Warn("set blast radius failed", "err", err)
	} else if br.Services > 1 || br.Namespaces > 1 {
		m.log.Info("blast radius expanded", "incident_id", agg.IncidentID,
			"services", br.Services, "namespaces", br.Namespaces)
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

func (m *Manager) handleResolved(ctx context.Context, sig model.Signal, groupingKey string) error {
	// 按 grouping_key + tenant 找到 incident 并标记 resolved(tenant 显式过滤,
	// 不再仅依赖 tenant 已编进 grouping_key 哈希的隐式隔离)。
	inc, err := m.findByGroupingKey(ctx, groupingKey, orDefault(sig.TenantID))
	if err != nil {
		return nil // 找不到对应 incident,忽略
	}
	if err := m.store.SetIncidentStatus(ctx, inc.IncidentID, "resolved"); err != nil {
		return err
	}
	m.log.Info("incident resolved by signal", "incident_id", inc.IncidentID)
	return nil
}

func (m *Manager) findByGroupingKey(ctx context.Context, key, tenant string) (model.Incident, error) {
	row := m.store.Pool().QueryRow(ctx,
		`SELECT incident_id, tenant_id, cluster_id, version, grouping_key, status, severity,
		   title, COALESCE(fault_category,''), signal_count
		 FROM incidents WHERE grouping_key=$1 AND tenant_id=$2`, key, tenant)
	var inc model.Incident
	err := row.Scan(&inc.IncidentID, &inc.TenantID, &inc.ClusterID, &inc.Version, &inc.GroupingKey,
		&inc.Status, &inc.Severity, &inc.Title, &inc.FaultCategory, &inc.SignalCount)
	return inc, err
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
	case "change", "event", "alert":
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

func newIncidentID() string {
	return "inc-" + randHex(10)
}
