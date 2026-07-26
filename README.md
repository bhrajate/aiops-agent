# AIOps Agent

生产级 AIOps Agent 系统 —— 以 **Incident 驱动、证据优先、工作流主导、默认只读** 为核心路线的 Kubernetes 故障根因分析(RCA)平台。

> 本仓库是对 [`生产级AIOps-Agent架构设计.md`](生产级AIOps-Agent架构设计.md) 的工程实现。设计文档为架构基线,本 README 描述代码结构与运行方式。

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
| `control-plane/` | 基础设施控制平面 | Go | Signal Ingress、Incident Manager、Trigger Policy、API + Workbench 后端、Tool Gateway |
| `ai-worker/` | AI 推理平面 | Python | Temporal RCA Worker:Investigation Workflow、Planner、Analyzers、Synthesizer、Model Gateway、Knowledge Service |
| `cluster-agent/` | 集群数据平面 | Go | 每集群只读 Agent,暴露类型化只读工具 |
| `frontend/` | 产品 | React + TS + Vite | Incident-first 的 Incident Workbench |
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

## 设计约束(务必遵守)

- **默认只读**:首版所有生产工具均为只读,LLM 无任何生产写权限。
- **确定性护栏**:权限、预算、限流、触发与停止条件由确定性代码执行,不交给 LLM。
- **证据优先**:任何关键结论都必须引用可追溯 Evidence ID,允许返回"无法确定"。
- **故障隔离**:AIOps Agent 失效不影响原有监控与告警链路。

## 许可证

见 [LICENSE](LICENSE)。
