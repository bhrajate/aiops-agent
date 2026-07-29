-- 回滚 000004_retention_indexes:只删本迁移新建的索引,不动任何数据。
--
-- 纯索引回滚,幂等且无数据风险。清理逻辑本身在应用侧
-- (control-plane/internal/retention),删索引只会让清理扫描变慢,不改变语义。
--
-- 注:idx_alert_groups_incident 由 000003 建立、000004 用 IF NOT EXISTS 重复声明,
-- 归属在 000003,故此处**不删**——否则回滚 000004 会破坏 000003 的状态。

DROP INDEX IF EXISTS idx_investigations_terminal_age;
DROP INDEX IF EXISTS idx_incidents_terminal_age;
DROP INDEX IF EXISTS idx_dead_letters_created_at;
DROP INDEX IF EXISTS idx_idempotency_created_at;
DROP INDEX IF EXISTS idx_evidence_created_at;
DROP INDEX IF EXISTS idx_inv_events_created_at;
DROP INDEX IF EXISTS idx_outbox_published_at;
