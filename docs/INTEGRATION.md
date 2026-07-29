# 集成契约(Integration Contract)

所有组件据此对齐。**这是冻结契约,修改需同步所有模块。**

## 端口分配

| 组件 | 端口 | 协议 | 说明 |
|---|---|---|---|
| control-plane 公共 API | `8088` | HTTP/JSON + SSE | 前端 + 外部 webhook 消费 |
| control-plane 内部 API | `8090` | HTTP/JSON | Tool Gateway + AI Worker 回写(仅集群内) |
| cluster-agent | `9100` | HTTP/JSON | Tool Gateway 调用(首版免 mTLS,预留开关) |
| ai-worker | 无入站 | — | Temporal Worker,主动连出 |
| frontend (dev) | `5173` | HTTP | Vite,`/v1` 代理到 `8088` |
| PostgreSQL | `5432` | — | 业务库 `aiops` |
| Temporal | `7233` (gRPC) / `8233` (UI) | — | namespace `default` |
| Redpanda (Kafka) | `19092` | Kafka | topics: `signals` `incidents` `investigations` |
| MinIO | `9000` / `9001` | S3 | bucket `aiops-evidence` |
| Redis | `6379` | — | 限流/缓存(可选) |

## Temporal 约定

- namespace: `default`
- task queue: `investigation-ai`
- workflow type name(跨语言字符串): `InvestigationWorkflow`
- workflow id: `investigation/{incident_id}/{version}`
- 启动参数(单个 JSON 对象):
  ```json
  { "investigation_id": "...", "incident_id": "...", "incident_version": 1,
    "tenant_id": "default", "cluster_id": "...",
    "budget": { "max_duration_sec": 300, "max_rounds": 3, "max_tokens": 200000, "max_cost_usd": 2.0, "max_tool_calls": 20 },
    "control_internal_url": "http://localhost:8090" }
  ```
- Signals: `IncidentUpdated` `IncidentResolved` `HumanFeedback` `Cancel`
- Go 控制面用 Go SDK `client.ExecuteWorkflow(..., "InvestigationWorkflow", arg)` 启动;
  Python Worker 注册同名 workflow。默认 JSON payload converter 跨语言兼容。

## 内部 API(control-plane `:8090`)—— 单一 DB 写入方

AI Worker **不直连数据库**,通过以下接口回写(保证业务库为唯一事实源)。

```
POST /internal/tools/invoke
  { "investigation_id","incident_id","tool","arguments","scope"? }
  → 200 { "status":"ok","evidence": <Evidence> }
      | { "status":"denied","reason":"..." }
  # Gateway: 范围注入+校验 → 调 cluster-agent → 脱敏 → 持久化 Evidence → 审计 → 返回 Evidence

GET  /internal/investigations/{id}/context
  → { "incident": <Incident>, "signals":[...], "topology":[...], "changes":[...] }

POST /internal/investigations/{id}/phase       { "phase":"planning" }
POST /internal/investigations/{id}/events      { "event_type":"...","payload":{...} }
POST /internal/investigations/{id}/hypotheses  { "hypotheses":[ <Hypothesis>... ] }   # 全量替换
POST /internal/investigations/{id}/diagnosis   { "diagnosis": <DiagnosisResult>, "phase":"concluded" }
POST /internal/investigations/{id}/usage       { "usage": {...} }
```

## 公共 API(control-plane `:8088`)—— 前端消费

```
POST /v1/signals                                 # Signal Ingress,快速 2xx
GET  /v1/incidents?status=&severity=&limit=
GET  /v1/incidents/{incident_id}
POST /v1/incidents/{incident_id}/investigations  # Header: Idempotency-Key
GET  /v1/investigations/{investigation_id}
GET  /v1/investigations/{investigation_id}/events   # SSE (text/event-stream)
POST /v1/investigations/{investigation_id}/cancel
POST /v1/investigations/{investigation_id}/feedback # { author, action, confirmed_root_cause?, comment? }
GET  /v1/evidence/{evidence_id}
GET  /v1/knowledge?q=                             # 知识库检索(可选)
GET  /healthz
```

统一响应错误体:`{ "error": { "code":"...", "message":"..." } }`。

## Cluster Agent 工具协议(gateway `:8090` → agent `:9100`)

```
GET  /healthz
GET  /tools                       # 返回工具清单与参数 schema
POST /tools/{tool_name}
  { "arguments": {...}, "scope": { "cluster_id","namespace","resource"?,"time_range"? } }
  → 200 { "source":"prometheus","summary":"...","raw":{...},"freshness":"10s" }
     | 4xx { "error":{...} }
```

工具集合(文档 9.1):
`get_workload_state get_kubernetes_events query_metrics search_logs get_traces list_recent_changes inspect_dependencies retrieve_runbook`

> `retrieve_runbook` 由 control-plane 直接查 Knowledge Service(pgvector),不经 cluster-agent。

## 环境变量(统一前缀 `AIOPS_`)

```
AIOPS_DB_DSN=postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable
AIOPS_KAFKA_BROKERS=localhost:19092
AIOPS_TEMPORAL_HOSTPORT=localhost:7233
AIOPS_TEMPORAL_NAMESPACE=default
AIOPS_S3_ENDPOINT=http://localhost:9000
AIOPS_S3_BUCKET=aiops-evidence
AIOPS_S3_ACCESS_KEY=minioadmin
AIOPS_S3_SECRET_KEY=minioadmin
# 角色拆分(物理平面分离):控制哪些子系统在本进程启用。
#   api / internal / ingest / trigger / outbox,或 all(默认全开=单体)
#   例:API 副本 AIOPS_ROLES=api,internal;后台副本 AIOPS_ROLES=ingest,trigger,outbox
AIOPS_ROLES=all
# 自动触发策略(F7)。此前一律触发 —— 每个 incident 都消耗一次分诊模型调用,
# 含 P4 单信号(磁盘到 80% 这类)。按每集群每天数千告警估算是持续的固定成本,
# 而其中相当一部分永远不会有人看诊断结论。
#
# 被跳过的 incident **仍然入库、仍在前端可见、仍可人工发起调查**(手动路径
# 不过这道闸门),且会写 trigger_skipped 审计与 aiops_trigger_decisions_total
# 指标 —— 所以"为什么这个故障没有诊断"能回答。跳过不留痕比不拦更糟。
#
# 调阈值前先看 reason 分布,它是唯一需要的信息:
#   sum by (reason) (aiops_trigger_decisions_total)
AIOPS_AUTO_TRIGGER_ALL=false                # true 完整回到旧行为(一律触发)
AIOPS_AUTO_TRIGGER_ALWAYS_SEVERITIES=P1,P2  # 无条件触发
# 可被跳过的级别(仍需其他判据均未命中)。默认只含 P4:P3 是最常见级别、
# 混着不少真问题,拦它会显著改变值班人员的预期。
AIOPS_AUTO_TRIGGER_SKIP_SEVERITIES=P4
AIOPS_AUTO_TRIGGER_BURST_SIGNALS=3          # 信号数达此值视为突发;0 关闭该判据
AIOPS_AUTO_TRIGGER_ON_CHANGE=true           # 变更关联必触发(最易自动定位的根因)

# ---- 服务依赖拓扑(拓扑关联)----
#
# 解决:相关性合并只按 tenant|cluster|namespace,调用链上的故障传播识别不了 ——
# checkout 挂了导致 payment-api 超时,值班人员看到两个互不相关的 incident,
# 而根因只有一个,得他们自己在脑子里把这两条连起来。
#
# 边的来源是 **Tempo service graph**:metrics-generator 从 trace 的父子 span 推导
# 真实调用关系,导出 traces_service_graph_request_total{client,server,connection_type}。
# 这些指标落在 Prometheus 里,控制面直接查 —— 不需要新基础设施,也不需要 cluster-agent。
#
# ⚠ **前置条件**:Tempo 必须启用 metrics-generator 的 service-graphs 处理器,
#   且其指标写入本控制面所连的那个 Prometheus。未启用时同步查到 0 条边并**告警一次**
#   (不重复刷日志),不影响任何既有路径 —— 但拓扑关联完全不生效,而那在别处
#   看不出任何异常:incident 照常创建、诊断照常产出,只是少了调用链上下文。
#   核对:
#     curl -s '<prom>/api/v1/query?query=count(traces_service_graph_request_total)'
#
# 为什么不用 Kubernetes Service selector:它只表达**入口**关系(哪个 Service 选中了
# 哪个工作负载),回答不了"checkout 调用了谁"。selector 边置信度只有 0.7,
# 够进 topology_refs 但不够链接 incident。
AIOPS_TOPOLOGY_ENABLED=true
AIOPS_TOPOLOGY_SYNC_SEC=300
AIOPS_TOPOLOGY_MAX_EDGE_AGE_SEC=3600     # 超此龄视为已下线,不参与关联
# 两级置信度是核心取舍:
#   MIN_CONFIDENCE      进 topology_refs —— 只是给 planner 更多上下文,错了代价小;
#   MIN_LINK_CONFIDENCE 链接 incident   —— 会出现在值班人员界面上,错了会误导排查方向。
# Tempo 真实调用边 0.9(够链接),K8s selector 边 0.7(只够进 refs)。
AIOPS_TOPOLOGY_MIN_CONFIDENCE=0.5
AIOPS_TOPOLOGY_MIN_LINK_CONFIDENCE=0.8
AIOPS_RETENTION_TOPOLOGY_DAYS=7          # 陈旧边清理;太短会在 Tempo 短暂不可用时误删
#
# 注:拓扑**不合并** incident,而是建立"疑似同源"链接(带方向,上游更可能是根因)。
# 合并的风险不对称:一条误判的边会把两次无关故障焊死成一个 incident,
# 而拆分比合并难得多 —— 已写入的 signal 与证据没法回滚归属。
# 关联结果在 GET /v1/incidents/{id} 的 relations 字段,也随 getContext 下发给 planner。

# ---- SLO 燃尽率监视(主动异常检测)----
#
# 解决:系统此前完全**被动**,只在告警流入时才有反应。没有告警规则覆盖的缓慢退化
# (错误率从 0.05% 爬到 0.4%,不触发任何静态阈值)完全看不见,直到变成用户投诉。
#
# 为什么是燃尽率而不是统计异常检测(3σ / 时序分解 / 孤立森林):
#   * 统计方法**不可解释** —— 能说"偏离基线 4.2σ",说不出"所以呢"。而本系统的产出
#     是给人看的诊断结论,一个无法解释的触发理由会污染整条推理链;
#   * 误报率高且难调(季节性、发布窗口、流量波动都会触发),而误报会训练值班人员
#     忽略告警 —— 比没有检测更糟;
#   * 与用户影响脱钩:偏离基线 ≠ 用户受损。燃尽率直接度量"错误预算消耗得多快"。
#
# 档位取自 SRE workbook 表 5-8(短窗为长窗的 1/12,用于确认"仍在燃烧"):
#   14.4× / 1h + 5m   → critical → P1(1 小时消耗 2% 月度预算)
#   6×    / 6h + 30m  → error    → P2(5%)
#   1×    / 3d + 6h   → warning  → P3(10%)
# 一次燃烧同时满足多档时**只报最严重的**,避免同一次故障发三条 signal。
#
# 越限后合成 signal 走**既有入口**,自动获得两层聚合 / 触发策略 / 幂等去重 / 审计。
AIOPS_SLO_ENABLED=false                  # 默认关闭:SLI 定义必须由你们给出
AIOPS_SLO_INTERVAL_SEC=60
# SLI 定义。二者择一,PATH 优先:
#   AIOPS_SLO_DEFINITIONS       inline JSON 数组
#   AIOPS_SLO_DEFINITIONS_PATH  文件路径(推荐:表达式含完整 PromQL,很长且
#                               容易被 shell 转义弄坏;K8s 下挂 ConfigMap)
# AIOPS_SLO_DEFINITIONS_PATH=/etc/aiops/slo/slis.json
#
# 表达式要求:
#   * 必须是**比率**(0..1),不是错误数;
#   * 必须含 $WINDOW 占位符,监视器按档位替换为 1h/5m/6h/30m/72h/6h。
#     ⚠ 缺占位符时窗口是写死的,多窗口退化成"同一窗口比两次",两个条件恒同真同假
#       —— 短窗过滤完全失效,而**表现上一切正常**(告警照常触发,只是不再防抖)。
#       启动时会因此报错拒绝启动。
#   * 有多处窗口时(分子分母各一处)全部会被替换 —— 只替换一处会让分子分母用不同
#     窗口,算出的比率毫无意义却仍是个数字,不会报错。
#
# 样例(按你们实际指标名改;Istio / Envoy / 自定义 recording rule 写法不同):
#   [{"name":"checkout-availability","namespace":"payment","service":"checkout",
#     "objective":0.999,
#     "error_ratio_expr":"sum(rate(http_requests_total{namespace=\"payment\",service=\"checkout\",code=~\"5..\"}[$WINDOW])) / sum(rate(http_requests_total{namespace=\"payment\",service=\"checkout\"}[$WINDOW]))"}]
#
# 任一条定义非法即**整体**拒绝启动:部分生效会让你以为都在监视,
# 而实际有一个 SLO 静默没在看。
# 观测后端集群维度 label 名——**按后端分别配置**。
#
# 三个后端对"集群"的命名法互不兼容:
#   Prometheus/Mimir : cluster            external_labels 注入;Grafana mixin
#                                         用 per_cluster_label 暴露为可配
#   Loki (Alloy)     : cluster            relabel 注入
#   Loki (OTLP 原生) : k8s_cluster_name   OTel resource attribute 提升为索引 label;
#                                         LogQL label 名须符合 Prometheus 命名法,
#                                         故点号转下划线
#   Tempo            : k8s.cluster.name   OTel 语义约定,原生保留点号
#
# 关键:**点号在 PromQL/LogQL 里是语法错误**,不只是"查不到数据"。所以单一值
# 无法同时满足三者——配 cluster 则 Tempo 静默查空;配 k8s.cluster.name 则
# Prometheus/Loki 每次查询都语法错。
#
# AIOPS_CLUSTER_LABEL 是全局回落值(向后兼容);下面三个按需覆盖。
AIOPS_CLUSTER_LABEL=
AIOPS_PROM_CLUSTER_LABEL=cluster
AIOPS_LOKI_CLUSTER_LABEL=cluster
AIOPS_TEMPO_CLUSTER_LABEL=k8s.cluster.name
# 仅当观测后端确为**本集群专用**时设 true。要求显式表态而非留空即关闭:
# 留空静默不隔离会让 RCA 读到其他集群同名 namespace 的数据,而这个错误在诊断
# 结论里看不出来——证据齐全、逻辑自洽,只是来自错误的集群。
AIOPS_CLUSTER_LABEL_DISABLED=false
#
# ⚠ 上线前必须对着**真实后端**核对这些名字。两类配错的表现完全不同:
#
#   (a) 名字合法但不匹配(实际是 cluster_id 却配了 cluster)
#       → 后端**不报错**,只静默返回空结果。最难排查的那类:RCA 跑完了,
#         证据一条也没采到,或全是降级提示,日志里没有任何异常。
#         代码里判定不了,只能人工核对:
#           Prometheus: curl -s <prom>/api/v1/labels | tr ',' '\n' | grep cluster
#           Loki:       curl -s <loki>/loki/api/v1/labels | tr ',' '\n' | grep cluster
#           Tempo:      取一条 trace,看 resource attributes 里集群字段实际叫什么
#                       curl -s <tempo>/api/traces/<trace_id> | grep -o 'k8s\.cluster[^"]*'
#         再确认该 label 的**取值**与 AIOPS_CLUSTER_ID 一致:
#           curl -s '<prom>/api/v1/query?query=count%20by%20(cluster)%20(up)'
#
#   (b) 语法非法(点号给了 Prometheus/Loki)
#       → 控制面**启动即拒绝**,并指名具体是哪个变量、该改成什么。
#         不会拖到运行期每次查询才失败。
# 共享可观测后端地址:由**控制面**直连(观测查询已从 cluster-agent 迁至控制面)。
# 未配置则 query_metrics / search_logs / get_traces 被拒绝(denied)。
AIOPS_PROM_URL=http://prometheus.observability.svc:9090
AIOPS_LOKI_URL=http://loki.observability.svc:3100
AIOPS_TEMPO_URL=http://tempo.observability.svc:3200
AIOPS_CLUSTER_AGENT_URL=http://localhost:9100   # 单集群兼容(未配置下面的映射时生效)
# 多集群:Tool Gateway 按 incident.cluster_id 路由到对应集群的 Agent。
# 未在映射中的集群一律拒绝工具调用(no_agent_for_cluster),不回退到其他 Agent。
AIOPS_CLUSTER_AGENTS=prod-cn-1=https://agent-cn1:9100,edge-eu-2=https://agent-eu2:9100
AIOPS_CONTROL_INTERNAL_URL=http://localhost:8090
AIOPS_MODEL_PROVIDER=mock            # mock | anthropic
AIOPS_ANTHROPIC_API_KEY=             # 切 anthropic 时填
AIOPS_ANTHROPIC_MODEL=claude-opus-4-8[1M]
```
