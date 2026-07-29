-- 回滚 000005:删除自然键唯一索引。纯索引回滚,无数据风险。
-- 回滚后重复标题的 runbook 又能写入,种子脚本的 ON CONFLICT 会重新变成空操作。

DROP INDEX IF EXISTS uniq_knowledge_natural_key;
