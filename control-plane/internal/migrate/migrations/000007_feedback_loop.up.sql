-- =============================================================================
-- 000007 — 反馈闭环:人工反馈 → 待审 Golden Case → 审核入评测集
--
-- 背景:human_feedback 表从 000001 起就在收人工反馈,注释也写明"先进入审核队列,
-- 审核后才能成为 Golden Case",但**从来没有实现那条通路**。反馈被持久化后就躺在
-- 表里,既不回流为评测用例,也不改进 runbook —— 系统学不到任何东西。
-- 而读取端(evaluation/store.py)一直按 review_status='approved' 过滤,
-- 也就是说消费方早就准备好了,只缺生产方。
--
-- 本迁移做三件事:
--   1. 把 review_status 默认值从 'approved' 改为 'pending';
--   2. 补 provenance 列(来源、来自哪次调查/反馈),使"这条用例凭什么在评测集里"可追溯;
--   3. 加唯一约束防止同一次调查被反复提升为多条用例。
-- =============================================================================

-- 1) 默认值改为 pending。
--
-- 原默认 'approved' 与"先进审核队列"的设计意图相反:任何忘记显式设置的写入方
-- 都会让用例**未经审核直接进入评测集**。而评测集决定发布质量门槛 ——
-- 一条错误标注的用例会让门槛失真,且这种失真很难发现(门槛照常通过或照常失败,
-- 只是标准错了)。默认值应指向安全的一侧。
--
-- 既有种子数据显式写了 'approved'(shared/seed/002),不受影响。
ALTER TABLE golden_cases ALTER COLUMN review_status SET DEFAULT 'pending';

-- 2) Provenance:这条用例从哪来。
--
-- 没有它就无法回答"这条用例凭什么在评测集里"。人工标注的 golden case 是**资产**
-- (每条对应一次真实故障的复盘结论),而资产必须可追溯到人和时间,
-- 否则几个月后没人敢动它 —— 不知道删掉会不会丢掉重要的回归覆盖。
ALTER TABLE golden_cases ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'seed';
ALTER TABLE golden_cases ADD COLUMN IF NOT EXISTS investigation_id TEXT;
ALTER TABLE golden_cases ADD COLUMN IF NOT EXISTS promoted_by TEXT;
ALTER TABLE golden_cases ADD COLUMN IF NOT EXISTS reviewed_by TEXT;
ALTER TABLE golden_cases ADD COLUMN IF NOT EXISTS reviewed_at TIMESTAMPTZ;
ALTER TABLE golden_cases ADD COLUMN IF NOT EXISTS review_note TEXT;

-- 3) 同一次调查只能产出一条用例。
--
-- 反馈可以来多次(先 correct 再 confirm),但它们描述的是同一次故障。
-- 不加约束会让一次故障在评测集里占多个席位,等于给它加权 ——
-- 而评测集的意义在于覆盖多样的故障类型,不是重复计票。
-- 部分索引:investigation_id 为空的行(种子用例)不受约束。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_golden_cases_investigation
    ON golden_cases (investigation_id)
 WHERE investigation_id IS NOT NULL;

-- 待审队列按创建时间取,加索引避免全表扫。
CREATE INDEX IF NOT EXISTS idx_golden_cases_review
    ON golden_cases (review_status, created_at);
