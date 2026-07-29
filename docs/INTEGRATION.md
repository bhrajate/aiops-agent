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
