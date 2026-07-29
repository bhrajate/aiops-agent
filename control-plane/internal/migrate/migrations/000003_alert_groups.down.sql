-- 回滚 000003_alert_groups:退回单层聚合模型(incidents.grouping_key 唯一)。
--
-- **这个回滚可能失败,且失败是正确行为。**
-- 两层模型下多个 incident 允许共享同一 grouping_key(它不再是唯一键),
-- 因此恢复 UNIQUE 约束时若库中已存在重复值,ALTER TABLE 会报错。
-- 这比静默丢数据好:遇到此情况需要先人工决定保留哪条 incident,例如
--   SELECT grouping_key, count(*) FROM incidents
--    GROUP BY grouping_key HAVING count(*) > 1;
-- 处理完重复后再重跑本回滚。

-- **归并不可逆**:up 把重复 correlation_key 的 incident 关闭并写了 superseded_by。
-- 回滚不会把它们改回 open —— 无法区分"被归并而关闭"与"本就该关闭"之外的语义,
-- 盲目改回 open 会让已处理完的故障重新出现在值班列表里。
-- 需要人工恢复时,先按指针查出受影响的记录再决定:
--   SELECT incident_id, superseded_by FROM incidents WHERE superseded_by IS NOT NULL;
-- 因此本回滚**先查询后删列**的顺序很重要:删掉 superseded_by 就再也查不到了。

DROP TABLE IF EXISTS alert_groups;

DROP INDEX IF EXISTS idx_incidents_correlation;
DROP INDEX IF EXISTS uniq_incidents_active_correlation;

ALTER TABLE incidents DROP COLUMN IF EXISTS correlation_key;
ALTER TABLE incidents DROP COLUMN IF EXISTS superseded_by;

-- 恢复原唯一约束(存在重复 grouping_key 时会失败,见上方说明)
ALTER TABLE incidents ADD CONSTRAINT incidents_grouping_key_key UNIQUE (grouping_key);
