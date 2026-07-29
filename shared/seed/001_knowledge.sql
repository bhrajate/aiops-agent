-- =============================================================================
-- 演示 / 开发 种子数据
--   * 一条 Knowledge Runbook,供 Knowledge Service RAG 检索。
--   * 说明:业务 Incident 由 Signal 流入后自动生成,此处不预置,避免与幂等键冲突。
-- =============================================================================

INSERT INTO knowledge_items (kind, title, content, applies_to, version)
VALUES
(
  'runbook',
  '发布回归导致依赖超时的排查手册',
  E'现象:某服务新版本发布后,下游依赖调用 P99 延迟显著上升,错误率仅在新版本实例上升。\n' ||
  E'排查步骤:\n' ||
  E'1. 按 version 维度拆分 metrics,确认异常是否集中在新版本 Pod。\n' ||
  E'2. 检查 ConfigMap / 连接池相关配置在本次发布是否变更。\n' ||
  E'3. 对比新旧版本的连接池等待时间、活跃连接数指标。\n' ||
  E'4. 查看下游依赖的 traces,定位排队/超时发生的具体 span。\n' ||
  E'常见根因:连接池大小、超时时间、重试策略配置回归。',
  '{"service":"*","environment":"production"}',
  'v1'
),
(
  'runbook',
  'Pod CrashLoopBackOff 处置手册',
  E'现象:Pod 反复重启,状态为 CrashLoopBackOff。\n' ||
  E'排查步骤:\n' ||
  E'1. get_kubernetes_events 查看最近事件(OOMKilled / 探针失败 / 镜像拉取失败)。\n' ||
  E'2. search_logs 查看容器启动日志中的 panic / fatal。\n' ||
  E'3. query_metrics 检查内存是否触及 limit(OOM)。\n' ||
  E'4. list_recent_changes 确认是否有镜像或配置变更。\n' ||
  E'常见根因:OOM、启动依赖未就绪、探针配置过紧、错误配置。',
  '{"service":"*","environment":"production"}',
  'v1'
),
(
  'runbook',
  '资源瓶颈(CPU/内存)排查手册',
  E'现象:服务响应变慢或被限流,CPU 节流(throttling)或内存接近 limit。\n' ||
  E'排查步骤:\n' ||
  E'1. query_metrics 查看 CPU throttled seconds、内存使用率。\n' ||
  E'2. 对比请求量(QPS)与资源使用,判断是否流量激增。\n' ||
  E'3. 检查是否有近期发布改变了资源 requests/limits。\n' ||
  E'常见根因:limit 设置过低、流量激增、内存泄漏、GC 压力。',
  '{"service":"*","environment":"production"}',
  'v1'
)
ON CONFLICT DO NOTHING;
