-- =============================================================================
-- 000006 — 服务依赖拓扑
--
-- 背景:`incidents.topology_refs` 从 000001 起就有这一列,但**从来没有写入路径**。
-- 相关性合并只按 tenant|cluster|namespace,于是调用链上的故障传播识别不了:
-- checkout 挂了导致 payment-api 超时,值班人员看到两个互不相关的 incident,
-- 而根因只有一个。
--
-- 边的来源(按置信度从高到低):
--   1. Tempo service graph(traces_service_graph_request_total{client,server})
--      —— 真实调用关系,由 metrics-generator 从 trace 的父子 span 推导。
--   2. Kubernetes Service selector —— 只表达入口关系(Service → 工作负载),
--      不是调用图。cluster-agent 已有该能力(kubernetes_topology.go),
--      作为 Tempo 不可用时的降级来源。
--
-- 为什么存成表而不是每次现查:
--   * 相关性合并在信号入库的事务里,不能在那里发 HTTP 请求(慢且会让入库失败);
--   * 拓扑变化远慢于告警频率,周期同步足够;
--   * 存下来才能回答"这条边是什么时候消失的"——服务下线本身是故障线索。
-- =============================================================================

CREATE TABLE IF NOT EXISTS service_topology (
    edge_id       BIGSERIAL PRIMARY KEY,
    tenant_id     TEXT NOT NULL DEFAULT 'default',
    cluster_id    TEXT NOT NULL,
    -- 服务名。与 model.ServiceKey 的口径一致(Pod 已归约到工作负载),
    -- 否则拓扑里的名字与 incident 里的资源名对不上,关联永远命中不了。
    from_service  TEXT NOT NULL,
    to_service    TEXT NOT NULL,
    -- 命名空间。Tempo 侧不一定带,留空表示未知;K8s 来源必然有。
    from_namespace TEXT NOT NULL DEFAULT '',
    to_namespace   TEXT NOT NULL DEFAULT '',
    -- 边的类型:call(服务调用)/ database / messaging / service-frontend(K8s 入口)
    kind          TEXT NOT NULL DEFAULT 'call',
    -- 来源与置信度。置信度参与关联决策:低置信度的边不足以合并 incident。
    source        TEXT NOT NULL,              -- tempo-service-graph / kubernetes-service
    confidence    DOUBLE PRECISION NOT NULL DEFAULT 0.5,
    -- 观测窗口内的请求量,用于在多条边里挑主路径(次要通路不该主导关联)。
    request_rate  DOUBLE PRECISION NOT NULL DEFAULT 0,
    first_seen    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, cluster_id, from_service, to_service, kind, source)
);

-- 关联查询是"给定服务,找它的邻居",两个方向都要走索引。
CREATE INDEX IF NOT EXISTS idx_topology_from
    ON service_topology (tenant_id, cluster_id, from_service, last_seen);
CREATE INDEX IF NOT EXISTS idx_topology_to
    ON service_topology (tenant_id, cluster_id, to_service, last_seen);
-- 陈旧边清理按 last_seen 扫描(retention janitor 用)。
CREATE INDEX IF NOT EXISTS idx_topology_last_seen
    ON service_topology (last_seen);

-- ---------------------------------------------------------------- incident 关联
-- 刻意**不合并** incident,而是建立"疑似同源"链接。
--
-- 合并的诱惑很大(值班人员只看一个),但风险不可接受:correlation_key 上有部分
-- 唯一索引,跨 namespace 合并会破坏它;更糟的是一条误判的拓扑边会把两次无关故障
-- 焊死成一个 incident,而拆分比合并难得多——已经写进去的 signal/证据没法回滚归属。
--
-- 链接给出同样的信息(值班人员看到"疑似与 inc-x 同源"),但可以随时撤销,
-- 且保留了两个 incident 各自独立的时间线与影响面。
CREATE TABLE IF NOT EXISTS incident_relations (
    relation_id   BIGSERIAL PRIMARY KEY,
    tenant_id     TEXT NOT NULL DEFAULT 'default',
    -- 方向有意义:upstream 表示 related_incident 在调用链上游(更可能是根因)。
    incident_id   TEXT NOT NULL REFERENCES incidents (incident_id) ON DELETE CASCADE,
    related_incident_id TEXT NOT NULL REFERENCES incidents (incident_id) ON DELETE CASCADE,
    relation      TEXT NOT NULL,              -- upstream / downstream
    -- 判定依据,便于值班人员判断可信度而不是盲信。
    via_edge      JSONB NOT NULL DEFAULT '{}',
    confidence    DOUBLE PRECISION NOT NULL DEFAULT 0.5,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (incident_id, related_incident_id, relation)
);
CREATE INDEX IF NOT EXISTS idx_incident_relations_inc
    ON incident_relations (incident_id);
