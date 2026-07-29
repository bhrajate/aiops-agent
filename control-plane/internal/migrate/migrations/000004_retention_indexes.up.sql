-- =============================================================================
-- 000004 — 数据保留(retention)支撑索引
--
-- 背景:此前所有高写入表都是无界增长(signals / investigation_events /
-- audit_log / outbox / dead_letters / idempotency_keys / evidence)。按每集群每天
-- 数千告警估算,signals 与 audit_log 会最先成为运维瓶颈,且随表变大清理越困难。
--
-- 本迁移只补索引;实际清理由控制面后台 Janitor 分批执行(见
-- control-plane/internal/retention)。清理策略刻意**不用 SQL 定时任务**:
--   * 数据库不一定装 pg_cron;
--   * 分批 + 限速 + 指标 + 审计更适合放在应用侧;
--   * 多副本下 Janitor 靠 advisory lock 保证只有一个实例在跑。
--
-- 分区表未采用:首版数据量下 range 分区带来的迁移与运维成本高于收益;
-- 若单表超过 ~1e8 行,应改为按 created_at 做 range 分区并 DROP 旧分区。
-- =============================================================================

-- outbox:已发布记录按 published_at 清理(idx_outbox_status 只覆盖 status,id)。
CREATE INDEX IF NOT EXISTS idx_outbox_published_at
    ON outbox (published_at)
    WHERE status = 'published';

-- investigation_events:清理按时间,已有索引是 (investigation_id, seq)。
CREATE INDEX IF NOT EXISTS idx_inv_events_created_at
    ON investigation_events (created_at);

-- evidence / hypotheses:随 investigation 级联清理时按 investigation_id 定位。
CREATE INDEX IF NOT EXISTS idx_evidence_created_at
    ON evidence (created_at);

-- idempotency_keys:短生命周期,过期即可删。
CREATE INDEX IF NOT EXISTS idx_idempotency_created_at
    ON idempotency_keys (created_at);

-- dead_letters 已有 (topic, created_at);补一个纯时间索引供清理扫描。
CREATE INDEX IF NOT EXISTS idx_dead_letters_created_at
    ON dead_letters (created_at);

-- 注:alert_groups(incident_id) 的索引已由 000003 建立,此处不重复声明
-- (重复声明会让 down 的归属变得含糊:回滚 000004 时不该删掉 000003 建的索引)。

-- incidents:只清理**终态且过期**的 incident,需要 (status, closed/resolved 时间)。
CREATE INDEX IF NOT EXISTS idx_incidents_terminal_age
    ON incidents (status, updated_at)
    WHERE status IN ('resolved', 'closed');

-- investigations:同上,终态才可清理。该表无 updated_at,用 started_at 作锚点
-- (ended_at 可为空:Temporal 启动失败等降级路径下调查记录仍会保留)。
CREATE INDEX IF NOT EXISTS idx_investigations_terminal_age
    ON investigations (phase, started_at)
    WHERE phase IN ('closed', 'cancelled');
