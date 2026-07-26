-- =============================================================================
-- 生产化硬化:幂等键、死信队列、Golden Dataset(评测)。
-- 对应 docs/SECURITY.md §5/§7 与架构文档第 18 节。
-- =============================================================================

-- Idempotency-Key 落库(SECURITY §5)
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key            TEXT PRIMARY KEY,
    scope          TEXT NOT NULL,          -- 例如 start_investigation
    target_id      TEXT NOT NULL,          -- 关联对象(如 incident_id)
    result_id      TEXT NOT NULL,          -- 首次产生的结果(如 investigation_id)
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 死信队列(SECURITY §7):消费重试超限的消息
CREATE TABLE IF NOT EXISTS dead_letters (
    id             BIGSERIAL PRIMARY KEY,
    topic          TEXT NOT NULL,
    key            TEXT,
    payload        JSONB NOT NULL,
    error          TEXT,
    attempts       INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_dead_letters_topic ON dead_letters (topic, created_at);

-- Golden Dataset(架构文档 18.2):关闭 Incident 的标注,供离线回放与质量门槛
CREATE TABLE IF NOT EXISTS golden_cases (
    case_id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tenant_id          TEXT NOT NULL DEFAULT 'default',
    incident_id        TEXT,
    fault_category     TEXT NOT NULL,          -- 最终根因类别
    root_cause         TEXT NOT NULL,          -- 标注的真实根因
    affected_component TEXT,
    signal_fixture     JSONB NOT NULL,         -- 回放用的输入 Signal(可复现)
    expected_top_causes JSONB NOT NULL DEFAULT '[]', -- 期望命中的根因关键词
    review_status      TEXT NOT NULL DEFAULT 'approved', -- pending/approved/rejected
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_golden_cases_category ON golden_cases (fault_category);

-- 评测运行记录:每次离线回放/回归的聚合指标
CREATE TABLE IF NOT EXISTS evaluation_runs (
    run_id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tenant_id          TEXT NOT NULL DEFAULT 'default',
    model_version      TEXT,
    prompt_version     TEXT,
    policy_version     TEXT,
    total_cases        INTEGER NOT NULL DEFAULT 0,
    top1_hits          INTEGER NOT NULL DEFAULT 0,
    top3_hits          INTEGER NOT NULL DEFAULT 0,
    evidence_citation_rate DOUBLE PRECISION,   -- 关键结论证据引用率
    hallucination_rate DOUBLE PRECISION,        -- 无证据支撑根因比例
    p95_first_diag_sec DOUBLE PRECISION,
    detail             JSONB NOT NULL DEFAULT '{}',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
