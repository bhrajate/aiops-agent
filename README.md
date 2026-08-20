# AIOps Agent

> Kubernetes 故障根因分析(RCA)平台 —— 把 LLM 塞进一个**可靠、可验证、可停止**的调查系统,让 AI 在只读证据上推理,而不是自由发挥。

以 **Incident 驱动、证据优先、工作流主导、默认只读** 为核心路线:信号进来 → 确定性护栏触发 → Temporal 可恢复工作流编排调查 → 类型化只读工具收敛证据 → 带 Evidence ID 的结论 → 由值班人员最终闭环。

> 本仓库是对 [`生产级AIOps-Agent架构设计.md`](docs/生产级AIOps-Agent架构设计.md) 的工程实现。设计文档为架构基线,本 README 描述代码结构与运行方式。

## 核心理念

大模型不是拥有更多自由度,而是被放进一个**可靠、可验证、可停止**的调查系统:

```
Signal
  → Incident Manager        (去重 / 聚合 / 版本 / 幂等)
  → Deterministic Trigger   (确定性触发与停止条件,不交给 LLM)
  → Temporal Durable Workflow (可恢复、可重放的调查状态机)
  → Bounded Python RCA      (有界预算的 Planner / Analyzer / Synthesizer)
  → Typed Read-only Tools   (Tool Gateway 范围注入 + 脱敏 + 审计)
  → Evidence-backed Diagnosis (每个结论都有 Evidence ID)
  → Human Confirmation      (P1/P2 由值班人员最终负责)
```

## 仓库结构(monorepo)

| 目录 | 平面 | 语言 | 职责 |
|---|---|---|---|
| `shared/` | 契约 | SQL / JSON Schema | 冻结的数据契约:Signal / Incident / Investigation / Evidence / Hypothesis,及 PostgreSQL DDL |
| `control-plane/` | 基础设施控制平面 | Go | Signal Ingress、Incident Manager、Trigger Policy、API + Workbench 后端、Tool Gateway(逻辑分层清晰,当前为单体进程部署) |
| `ai-worker/` | AI 推理平面 | Python | Temporal RCA Worker:Investigation Workflow、Planner、Analyzers、Synthesizer、Model Gateway、Knowledge Service |
| `cluster-agent/` | 集群数据平面 | Go | 每集群只读 Agent(pull 模式),暴露类型化只读工具 |
| `frontend/` | 产品 | React + TS + Vite | Incident-first 的值班台:总览 / 告警 / 调查 / 评测集 / 知识库 / 审计 |
| `deploy/` | 部署 | docker-compose | PostgreSQL+pgvector / Temporal / Redpanda / MinIO / Redis |
| `docs/` | 文档 | Markdown | 架构映射、运行手册、API 说明 |

## 快速开始

```bash
# 1. 拉起基础设施(PostgreSQL / Temporal / Redpanda / MinIO / Redis)
cd deploy && make up

# 2. 启动 Cluster Agent(只读工具服务,:9100)
cd ../cluster-agent && make run

# 3. 启动 Go 控制面(公共 API :8088 + 内部 API :8090 + Ingress + Incident Manager + Tool Gateway)
cd ../control-plane && make run

# 4. 启动 Python RCA Worker(连 Temporal)
cd ../ai-worker && make install && make run

# 5. 启动前端 Workbench(:5173)
cd ../frontend && npm install && npm run dev
```

> 注:公共 API 默认端口为 **8088**(本机 8080 常被占用);Redis 宿主端口 **6380**。详见 [`docs/INTEGRATION.md`](docs/INTEGRATION.md)。

### 一键端到端验证

```bash
# 基础设施 make up 后,直接跑编排脚本(自动启动 agent/control-plane/worker、注入信号、验证全链路)
bash scripts/e2e.sh
```

该脚本已验证:Signal → Incident 聚合 → Temporal 调查状态机(triage→plan→collect→synthesize→conclude)
→ 7 条证据 + 排序假设 + resolved 诊断(`remediation_proposal=null`)→ 人工 confirm/close 闭环 → 完整审计。

详见各子目录的 README、[`docs/RUNBOOK.md`](docs/RUNBOOK.md)、[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) 与 [`docs/ACCEPTANCE.md`](docs/ACCEPTANCE.md)。

## 生产化能力(已落地并端到端验证)

- **认证/授权**:OIDC/JWT(开发 hs256 签发)+ RBAC 角色 + ABAC 集群/命名空间范围,有效权限 = `用户 ∩ Agent ∩ Incident`。越权/幂等/webhook 共 14 项安全测试通过。
- **mTLS**:Tool Gateway ↔ Cluster Agent 双向 TLS(证书脚本 `deploy/certs/gen-certs.sh`)。
- **可靠性**:Idempotency-Key 落库、证据快照入 MinIO、Kafka 死信队列、webhook HMAC 签名、内部 API 共享密钥。
- **可观测性**:Prometheus `/metrics`(control-plane + cluster-agent)+ OTLP 追踪(Signal→Incident→Workflow→Activity→ToolGateway→Agent 统一 Trace)。
- **评测**:Golden Dataset 离线回放,质量门槛 Top-3 100% / 证据引用 100% / 幻觉 0% / P95<300s 全部 PASS(架构 §18.1)。
- **部署**:Kubernetes manifests + Helm chart(dev/prod values)+ GitHub Actions CI + cluster-agent 只读 ClusterRole(仅 get/list/watch)。
- **真实数据源**:cluster-agent 支持 mock(默认)/ live(client-go 只读 + Prometheus/Loki/Tempo)。

详见 [`docs/SECURITY.md`](docs/SECURITY.md)、[`docs/ACCEPTANCE.md`](docs/ACCEPTANCE.md)、[`deploy/DEPLOY.md`](deploy/DEPLOY.md)。

> **能力边界**:架构设计文档描述目标形态,部分主题(平面分离粒度、告警聚合深度、深度 RCA 上界、多集群路由、Agent 推拉形态)的实现边界与设计意图存在差距。逐项对照见 [`docs/ARCHITECTURE.md` 能力边界](docs/ARCHITECTURE.md#能力边界设计意图-vs-当前实现)。

## 设计约束(务必遵守)

- **默认只读**:所有生产工具均为只读(K8s 仅 Get/List),LLM 无任何生产写权限,`remediation_proposal` 恒为 null。
- **确定性护栏**:权限、预算、限流、触发与停止条件由确定性代码执行,不交给 LLM。
- **证据优先**:任何关键结论都必须引用可追溯 Evidence ID,允许返回"无法确定"。
- **故障隔离**:AIOps Agent 失效不影响原有监控与告警链路;Temporal/Kafka/Agent/S3 不可用时降级不崩溃。

## 许可证

见 [LICENSE](LICENSE)。
