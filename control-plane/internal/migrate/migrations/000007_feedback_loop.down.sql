-- 回滚 000007:恢复 review_status 默认值,删除 provenance 列与索引。
--
-- **注意**:删掉 provenance 列会丢失"这条用例从哪来、谁审核的"这些信息,
-- 而那是不可重建的(反馈与审核动作本身在 human_feedback / audit_log 里还有,
-- 但用例与它们的对应关系只存在这些列里)。回滚前应先导出:
--   \copy (SELECT case_id, source, investigation_id, promoted_by, reviewed_by,
--                 reviewed_at, review_note FROM golden_cases)
--     TO 'golden_provenance.csv' CSV HEADER
--
-- 由自动提升产生的 pending 用例本身不会被删除,只是失去来源标记。
-- 若要一并清掉它们(回退到只有种子用例的状态),需人工执行:
--   DELETE FROM golden_cases WHERE source = 'human_feedback';
-- 这一步刻意不放进回滚:那些用例可能已被审核通过并投入使用。

DROP INDEX IF EXISTS idx_golden_cases_review;
DROP INDEX IF EXISTS uniq_golden_cases_investigation;

ALTER TABLE golden_cases DROP COLUMN IF EXISTS review_note;
ALTER TABLE golden_cases DROP COLUMN IF EXISTS reviewed_at;
ALTER TABLE golden_cases DROP COLUMN IF EXISTS reviewed_by;
ALTER TABLE golden_cases DROP COLUMN IF EXISTS promoted_by;
ALTER TABLE golden_cases DROP COLUMN IF EXISTS investigation_id;
ALTER TABLE golden_cases DROP COLUMN IF EXISTS source;

ALTER TABLE golden_cases ALTER COLUMN review_status SET DEFAULT 'approved';
