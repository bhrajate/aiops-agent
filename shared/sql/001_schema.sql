-- =============================================================================
-- AIOps Agent — 业务数据库 Schema (PostgreSQL + pgvector)
-- 对应架构设计文档第 10 节(数据模型)与第 13 节(存储设计)。
--
-- 说明:
--   * 业务数据库是 Incident / Investigation / Evidence 的"事实源"(source of truth)。
--   * Temporal 使用独立数据库,不在此 Schema 内。
--   * 所有表预留 tenant_id 以支持未来多租户。
--   * 领域事件通过 outbox 表 + Outbox Pattern 发布,避免"状态已提交但事件未发布"。
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS "pgcrypto";   -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS vector;        -- pgvector,用于 Knowledge Service RAG

-- -----------------------------------------------------------------------------
-- Signal:归一化后的原始信号(告警 / 变更 / 事件 / 恢复)
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS signals (
    signal_id     TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL DEFAULT 'default',
    cluster_id    TEXT NOT NULL,
    source        TEXT NOT NULL,                 -- alertmanager / kubernetes / cicd / itsm / slo
    signal_type   TEXT NOT NULL,                 -- alert / change / event / resolved
    resource_ref  JSONB NOT NULL DEFAULT '{}',   -- {namespace, kind, name, uid}
    severity      TEXT,                          -- 原始严重级别
    starts_at     TIMESTAMPTZ,
    ends_at       TIMESTAMPTZ,
    labels        JSONB NOT NULL DEFAULT '{}',
    payload_ref   TEXT,                          -- 原始载荷引用或哈希(对象存储 key)
    payload_hash  TEXT,
    incident_id   TEXT,                          -- 聚合后归属的 Incident(可空)
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_signals_cluster       ON signals (cluster_id);
CREATE INDEX IF NOT EXISTS idx_signals_incident      ON signals (incident_id);
CREATE INDEX IF NOT EXISTS idx_signals_received_at   ON signals (received_at);

-- -----------------------------------------------------------------------------
-- Incident:去重聚合后的事件,拥有稳定 ID 与内容版本
-- 幂等键: tenant_id / cluster_id / namespace / resource_uid / signal_type / rule_id
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS incidents (
    incident_id        TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL DEFAULT 'default',
    cluster_id         TEXT NOT NULL,
    version            INTEGER NOT NULL DEFAULT 1,     -- 内容版本,新增 Signal 递增
    grouping_key       TEXT NOT NULL,                  -- 聚合与幂等键
    status             TEXT NOT NULL DEFAULT 'open',   -- open / acknowledged / resolved / closed
    severity           TEXT NOT NULL DEFAULT 'P3',     -- 归一化严重级别 P1..P4
    title              TEXT NOT NULL,
    fault_category     TEXT,                           -- release_regression / pod_workload / resource / dependency
    affected_resources JSONB NOT NULL DEFAULT '[]',
    blast_radius       JSONB NOT NULL DEFAULT '{}',
    topology_refs      JSONB NOT NULL DEFAULT '[]',
    change_refs        JSONB NOT NULL DEFAULT '[]',
    signal_count       INTEGER NOT NULL DEFAULT 0,
    first_seen         TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen          TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at        TIMESTAMPTZ,
    closed_at          TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (grouping_key)
);
CREATE INDEX IF NOT EXISTS idx_incidents_status   ON incidents (status);
CREATE INDEX IF NOT EXISTS idx_incidents_cluster  ON incidents (cluster_id);
CREATE INDEX IF NOT EXISTS idx_incidents_severity ON incidents (severity);

-- -----------------------------------------------------------------------------
-- Investigation:一次调查(绑定 Incident 的某个版本 + Temporal Workflow)
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS investigations (
    investigation_id  TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL DEFAULT 'default',
    incident_id       TEXT NOT NULL REFERENCES incidents (incident_id),
    incident_version  INTEGER NOT NULL,
    workflow_id       TEXT,                            -- Temporal Workflow ID
    run_id            TEXT,
    phase             TEXT NOT NULL DEFAULT 'queued',  -- 见状态机
    trigger_reason    TEXT,                            -- 触发原因(策略判定或人工)
    triggered_by      TEXT,                            -- system / <user>
    budget            JSONB NOT NULL DEFAULT '{}',     -- 时间/token/费用/工具 预算
    usage             JSONB NOT NULL DEFAULT '{}',     -- 实际用量
    model_version     TEXT,
    prompt_version    TEXT,
    policy_version    TEXT,
    diagnosis         JSONB,                           -- 最终 DiagnosisResult
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at          TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_investigations_incident ON investigations (incident_id);
CREATE INDEX IF NOT EXISTS idx_investigations_phase    ON investigations (phase);

-- -----------------------------------------------------------------------------
-- Evidence:调查过程中冻结的证据(不可变)
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS evidence (
    evidence_id      TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL DEFAULT 'default',
    investigation_id TEXT NOT NULL REFERENCES investigations (investigation_id),
    type             TEXT NOT NULL,                    -- metric / log / trace / kubernetes / change / knowledge
    source           TEXT NOT NULL,                    -- 实际数据源
    tool_name        TEXT,                             -- 产生该证据的工具
    query            JSONB NOT NULL DEFAULT '{}',      -- 脱敏归一化后的查询
    time_range       JSONB,
    summary          TEXT NOT NULL,                    -- 供推理使用的受控摘要
    raw_ref          TEXT,                             -- 原始数据引用(对象存储 key)
    content_hash     TEXT NOT NULL,                    -- 防篡改哈希
    freshness        TEXT,                             -- 数据新鲜度
    redaction_status TEXT NOT NULL DEFAULT 'clean',    -- clean / redacted
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_evidence_investigation ON evidence (investigation_id);
CREATE INDEX IF NOT EXISTS idx_evidence_type          ON evidence (type);

-- -----------------------------------------------------------------------------
-- Hypothesis:根因假设,绑定支持/反对证据
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS hypotheses (
    hypothesis_id           TEXT PRIMARY KEY,
    tenant_id               TEXT NOT NULL DEFAULT 'default',
    investigation_id        TEXT NOT NULL REFERENCES investigations (investigation_id),
    rank                    INTEGER,
    statement               TEXT NOT NULL,
    component_ref           JSONB,
    confidence              DOUBLE PRECISION NOT NULL DEFAULT 0,  -- 校准后置信度 [0,1]
    supporting_evidence_ids JSONB NOT NULL DEFAULT '[]',
    contradicting_evidence_ids JSONB NOT NULL DEFAULT '[]',
    missing_evidence        JSONB NOT NULL DEFAULT '[]',
    status                  TEXT NOT NULL DEFAULT 'proposed', -- proposed / supported / rejected / unresolved
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_hypotheses_investigation ON hypotheses (investigation_id);

-- -----------------------------------------------------------------------------
-- Investigation 时间线事件(用于 SSE 推送与回放)
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS investigation_events (
    id               BIGSERIAL PRIMARY KEY,
    investigation_id TEXT NOT NULL REFERENCES investigations (investigation_id),
    seq              INTEGER NOT NULL,
    event_type       TEXT NOT NULL,   -- phase_changed / tool_called / evidence_added / hypothesis_updated / diagnosis_published / human_feedback / escalated
    payload          JSONB NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (investigation_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_inv_events_inv ON investigation_events (investigation_id, seq);

-- -----------------------------------------------------------------------------
-- 人工反馈(第 18 节:先进入审核队列,审核后才能成为 Golden Case)
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS human_feedback (
    feedback_id      TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    investigation_id TEXT NOT NULL REFERENCES investigations (investigation_id),
    author           TEXT NOT NULL,
    action           TEXT NOT NULL,   -- confirm / correct / reject / close
    confirmed_root_cause TEXT,
    comment          TEXT,
    review_status    TEXT NOT NULL DEFAULT 'pending', -- pending / approved / rejected
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- -----------------------------------------------------------------------------
-- 审计日志(第 14.3 节)
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS audit_log (
    id            BIGSERIAL PRIMARY KEY,
    tenant_id     TEXT NOT NULL DEFAULT 'default',
    actor         TEXT NOT NULL,          -- system / model / <user> / cluster-agent
    action        TEXT NOT NULL,
    target_type   TEXT,
    target_id     TEXT,
    scope         JSONB,                  -- cluster / namespace / resource / time_range
    result        TEXT,                   -- allowed / denied / ok / error
    detail        JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_target ON audit_log (target_type, target_id);
CREATE INDEX IF NOT EXISTS idx_audit_time   ON audit_log (created_at);

-- -----------------------------------------------------------------------------
-- Outbox(第 13 节:Outbox Pattern)——业务事务内写入,后台投递到事件总线
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS outbox (
    id            BIGSERIAL PRIMARY KEY,
    topic         TEXT NOT NULL,           -- signals / incidents / investigations
    key           TEXT NOT NULL,
    payload       JSONB NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending', -- pending / published / failed
    attempts      INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_outbox_status ON outbox (status, id);

-- -----------------------------------------------------------------------------
-- Knowledge Base(第 12.2 节 RAG 边界)——Runbook / 架构文档 / 历史 Incident
-- 使用 pgvector 存储 embedding。维度 1536(可按 embedding 模型调整)。
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS knowledge_items (
    knowledge_id   TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tenant_id      TEXT NOT NULL DEFAULT 'default',
    kind           TEXT NOT NULL,          -- runbook / architecture / service_catalog / historical_incident / postmortem
    title          TEXT NOT NULL,
    content        TEXT NOT NULL,
    applies_to     JSONB NOT NULL DEFAULT '{}', -- {service, environment}
    version        TEXT,
    valid_until    TIMESTAMPTZ,            -- 失效时间
    embedding      vector(1536),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_knowledge_kind ON knowledge_items (kind);
-- 向量索引(数据量小时可省略,规模上来后启用)
-- CREATE INDEX ON knowledge_items USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
