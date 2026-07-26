-- =============================================================================
-- 种子 Golden Cases(评测离线回放)——对应架构文档 18.2 与 ai-worker 评测服务。
--   * 与 aiops_worker/evaluation/seed_cases.py 保持一致(五条,覆盖四类故障)。
--   * signal_fixture 为可复现输入(incident stub + signals)。
--   * expected_top_causes 为期望命中的根因关键词。
-- 幂等:ON CONFLICT (case_id) DO NOTHING。
-- =============================================================================

INSERT INTO golden_cases
  (case_id, incident_id, fault_category, root_cause, affected_component,
   signal_fixture, expected_top_causes, review_status)
VALUES
(
  'gc-release-001', 'inc-release-001', 'release_regression',
  'checkout 新版本连接池配置回归导致依赖调用排队、5xx 上升', 'payment/checkout',
  '{"incident":{"incident_id":"inc-release-001","version":1,"status":"closed","severity":"P2","fault_category":"release_regression","affected_resources":[{"kind":"Deployment","name":"checkout","namespace":"payment"}],"blast_radius":{"services":2,"namespaces":1},"change_refs":["chg-inc-release-001"]},"signals":[{"signal_id":"s-r1","cluster_id":"prod-cn-1","source":"cicd","signal_type":"change","labels":{"event":"rollout","version":"v2.3.0"}},{"signal_id":"s-r2","cluster_id":"prod-cn-1","source":"alertmanager","signal_type":"alert","severity":"critical","labels":{"alertname":"HighErrorRate","release":"v2.3.0"}}]}'::jsonb,
  '["新版本","连接池","错误率"]'::jsonb, 'approved'
),
(
  'gc-resource-001', 'inc-resource-001', 'resource_saturation',
  '订单服务内存接近 limit 触发 OOMKill 与 CPU throttling', 'orders/order-api',
  '{"incident":{"incident_id":"inc-resource-001","version":1,"status":"closed","severity":"P2","fault_category":"resource_saturation","affected_resources":[{"kind":"Deployment","name":"order-api","namespace":"orders"}],"blast_radius":{"services":1,"namespaces":1},"change_refs":[]},"signals":[{"signal_id":"s-c1","cluster_id":"prod-cn-1","source":"alertmanager","signal_type":"alert","severity":"warning","labels":{"alertname":"CPUThrottlingHigh","resource":"cpu"}}]}'::jsonb,
  '["OOMKill","throttling","资源"]'::jsonb, 'approved'
),
(
  'gc-dependency-001', 'inc-dependency-001', 'dependency_failure',
  '下游支付网关超时导致上游 checkout 级联失败', 'payment/checkout',
  '{"incident":{"incident_id":"inc-dependency-001","version":1,"status":"closed","severity":"P1","fault_category":"dependency_failure","affected_resources":[{"kind":"Deployment","name":"checkout","namespace":"payment"}],"blast_radius":{"services":3,"namespaces":2},"change_refs":[]},"signals":[{"signal_id":"s-d1","cluster_id":"prod-cn-1","source":"alertmanager","signal_type":"alert","severity":"critical","labels":{"alertname":"DownstreamTimeout","dependency":"pay-gw"}}]}'::jsonb,
  '["依赖","超时","级联"]'::jsonb, 'approved'
),
(
  'gc-pod-001', 'inc-pod-001', 'config_error',
  '错误的 ConfigMap 连接串导致 Pod CrashLoopBackOff', 'catalog/catalog-svc',
  '{"incident":{"incident_id":"inc-pod-001","version":1,"status":"closed","severity":"P2","fault_category":"config_error","affected_resources":[{"kind":"Deployment","name":"catalog-svc","namespace":"catalog"}],"blast_radius":{"services":1,"namespaces":1},"change_refs":["chg-inc-pod-001"]},"signals":[{"signal_id":"s-p1","cluster_id":"prod-cn-1","source":"kubernetes","signal_type":"event","labels":{"reason":"CrashLoopBackOff","configmap":"catalog-cfg"}}]}'::jsonb,
  '["配置","连接串"]'::jsonb, 'approved'
),
(
  'gc-pod-oom-002', 'inc-pod-oom-002', 'resource_saturation',
  '内存泄漏导致 Pod 反复 OOMKilled', 'search/search-api',
  '{"incident":{"incident_id":"inc-pod-oom-002","version":1,"status":"closed","severity":"P2","affected_resources":[{"kind":"Deployment","name":"search-api","namespace":"search"}],"blast_radius":{"services":1,"namespaces":1}},"signals":[{"signal_id":"s-o1","cluster_id":"prod-cn-1","source":"kubernetes","signal_type":"event","labels":{"reason":"OOMKilled","alertname":"PodOOMKilled"}}]}'::jsonb,
  '["OOMKill","资源","重启"]'::jsonb, 'approved'
)
ON CONFLICT (case_id) DO NOTHING;
