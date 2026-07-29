-- 回滚 000006:删除拓扑与 incident 关联表。
--
-- 拓扑边是**可重建**的(周期性从 Tempo/K8s 同步),删掉只是下一轮同步前少一份缓存,
-- 无数据损失。incident_relations 同理:它是推导结果,不是事实源。
-- 因此这个回滚是安全的,不像 000003 那样含不可逆的归并。

DROP TABLE IF EXISTS incident_relations;
DROP TABLE IF EXISTS service_topology;
