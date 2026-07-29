-- =============================================================================
-- 000005 — 为 knowledge_items 补自然键唯一约束
--
-- 起因:shared/seed/001_knowledge.sql 写了 ON CONFLICT DO NOTHING,但它**永远不
-- 触发** —— knowledge_id 默认 gen_random_uuid(),每次执行都生成新主键,没有任何
-- 约束可冲突。实测连跑两遍,knowledge_items 从 3 行变 6 行。
--
-- 为什么这是 schema 问题而不是脚本问题:同一租户下标题重复的 runbook 本身就是
-- 数据错误。重复 runbook 会被 RAG 反复检索、挤占 context 预算,并让同一份知识在
-- 证据里出现多次,看起来像"多个独立来源支持同一结论"。约束放在库上,任何写入
-- 路径都受约束,而不是指望每个调用方自己记得去重。
--
-- 选 (tenant_id, kind, title) 而非仅 title:不同 kind(runbook / postmortem)
-- 允许同名;多租户下各租户的知识库互不干扰。
--
-- 与 000003 同一个坑:任何**已经重复跑过种子**的库都存在重复标题,直接建唯一索引
-- 必然失败。所以先去重再建索引。保留 created_at 最早的一条(最初写入的那份),
-- 删掉后来重复插入的副本 —— 这里可以安全删除:重复行是脚本 bug 的产物,内容完全
-- 相同,不像 incident 那样承载独立的业务语义。
DELETE FROM knowledge_items k
 WHERE EXISTS (
     SELECT 1 FROM knowledge_items keep
      WHERE keep.tenant_id = k.tenant_id
        AND keep.kind      = k.kind
        AND keep.title     = k.title
        AND (keep.created_at, keep.knowledge_id) < (k.created_at, k.knowledge_id)
 );

CREATE UNIQUE INDEX IF NOT EXISTS uniq_knowledge_natural_key
    ON knowledge_items (tenant_id, kind, title);
