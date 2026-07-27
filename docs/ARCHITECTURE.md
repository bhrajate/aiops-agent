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
| **深度 RCA** | Planner + 并行 Analyzer 深度调查 | **计划先定、逐个执行**:Planner 先产出工具清单,采集期模型不参与,要追问需等下一轮。非 native tool-use。取舍换来可重放性与可预测预算,但**能力上界低于"自由追问式"RCA** | 有意取舍,上界真实存在 |
| **多集群** | 每集群一个只读 Agent | ✅ 已实现:`AIOPS_CLUSTER_AGENTS`(cluster_id→URL 映射)+ Gateway 按 `incident.cluster_id` 路由;未配置的集群**拒绝**工具调用(`no_agent_for_cluster`)而非回退,避免跨集群误读。未配置映射时退化为单集群兼容模式 | 已闭合 |
| **Cluster Agent 形态** | 推拉结合(含主动上报 Signal) | **仅 pull**:被动 HTTP 工具服务,无主动上报、无 K8s Event watch。瞬时事件若超出查询时间窗或被 K8s 回收即不可得 | 功能未实现 |
| **证据时间窗** | 按需 | 由 `incident.first_seen` 推导(前置 15 分钟基线,上限 24h);模型不能自定义时间范围 | 已改进,仍非模型可控 |
| **观测后端隔离** | 每集群一套 | Prometheus/Loki 查询强制注入 `namespace`,**未注入 `cluster` label**。共享后端(Thanos/Mimir)场景下,不同集群同名 namespace 会混淆 | 共享后端场景隔离不完整 |

生产验收的逐项落地状态见 [ACCEPTANCE.md](ACCEPTANCE.md)。
