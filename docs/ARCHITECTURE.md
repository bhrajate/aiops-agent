# 架构到代码映射

把 [`../生产级AIOps-Agent架构设计.md`](../生产级AIOps-Agent架构设计.md) 的组件映射到本仓库实现,便于审阅与对照。

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
