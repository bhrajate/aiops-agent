package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aiops/control-plane/internal/model"
	"github.com/jackc/pgx/v5"
)

// 两层聚合模型(优化②):
//   alert_groups = 去重单元(同资源+同规则的重复告警收敛)
//   incidents    = 相关性单元(按 correlation_key 合并同 tenant/cluster/namespace 的多个 group)
// incident 的 affected_resources / blast_radius / severity / signal_count 由其
// 下所有活跃 group 聚合得出,因此"影响面扩大"天然可见。

// AlertGroupInput 一条信号归一化后的去重单元输入。
type AlertGroupInput struct {
	GroupID       string
	TenantID      string
	ClusterID     string
	GroupingKey   string
	Namespace     string
	ResourceRef   model.ResourceRef
	Severity      string
	FaultCategory string
	Title         string
}

// CorrelationKey 相关性键:同 tenant/cluster/namespace 的故障视为同一 incident。
// 这是"聚合"维度,与 grouping_key("去重"维度,含 resource)正交。
func CorrelationKey(tenant, cluster, namespace string) string {
	return tenant + "|" + cluster + "|" + namespace
}

// UpsertAlertGroupAndCorrelate 在单事务内:
//  1. upsert alert_group(按 grouping_key 去重,递增 signal_count / 更新 last_seen);
//  2. find-or-create 该 correlation_key 的活跃 incident;
//  3. 把 group 关联到 incident;
//  4. 由该 incident 下所有活跃 group 重算 affected_resources / blast_radius /
//     severity / signal_count / first_seen / last_seen,并递增 version;
//  5. 写 incidents outbox。
//
// 返回聚合后的 Incident 与 incident 是否为新建。
// windowSec 是相关性合并的时间窗(秒):只与该窗口内仍活跃的 incident 合并。
func (s *Store) UpsertAlertGroupAndCorrelate(
	ctx context.Context, g AlertGroupInput, lastSeen, firstSeen interface{}, windowSec int,
) (model.Incident, bool, error) {
	var result model.Incident
	var created bool

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// 1) upsert 去重单元
		var groupID string
		err := tx.QueryRow(ctx,
			`INSERT INTO alert_groups
			   (group_id, tenant_id, cluster_id, grouping_key, namespace, resource_ref,
			    severity, fault_category, title, status, signal_count, first_seen, last_seen)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'open',1,$10,$10)
			 ON CONFLICT (grouping_key) DO UPDATE SET
			   signal_count = alert_groups.signal_count + 1,
			   last_seen    = EXCLUDED.last_seen,
			   status       = 'open',
			   severity     = LEAST(alert_groups.severity, EXCLUDED.severity),
			   updated_at   = now()
			 RETURNING group_id`,
			g.GroupID, g.TenantID, g.ClusterID, g.GroupingKey, g.Namespace,
			mustJSON(g.ResourceRef), g.Severity, g.FaultCategory, g.Title, lastSeen).Scan(&groupID)
		if err != nil {
			return fmt.Errorf("upsert alert_group: %w", err)
		}

		// 2) find-or-create 相关性单元(活跃 incident)。
		//
		// 时间窗约束:只与"仍在活跃时间窗内"的 incident 合并。若同 correlation_key
		// 的 incident 已陈旧(windowSec 内无新信号),说明上一轮故障已停止,
		// 不应把新故障并进去——否则一个 open 的 incident 会无限吸收后续无关故障,
		// 滚成永不关闭的巨型 incident 并污染 blast_radius / 深度 RCA 闸门。
		// 陈旧 incident 先自动 resolved(连同其 group),再新建。
		ck := CorrelationKey(g.TenantID, g.ClusterID, g.Namespace)
		var incidentID string
		var stale bool
		// 陈旧判定用 updated_at(**处理时间**:我们最后一次为该 incident 处理信号的时刻),
		// 而不是 last_seen(**事件时间**:来自告警 payload 的 startsAt)。
		// 告警可能携带很旧的 startsAt(补发、时钟漂移、回放),用事件时间判活会把
		// 刚创建的 incident 误判为陈旧。last_seen 仍保留事件时间语义,供展示与 RCA 时间窗使用。
		err = tx.QueryRow(ctx,
			`SELECT incident_id,
			        (updated_at < now() - make_interval(secs => $2::double precision)) AS stale
			   FROM incidents
			  WHERE correlation_key=$1 AND status IN ('open','acknowledged')
			  FOR UPDATE`, ck, windowSec).Scan(&incidentID, &stale)
		if err == nil && stale {
			// 自动关闭陈旧 incident 及其 group,腾出 correlation_key(部分唯一索引)
			if _, e := tx.Exec(ctx,
				`UPDATE alert_groups SET status='resolved', updated_at=now()
				  WHERE incident_id=$1 AND status='open' AND group_id <> $2`,
				incidentID, groupID); e != nil {
				return fmt.Errorf("resolve stale groups: %w", e)
			}
			if _, e := tx.Exec(ctx,
				`UPDATE incidents SET status='resolved', resolved_at=now(), updated_at=now()
				  WHERE incident_id=$1`, incidentID); e != nil {
				return fmt.Errorf("resolve stale incident: %w", e)
			}
			err = pgx.ErrNoRows // 走下面的新建分支
		}
		switch {
		case err == pgx.ErrNoRows:
			created = true
			incidentID = "inc-" + randHexStore(10)
			_, err = tx.Exec(ctx,
				`INSERT INTO incidents
				   (incident_id, tenant_id, cluster_id, version, grouping_key, correlation_key,
				    status, severity, title, fault_category, affected_resources, blast_radius,
				    topology_refs, change_refs, signal_count, first_seen, last_seen)
				 VALUES ($1,$2,$3,1,$4,$5,'open',$6,$7,$8,'[]','{}','[]','[]',0,$9,$9)`,
				incidentID, g.TenantID, g.ClusterID, g.GroupingKey, ck,
				g.Severity, g.Title, g.FaultCategory, firstSeen)
			if err != nil {
				return fmt.Errorf("create incident: %w", err)
			}
		case err != nil:
			return fmt.Errorf("find incident: %w", err)
		}

		// 3) 关联 group → incident
		if _, err := tx.Exec(ctx,
			`UPDATE alert_groups SET incident_id=$1, updated_at=now() WHERE group_id=$2`,
			incidentID, groupID); err != nil {
			return fmt.Errorf("link group: %w", err)
		}

		// 4) 由该 incident 下所有活跃 group 重算聚合量
		if err := recomputeIncidentFromGroups(ctx, tx, incidentID); err != nil {
			return err
		}

		loaded, lerr := loadIncidentTx(ctx, tx, incidentID)
		if lerr != nil {
			return lerr
		}
		result = loaded
		// 5) 领域事件
		return EnqueueOutboxTx(ctx, tx, "incidents", result.IncidentID, result)
	})
	return result, created, err
}

// recomputeIncidentFromGroups 用 incident 下所有活跃 alert_group 重算:
// affected_resources(去重并集)、blast_radius(services/namespaces/groups)、
// severity(取最高)、signal_count(求和)、first/last_seen(极值),并 version+1。
func recomputeIncidentFromGroups(ctx context.Context, tx pgx.Tx, incidentID string) error {
	rows, err := tx.Query(ctx,
		`SELECT resource_ref, namespace, severity, signal_count, first_seen, last_seen, fault_category
		   FROM alert_groups
		  WHERE incident_id=$1 AND status='open'`, incidentID)
	if err != nil {
		return fmt.Errorf("load groups: %w", err)
	}
	type agg struct {
		resources   []model.ResourceRef
		namespaces  map[string]struct{}
		services    map[string]struct{}
		groups      int
		severity    string
		signalCount int
		category    string
	}
	a := agg{
		namespaces: map[string]struct{}{},
		services:   map[string]struct{}{},
		severity:   "P4",
	}
	seen := map[string]struct{}{}
	for rows.Next() {
		var refRaw []byte
		var ns, sev, cat string
		var cnt int
		var fs, ls any
		if err := rows.Scan(&refRaw, &ns, &sev, &cnt, &fs, &ls, &cat); err != nil {
			rows.Close()
			return err
		}
		var ref model.ResourceRef
		_ = json.Unmarshal(refRaw, &ref)
		a.groups++
		key := ref.Namespace + "/" + ref.Kind + "/" + ref.Name
		if _, dup := seen[key]; !dup {
			seen[key] = struct{}{}
			a.resources = append(a.resources, ref)
		}
		// 服务维度:把 Pod 归约到其所属工作负载,避免"同一服务多个 Pod"
		// 被当成"多个服务受影响"而误触发深度 RCA(见 model.ServiceKey)。
		a.services[model.ServiceKey(ref)] = struct{}{}
		if ns != "" {
			a.namespaces[ns] = struct{}{}
		}
		if sev < a.severity { // "P1" < "P2" 字典序即严重度序
			a.severity = sev
		}
		a.signalCount += cnt
		if a.category == "" {
			a.category = cat
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	nsCount := len(a.namespaces)
	if nsCount < 1 {
		nsCount = 1
	}
	// 三个维度语义不同,不可互相替代:
	//   services  = 受影响的服务数(Pod 已归约到工作负载)—— 驱动深度 RCA 闸门
	//   resources = 受影响的具体资源数(Pod 级)—— 展示用
	//   groups    = 去重单元数(告警规则×资源)—— 噪声量级
	blast := map[string]any{
		"services":   len(a.services),
		"namespaces": nsCount,
		"resources":  len(a.resources),
		"groups":     a.groups,
	}
	_, err = tx.Exec(ctx,
		`UPDATE incidents SET
		   version = version + 1,
		   affected_resources = $1,
		   blast_radius = $2,
		   severity = $3,
		   signal_count = $4,
		   fault_category = COALESCE(NULLIF($5,''), fault_category),
		   first_seen = LEAST(first_seen, (SELECT min(first_seen) FROM alert_groups WHERE incident_id=$6 AND status='open')),
		   last_seen  = GREATEST(last_seen, (SELECT max(last_seen)  FROM alert_groups WHERE incident_id=$6 AND status='open')),
		   updated_at = now()
		 WHERE incident_id=$6`,
		mustJSON(a.resources), mustJSON(blast), a.severity, a.signalCount, a.category, incidentID)
	if err != nil {
		return fmt.Errorf("recompute incident: %w", err)
	}
	return nil
}

// ResolveAlertGroup 将某去重单元标记 resolved,并重算所属 incident;
// 当 incident 下已无活跃 group 时,将 incident 一并标记 resolved。
func (s *Store) ResolveAlertGroup(ctx context.Context, groupingKey, tenant string) (string, bool, error) {
	var incidentID string
	var incidentResolved bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`UPDATE alert_groups SET status='resolved', updated_at=now()
			 WHERE grouping_key=$1 AND tenant_id=$2
			 RETURNING COALESCE(incident_id,'')`, groupingKey, tenant).Scan(&incidentID)
		if err != nil {
			return err // 含 ErrNoRows:无此 group
		}
		if incidentID == "" {
			return nil
		}
		var remaining int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM alert_groups WHERE incident_id=$1 AND status='open'`,
			incidentID).Scan(&remaining); err != nil {
			return err
		}
		if remaining == 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE incidents SET status='resolved', resolved_at=now(), updated_at=now()
				 WHERE incident_id=$1`, incidentID); err != nil {
				return err
			}
			incidentResolved = true
			return nil
		}
		return recomputeIncidentFromGroups(ctx, tx, incidentID)
	})
	return incidentID, incidentResolved, err
}

// ListAlertGroups 返回某 incident 下的去重单元(供 API/Workbench 展示碎片明细)。
func (s *Store) ListAlertGroups(ctx context.Context, incidentID string) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT group_id, namespace, resource_ref, severity, COALESCE(fault_category,''),
		        title, status, signal_count, first_seen, last_seen
		   FROM alert_groups WHERE incident_id=$1 ORDER BY first_seen`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, ns, sev, cat, title, status string
		var refRaw []byte
		var cnt int
		var fs, ls any
		if err := rows.Scan(&id, &ns, &refRaw, &sev, &cat, &title, &status, &cnt, &fs, &ls); err != nil {
			return nil, err
		}
		var ref model.ResourceRef
		_ = json.Unmarshal(refRaw, &ref)
		out = append(out, map[string]any{
			"group_id": id, "namespace": ns, "resource": ref, "severity": sev,
			"fault_category": cat, "title": title, "status": status,
			"signal_count": cnt, "first_seen": fs, "last_seen": ls,
		})
	}
	return out, rows.Err()
}
