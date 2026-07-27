-- =============================================================================
-- 两层告警聚合模型(优化②):拆开"去重"与"聚合"两个职责。
--
-- 问题:原设计用单一 grouping_key(把 resource 编进哈希)同时承担去重与聚合,
-- 结果只做到"同资源去重" —— 跨资源故障永远进不了同一 incident,
-- blast_radius 无法反映影响面扩大,值班人员看到 N 个碎片化 incident。
--
-- 新模型:
--   alert_groups  = 去重单元(原 grouping_key 语义:同资源+同规则的重复告警收敛为一条)
--   incidents     = 相关性单元(可包含多个 alert_group;按 correlation_key + 时间窗合并)
--
-- incidents.affected_resources / blast_radius / severity / signal_count
-- 由其下所有 alert_groups 聚合得出,不再是"第一条信号的快照"。
-- =============================================================================

-- ---------------------------------------------------------------- 去重单元
CREATE TABLE IF NOT EXISTS alert_groups (
    group_id      TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL DEFAULT 'default',
    cluster_id    TEXT NOT NULL,
    grouping_key  TEXT NOT NULL,              -- tenant/cluster/ns/resource/type/rule 哈希
    incident_id   TEXT REFERENCES incidents (incident_id) ON DELETE SET NULL,
    namespace     TEXT NOT NULL DEFAULT '',
    resource_ref  JSONB NOT NULL DEFAULT '{}',
    severity      TEXT NOT NULL DEFAULT 'P4', -- 该组归一化严重级别
    fault_category TEXT,
    title         TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'open', -- open / resolved
    signal_count  INTEGER NOT NULL DEFAULT 0,
    first_seen    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (grouping_key)
);
CREATE INDEX IF NOT EXISTS idx_alert_groups_incident ON alert_groups (incident_id);
CREATE INDEX IF NOT EXISTS idx_alert_groups_scope
    ON alert_groups (tenant_id, cluster_id, namespace, status, last_seen);

-- ---------------------------------------------------------------- 相关性单元
-- incidents 改为按 correlation_key 唯一(tenant/cluster/namespace),
-- 原 grouping_key 列保留但不再唯一(历史兼容 / 便于回溯)。
ALTER TABLE incidents ADD COLUMN IF NOT EXISTS correlation_key TEXT;

-- 回填:历史 incident 的 correlation_key 用 tenant|cluster|namespace 推导
UPDATE incidents
   SET correlation_key = tenant_id || '|' || cluster_id || '|' ||
                         COALESCE(affected_resources->0->>'namespace', '')
 WHERE correlation_key IS NULL;

-- 去掉 grouping_key 的唯一约束(新模型下多个 incident 可共享/为空)
ALTER TABLE incidents DROP CONSTRAINT IF EXISTS incidents_grouping_key_key;

-- correlation_key 需要唯一以支持 find-or-create 的原子 upsert。
-- 注:同一 namespace 在时间窗外的新故障通过"关闭旧 incident"来区分,
-- 因此这里用 (correlation_key) WHERE status 活跃 的部分唯一索引。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_incidents_active_correlation
    ON incidents (correlation_key)
 WHERE status IN ('open', 'acknowledged');

CREATE INDEX IF NOT EXISTS idx_incidents_correlation ON incidents (correlation_key);
