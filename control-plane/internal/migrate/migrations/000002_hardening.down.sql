-- 回滚 000002_hardening:删除幂等键、死信队列与 Golden Dataset 表。
--
-- 注意:golden_cases 是**人工标注资产**,重建成本高(每条对应一次真实故障的
-- 复盘结论)。生产回滚前应先导出:
--   \copy (SELECT * FROM golden_cases) TO 'golden_cases.csv' CSV HEADER

DROP TABLE IF EXISTS evaluation_runs;
DROP TABLE IF EXISTS golden_cases;
DROP TABLE IF EXISTS dead_letters;
DROP TABLE IF EXISTS idempotency_keys;
