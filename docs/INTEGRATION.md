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
POST /v1/auth/login                              # 仅 hs256 开发模式;生产用 IdP
GET  /v1/auth/me
POST /v1/signals                                 # Signal Ingress,快速 2xx(webhook HMAC 鉴权)
GET  /v1/overview?hours=24                        # 值班首屏聚合(见下)
GET  /v1/incidents?status=&severity=&limit=
GET  /v1/incidents/{incident_id}                  # 附带 investigations / alert_groups / relations
POST /v1/incidents/{incident_id}/investigations  # Header: Idempotency-Key
POST /v1/incidents/{incident_id}/status           # { status: acknowledged | resolved }
GET  /v1/investigations?phase=&active=&limit=     # 跨 incident 的调查队列
GET  /v1/investigations/{investigation_id}
GET  /v1/investigations/{investigation_id}/events   # SSE (text/event-stream)
POST /v1/investigations/{investigation_id}/cancel
POST /v1/investigations/{investigation_id}/feedback # { author, action, confirmed_root_cause?, comment? }
GET  /v1/evidence/{evidence_id}
GET  /v1/knowledge?q=                             # 知识库检索(与 Agent 做 RAG 同一入口)
GET  /v1/golden-cases?status=pending&limit=       # 待审评测用例(sre/admin)
POST /v1/golden-cases/{case_id}/review            # { status: approved|rejected, note? }
GET  /v1/audit?actor=&action=&result=&hours=&limit=&before_id=   # 审计日志(sre/admin)
GET  /healthz                                     # liveness,恒 200
GET  /readyz                                      # readiness,DB 不可用返 503
```

统一响应错误体:`{ "error": { "code":"...", "message":"..." } }`。

### RBAC 与端点的对应

有效权限 = 用户 ∩ Agent 服务身份 ∩ Incident 范围(详见 `docs/SECURITY.md`)。

| 动作 | viewer | oncall | sre | admin | 端点 |
|---|---|---|---|---|---|
| `read_incident` / `read_evidence` | ✓ | ✓ | ✓ | ✓ | incidents / investigations / evidence / overview |
| `start_investigation` / `cancel_investigation` / `feedback` | | ✓ | ✓ | ✓ | investigations / cancel / feedback |
| `update_incident` | | ✓ | ✓ | ✓ | `POST /v1/incidents/{id}/status` |
| `review_golden_case` | | | ✓ | ✓ | golden-cases |
| `read_audit` | | | ✓ | ✓ | `GET /v1/audit`、overview 的 `queue` 字段 |

`update_incident` 给 oncall 起:认领是值班动作的起点,"这个我在看了"必须能被别人看到,
否则同一个 P1 会有三个人同时开始排查。viewer 不给 —— 只读角色改状态会让"谁在负责"
这个信息失真,而值班交接完全依赖它。

`read_audit` 只给 sre/admin:审计日志跨 incident、跨命名空间,且含**被拒绝访问**的
目标 ID 与 scope,那本身是敏感信息(能推断出存在哪些资源)。ABAC 在这里无法逐行过滤
(记录的是"谁试图访问什么",不都挂在 incident 上),所以用 RBAC 收紧到少数人,
而不是给一个过滤不干净的接口。

### `GET /v1/overview` 的口径

一个请求返回整块首屏。**刻意不拆成八个端点**:首屏若并发八个请求,任意一个失败会让
页面呈现*部分真实*的状态 —— "P1: 2"是真的但"进行中调查: 0"是加载失败的默认值,
而值班人员没法分辨哪个数字是坏的。一个端点要么整块成功要么整块报错。

聚合口径与 `GET /v1/incidents` **严格一致**:ABAC 逐行过滤后累加,共用
`auth.EffectiveAccess`。两处不一致会让总览说有 3 个 P1、点进列表只有 1 个,
这种矛盾一旦出现,面板就再不会被信任。也因此不能让前端拿 incident 列表现算:
列表有 limit(默认 50、上限 500),算出来的是"最近 50 条里的 P1 数"。

几个字段的语义值得写明:

- `open_total` / `open_p1` / `open_p2` / `unacknowledged` **不受 `hours` 约束**。
  一个三天前爆发、至今没人处理的 P1 必须出现在值班总览上 —— 按 24h 窗口过滤掉它,
  恰好隐藏了最该被看见的那类故障。窗口只约束分布、趋势与耗时统计。
- `mttr_seconds` / `p95_investigation_seconds` 样本不足时为 **`null`**,不是 0。
  0 会被读成"秒级解决",而真相是"没有已解决的样本"。前端须显示破折号。
  伴随的 `*_sample_size` 给出样本量。
- `stalled_investigations` 用**挂钟时间**判定(启动至今 > 10 分钟且仍非终态),
  不用 `usage.elapsed_sec` —— 后者由 worker 上报,worker 挂了它就不再更新,
  而那正是要检测的情况。
- `queue` 为 **`null`** 表示查询失败或调用方无 `read_audit` 权限。
  **绝不填 0**:0 会被读成"队列是空的",恰好掩盖 outbox 卡死这类静默失败。
  `queue.health` 按最老待投递记录的**年龄**判定(lagging ≥60s、stuck ≥300s 或有
  `status='dead'`),不按条数:告警风暴下积压几千条但几秒排空是正常的,只积压 3 条
  却卡了 20 分钟才是故障。按条数判定会在风暴时误报、在真卡住时漏报。
- `by_severity` 按 P1→P4 定序,其余分布按计数降序、同数按键名。**前端不要再排序**
  —— 计数序会把 P4 排在 P1 前面,而值班台第一眼要看的是有没有 P1。
- `by_feedback` 按动作分维度返回,**不折算成采纳率**:比率会丢掉分子分母,而
  "低采纳率"与"根本没人给反馈"是完全不同的问题,处置方式相反。

### `POST /v1/incidents/{id}/status` 的约束

只接受 `acknowledged` 与 `resolved`:

- `open` 是 Signal 写入的初始态,不允许人工退回 —— 会让 `first_seen` 与状态历史矛盾,
  而值班交接依赖它。
- `closed` 走调查反馈的 `close` 动作。关闭意味着"这次调查的结论我认了",必须与反馈
  一起记录,否则评测集拿不到标注真值。
- 已 `closed` 的 incident 拒绝变更(409)。重新打开应当是一次新的 Signal 聚合
  (它会带新的 `first_seen`),而不是把旧记录改回去。
- 目标态与当前态相同时**幂等返回** `{changed:false}` 而非 409:两个值班人员同时点
  "认领"是常态,第二个人看到 409 会以为自己操作失败。

### `GET /v1/audit` 的翻页

用 `before_id` 游标而非 OFFSET:审计表持续写入,OFFSET 翻页会在新记录插入时漏行或
重复行,而这是问责依据,不能有"翻页时少了一条"。响应的 `next_cursor` 为 0 表示没有
更多;`action_counts` 按 `(action, result)` 聚合,`denied` 单独可见才有意义 ——
"有 40 次权限拒绝"是安全信号,混在 action 总数里就看不见了。

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

> **上生产前必须显式设置的两项:`AIOPS_ENV` 与 `AIOPS_DATASOURCE`。**
> 二者的默认值(`development` / `mock`)都是为本地零依赖开发准备的,
> 漏配不会报错、日志无异常、指标正常 —— 只有安全护栏不生效、证据是编造的。
> 校验:`bash scripts/check-prod-guards.sh`。

```
# 运行环境:**所有生产护栏的总开关**,不是日志标签。
#   production | prod → 执行严格启动校验;其余值(含缺省)→ 全部跳过。
# 严格校验覆盖:auth 不得 disabled、HS256 密钥不得为默认值/短于 32 字节、
# 必须有 internal token 与 webhook secret、不得用 mock 观测数据源、
# 必须配至少一个观测后端、必须配集群维度隔离。
#
# ⚠ 缺省是 development。生产漏配这一项的后果不是"少了个标签",而是上面每一条
#   都变成静默放行:配错不再启动失败,而是带着弱配置正常跑起来。
AIOPS_ENV=development                # 生产必须显式设为 production

# cluster-agent 的 K8s 只读数据源:live(client-go)| mock(确定性假数据)。
# ⚠ 缺省是 mock。mock 会让 get_workload_state / get_kubernetes_events /
#   list_recent_changes / inspect_dependencies 返回**虚构但自洽**的故障数据,
#   它们照常被冻结成 Evidence、拿到 Evidence ID、进入诊断结论 ——
#   evidence-grounding 只校验"结论是否引用了证据",不校验证据是否真实。
#   于是值班人员看到一份"有据可查"的根因,底下全是编造的。
#   AIOPS_ENV=production 下 mock 会被拒绝启动(cluster-agent 侧 fail-fast)。
AIOPS_DATASOURCE=mock                # 生产必须设为 live

# 控制面的观测数据源。留空时:配了任一后端 URL → live;都没配 → **静默回退 mock**。
# 生产下显式 mock 或未配任何后端都会被启动校验拒绝。
AIOPS_OBS_DATASOURCE=                 # 留空 | mock | live

AIOPS_DB_DSN=postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable
AIOPS_KAFKA_BROKERS=localhost:19092
AIOPS_TEMPORAL_HOSTPORT=localhost:7233
AIOPS_TEMPORAL_NAMESPACE=default
# 单个 Workflow run 的墙钟硬上限(默认 7 天)。**不是**调查时长上限 ——
# 那由 Budget.max_duration_sec(默认 300s)管。这一项只兜「run 异常挂死」。
#
# 下限 216000 秒(60h),控制面启动时校验,配小了直接拒绝启动:
# run timeout 到点是服务端**硬终止**,不执行 CLOSED 迁移、不落账用量。
# Worker 最长等待人工反馈 48h,若 run timeout 小于它,一条正在**正常等待人工**
# 的调查会被掐掉,库里永久停在 waiting_feedback 且用量永不落账 ——
# 那比完全不设 run timeout 更糟。60h = 48h + 12h 收尾余量。
AIOPS_TEMPORAL_RUN_TIMEOUT_SEC=604800
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
# 模型 provider。⚠ 缺省是 mock —— MockProvider 返回的是**编造的**假设与诊断结论:
# 不报错、不超时、schema 完全合法,一路写进 incident 的诊断里,值班人员没有任何
# 线索能看出这份根因不是模型分析出来的。AIOPS_ENV=production 下 mock 会被拒绝启动,
# 拼错的 provider 名也会(此前只拦 mock,拼错要等到 build_provider 才抛)。
#
#   mock        零依赖确定性假数据,仅非生产
#   anthropic   手写结构化输出管线(JSON 文本 -> 解析 -> 修复重问 -> 兜底)
#   pydantic-ai 同一个模型与密钥,结构化输出交给 pydantic-ai 的 output_type
#               (tool-calling,schema 在采样层约束)。需装 [pydantic-ai] extra
#
# 后两者共用同一份提示词与系统指令,差异只在结构化输出如何取得 —— 这样才能在真实
# 流量上比较,而不是靠推断。两者的失败处置也一致:绝不抛异常,返回低置信度兜底
# 让工作流升级到 needs_human。
AIOPS_MODEL_PROVIDER=mock            # mock | anthropic | pydantic-ai;生产必须后两者之一
AIOPS_ANTHROPIC_API_KEY=             # 切 anthropic 时填
AIOPS_ANTHROPIC_MODEL=claude-opus-4-8[1M]
AIOPS_HTTP_TIMEOUT_SEC=15            # 内部 API 单次往返超时
# Worker 并发 activity 上限,同时也是**并发模型调用**上限:一轮最多 5 个分析器
# 并行,多条调查叠加时这是唯一的闸门。
#
# 为什么设在 Worker 层而不是工作流里用 semaphore:被这个上限挡住的 activity
# 处于「未开始」状态,start_to_close 计时还没启动;semaphore 是在 activity
# **内部**等待,计时已经在跑 —— 排队久了会把正常任务拖成超时。
#
# 不要配太低:record_phase / record_event 这类记账 activity 与模型 activity
# 共享槽位,被长时间占满会撞上它们 30s 的超时并触发重试。
AIOPS_MAX_CONCURRENT_ACTIVITIES=16
```

### Activity 超时与心跳

| 类别 | start_to_close | heartbeat | 说明 |
|---|---|---|---|
| 模型类(triage / plan / analyze / synthesize) | 180s | 30s | 见下 |
| 记账类(record_phase / record_event / record_usage) | 30s | — | 一次内部 HTTP 写入 |

模型类取 180s 的依据:`run_analyzer` 是**串行**工具调用后再接一次模型调用 ——
单个工具往返最长 `AIOPS_HTTP_TIMEOUT_SEC`(15s),一个分析器最多 2 个工具即 30s,
之后 reasoning 模型本身可能再花 60s+。原先的 90s 会把一次**正常但缓慢**的分析
判成超时并重试,那才是真正白烧三倍 token 的路径。这不会让调查跑更久:
`Budget.max_duration_sec`(默认 300s)会先兜住。

心跳窗口 30s、activity 侧每 5s 心跳一次(6 倍余量,吸收 GC 抖动与 SDK 心跳节流)。
作用是把「worker 被 OOM kill」的发现时间从最长 180s 压到 30s 量级。

模型调用是**单次不可分割的 await**,中途没有天然落点可以心跳,因此用
`heartbeat_while` 起并发任务覆盖整个等待期。这一点是必须的:设了
`heartbeat_timeout` 却没人按时心跳,Temporal 会把正常推理判成失联并重试 ——
比不设心跳更糟。
