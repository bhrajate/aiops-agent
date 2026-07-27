# 架构到代码映射

把 [`生产级AIOps-Agent架构设计.md`](生产级AIOps-Agent架构设计.md) 的组件映射到本仓库实现,便于审阅与对照。

## 三平面

```
信号生产方 (Alertmanager / CI-CD / ITSM)
        │  POST /v1/signals
        ▼
┌─────────────────────────── 基础设施控制平面 (Go, control-plane/) ───────────────────────────┐
│  Signal Ingress      internal/api/ingress.go        鉴权·标准化·快速2xx·持久化+outbox        │
│  Event Bus           internal/bus + internal/outbox  Kafka(Redpanda)·至少一次·Outbox        │
│  Incident Manager    internal/incident               去重·聚合·版本·分类·生命周期            │
│  Trigger Policy       internal/trigger/policy.go      确定性触发/停止(不交给 LLM)            │
│  Orchestrator        internal/trigger/orchestrator    创建 Investigation·启动/信号 Workflow   │
│  Tool Gateway        internal/gateway                 白名单·范围注入·脱敏·审计·冻结 Evidence │
│  公共 API :8088       internal/api/public_api,sse     前端·webhook·SSE 时间线                 │
│  内部 API :8090       internal/api/internal_api       AI Worker 唯一回写入口(单一事实源)     │
│  持久化              internal/store (pgx)             PostgreSQL 业务库                        │
│  Temporal 客户端      internal/temporalx              启动/信号/取消·可降级                    │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
        │  Temporal (investigation-ai queue)              │ HTTP :9100 (Tool Gateway → Agent)
        ▼                                                 ▼
┌──────────── AI 推理平面 (Python, ai-worker/) ──────┐   ┌──── 集群数据平面 (Go, cluster-agent/) ────┐
│  InvestigationWorkflow  状态机(7.3/7.4)          │   │  只读 ServiceAccount 语义                  │
│  Activities: triage / plan / analyze / synth      │   │  类型化只读工具(9.1)                      │
│  Planner + 5 Analyzers + Synthesizer(第 8 节)    │   │  可插拔数据源(mock / client-go / Prom…)  │
│  Model Gateway(mock | anthropic,第 12 节)        │   └────────────────────────────────────────────┘
│  有界预算 / Pydantic 契约 / Prompt 注入防护         │
└─────────────────────────────────────────────────────┘

前端 Incident Workbench (React+TS, frontend/) ── 消费公共 API :8088,Incident-first(第 17 节)
```

## 关键数据流(与文档第 6、7 节一致)

1. **接入**:`POST /v1/signals` → `ingress.go` 归一化 → `signals` 表 + `outbox` → Kafka `signals`
2. **聚合**:`incident.Manager.HandleSignal` 消费 → `grouping_key` 去重/版本 → `incidents` 表 + Kafka `incidents`
3. **触发**:`trigger.Orchestrator.HandleIncidentEvent` 消费 → `EvaluateAuto`/`StopReason` → 创建 `investigations` → `temporalx.Start`
4. **调查**:Python `InvestigationWorkflow` 跑状态机,经内部 API 拉上下文、经 Tool Gateway 取 Evidence、回写 phase/events/hypotheses/diagnosis
5. **呈现**:前端轮询 + SSE `GET /v1/investigations/{id}/events` 实时展示;人工反馈 `POST .../feedback`

## 事实源与状态

- **业务库(PostgreSQL)= 事实源**:Incident/Investigation/Evidence/Hypothesis/审计/反馈。
- **Temporal = 可靠执行**:仅编排,不做第二事实源。
- **AI Worker 不直连 DB**:所有写入经内部 API :8090,保证单一写入路径。
- **对象存储(MinIO)**:证据快照/报告(raw_ref 引用)。

## 安全边界(第 14 节)

- LLM 永不接触 K8s Token / DB 密码 / 观测凭据。
- 工具全只读,白名单硬编码;`remediation_proposal` 强制 null。
- 进入模型的证据经脱敏(`gateway/redact.go`)。
- 工具结果作为数据,不作为指令;模型输出过结构化 schema 校验。

## 能力边界(设计意图 vs 当前实现)

架构设计文档描述的是**目标形态**;下表说明当前代码的实际边界,避免把设计意图读成已实现能力。

| 主题 | 设计文档表述 | 当前实现 | 差距性质 |
|---|---|---|---|
| **平面分离** | 三平面可独立部署 | ✅ **支持按角色拆分**:`AIOPS_ROLES`(api/internal/ingest/trigger/outbox,默认 all)控制本进程启用哪些子系统;Helm `controlPlane.splitRoles=true` 渲染独立的 API 副本(api,internal)与后台管道副本(ingest,trigger,outbox),可独立扩缩、后台故障不影响 API。默认仍为单体(向后兼容) | 已闭合 |
| **告警聚合** | 去重 + 相关告警聚合 + 拓扑关联 | ✅ **两层模型已实现**:`alert_groups`(去重单元,按 grouping_key 收敛同资源同规则重复告警)+ `incidents`(相关性单元,按 correlation_key = tenant/cluster/namespace 合并多个 group)。incident 的 affected_resources / blast_radius / severity / signal_count 由其下活跃 group 聚合得出;单个 group 恢复只关该 group,全部恢复才关 incident。**拓扑关联仍未实现**(见下一行) | 去重/聚合已分离;拓扑维度待补 |
| **相关性含义** | 服务拓扑上下游关联 | 相关性维度为 **tenant/cluster/namespace**(非拓扑)。`TopologyRefs`/`ChangeRefs` 仍为空,无写入点。**同 namespace 相关 ≠ 因果**;跨 namespace 的上下游传播不会被合并 | 缺拓扑数据源 |
| **深度 RCA** | Planner + 并行 Analyzer 深度调查 | **计划先定、逐个执行**:Planner 先产出工具清单,采集期模型不参与,要追问需等下一轮。非 native tool-use。取舍换来可重放性与可预测预算,但**能力上界低于"自由追问式"RCA**。<br>✅ **模型现可参数化查询**(F1):`AnalyzerSpec.queries` 让 Planner 指定具体 PromQL / LogQL / service,不再每次都跑后端默认表达式。此前 `objective` 是死字段——计划怎么写都不影响采集什么数据,LLM 实际只是给一块固定看板写摘要 | 取舍仍在(不能中途追问);但"这一轮问什么"已可表达 |
| **结论必须有据** | Evidence-first,关键结论必须引用证据 | ✅ **运行时强制**(F2):`policy.enforce_evidence_grounding` 在**持久化之前**把"声称 supported 却无实时证据引用"的假设确定性降级为 unresolved,业务库因此不存在无证据支撑的已确认根因。参考知识(runbook,`type=knowledge`)可启发假设但不能证明。降级计入 `usage.ungrounded_downgrades` 并发 `hypothesis_downgraded` 事件。此前该不变量**只在离线评测度量**,运行时只看模型自报 status | 已闭合 |
| **影响面口径** | blast_radius 反映影响面 | ✅ **拆为三个语义不同的维度**(F3):`services`(Pod 已归约到所属工作负载,驱动深度 RCA 闸门)/ `resources`(Pod 级)/ `groups`(去重单元数)。此前 `services` 数的是资源数且与 `groups` 同值——同一 Deployment 下 3 个 Pod 各自告警即 `services=3`,单服务故障被误判为影响面扩大并拉起多轮 RCA | 已闭合 |
| **数据保留** | — | ✅ **Janitor 分批清理**(F4):此前所有高写入表无界增长,无保留也无分区。只删终态数据,活跃 incident 与未结束调查的上下文永不触碰;多副本靠 PG advisory lock 互斥;保留期全部可配。未采用分区表(首版数据量下成本高于收益,阈值已记入迁移注释) | 已闭合 |
| **告警风暴防护** | — | ✅ **入口限流**(F6):按租户令牌桶,**按信号条数计费**(一个 webhook 可带数百条告警,按请求计费形同虚设),写库前判定,429 带 Retry-After。**权衡**:进程内实现,每副本独立配额 → 集群总容量 = 配置值 × 副本数;这是为了不给信号入口引入 Redis 这个新的必经故障点。需要全局精确配额时换实现即可,接口不变 | 已闭合(含权衡记录) |
| **多集群** | 每集群一个只读 Agent | ✅ 已实现:`AIOPS_CLUSTER_AGENTS`(cluster_id→URL 映射)+ Gateway 按 `incident.cluster_id` 路由;未配置的集群**拒绝**工具调用(`no_agent_for_cluster`)而非回退,避免跨集群误读。未配置映射时退化为单集群兼容模式 | 已闭合 |
| **Cluster Agent 形态** | 推拉结合(含主动上报 Signal) | **仅 pull**:被动 HTTP 工具服务,无主动上报、无 K8s Event watch。瞬时事件若超出查询时间窗或被 K8s 回收即不可得 | 功能未实现 |
| **证据时间窗** | 按需 | 由 `incident.first_seen` 推导(前置 15 分钟基线,上限 24h);模型不能自定义时间范围 | 已改进,仍非模型可控 |
| **观测后端隔离** | 每集群一套 或 中心共享 | ✅ **观测查询已迁至控制面直连**(`control-plane/internal/obsquery`):共享 Prometheus/Loki/Tempo 不在任何集群内,由 Tool Gateway 直接查询,不再绕经 cluster-agent(少一跳、少一个必经故障点、凭据一份)。守卫完整随迁:PromQL AST 级 label 强制注入(防裸选择器绕过)、LogQL 流选择器注入、`cluster`+`namespace` 双维度强制(`AIOPS_CLUSTER_LABEL`)、跨范围 matcher 拒绝、DNS-1123 校验、响应体上限、时间窗上限。cluster-agent 收窄为**纯 K8s 只读代理**。**权衡**:控制面因此持有观测后端凭据;若控制面威胁模型变化(暴露到更不可信网络),这是第一个应回退的决定 | 已闭合(含权衡记录) |

### 尚未闭合的结构性缺口

上表逐条对齐了设计文档;下面三项是**方向性**缺口,决定了本系统目前更接近
"AI 辅助 RCA",而非完整意义上的 AIOps 平台。它们不是没做完,是还没开始做:

| 缺口 | 现状 | 影响 |
|---|---|---|
| **拓扑 / 服务图相关性** | 相关性维度只有 tenant/cluster/namespace + 时间窗;`topology_refs` / `change_refs` 有列无写入点 | 跨 namespace 的上下游传播不会被合并——而依赖类故障恰恰常常跨 namespace。同 namespace 相关 ≠ 因果 |
| **反馈回流学习** | 人工反馈只落库;不生成 golden case、不更新 runbook,workflow 收到反馈即结束 | 系统不会因为被纠正而变好。长期价值取决于这个闭环 |
| **主动发现(异常检测 / SLO burn-rate)** | 纯被动,只消费别人配好的告警 | 覆盖面受既有告警规则限制;没有告警规则的故障不可见 |

另有两个已定位、修法明确但本轮未做的缺陷,以及成效指标缺位,详见
[OPTIMIZATION-LOG.md](OPTIMIZATION-LOG.md)。

生产验收的逐项落地状态见 [ACCEPTANCE.md](ACCEPTANCE.md);
本轮各项改动的**背景与权衡理由**见 [OPTIMIZATION-LOG.md](OPTIMIZATION-LOG.md)。
