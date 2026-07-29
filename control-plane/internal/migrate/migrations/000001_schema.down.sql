-- 回滚 000001_schema:按外键依赖的**逆序**删表。
--
-- 说明:down 的用途是本地开发与 CI 上的快速重置,以及生产升级失败时的应急回退。
-- 它会**删除全部业务数据**,生产执行前必须先有可用备份(见 deploy/DEPLOY.md §7)。
-- 扩展(pgcrypto / vector)不在此删除:它们可能被同库的其他 schema 使用,
-- 且重复创建是幂等的,删掉反而有破坏面。

DROP TABLE IF EXISTS knowledge_items;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS human_feedback;
DROP TABLE IF EXISTS investigation_events;
DROP TABLE IF EXISTS hypotheses;
DROP TABLE IF EXISTS evidence;
DROP TABLE IF EXISTS investigations;
DROP TABLE IF EXISTS incidents;
DROP TABLE IF EXISTS signals;
