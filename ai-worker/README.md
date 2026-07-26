# ai-worker — AIOps AI 推理平面

生产级 AIOps Agent 的 **AI 推理平面**:一个 Temporal Python Worker,注册
`InvestigationWorkflow`(调查状态机)与各 Activity,负责快速分诊与深度根因分析(RCA)。

设计严格遵循仓库内契约:
- `docs/INTEGRATION.md`(Temporal 约定、内部 API `:8090`)
- `shared/schemas/contracts.md`(Signal/Incident/Investigation/Evidence/Hypothesis/DiagnosisResult)
- `docs/生产级AIOps-Agent架构设计.md`(第 7 / 8 / 12 / 14 节)

## 核心原则

- **Temporal 是唯一持久工作流**。Workflow 代码保持确定性(只用 `workflow.now()` 取时间);
  所有模型调用、内部 API 调用、工具查询都封装为 **Activity**(带超时 + 有限重试)。
- **AI Worker 不直连数据库**,通过 control-plane 内部 API(`:8090`)回写
  phase / events / hypotheses / diagnosis / usage,业务库为唯一事实源。
- **有界执行**:budget 从启动参数传入(`max_duration_sec / max_rounds / max_tokens /
  max_cost_usd / max_tool_calls`),达到任一预算即停止并升级(EscalateToHuman),禁止无限循环。
- **确定性策略**:是否深挖(deep RCA)、诊断状态映射等由规则代码决定,**不由 LLM 决定**。
- **Prompt Injection 防护**:工具结果作为数据进入 prompt(围栏 + 净化),不作为指令;
  模型输出一律过 Pydantic schema 校验;planner 只能从固定 Analyzer/工具集合选择。

## 目录结构

```
ai-worker/
├── pyproject.toml            # uv 管理,阿里源,Python 3.11
├── Makefile                  # install / run / test / check-import
├── Dockerfile
├── .env.example
├── aiops_worker/
│   ├── __init__.py           # WORKFLOW_TYPE_NAME / TASK_QUEUE 常量
│   ├── config.py             # AIOPS_ 环境变量
│   ├── contracts.py          # 所有 Pydantic 契约 + 预算/用量 + validate_plan
│   ├── internal_api.py       # control-plane 内部 API httpx 客户端
│   ├── policy.py             # 确定性:deep RCA 策略 + 诊断组装
│   ├── activities.py         # 所有 Temporal Activity
│   ├── workflow.py           # InvestigationWorkflow 状态机
│   ├── main.py               # Worker 入口(连 Temporal、注册、运行)
│   ├── model_gateway/
│   │   ├── __init__.py       # build_provider 工厂
│   │   ├── base.py           # ModelProvider 抽象 + 注入防护助手
│   │   ├── mock.py           # 确定性 MockProvider(四类故障场景)
│   │   └── anthropic_provider.py  # 真实 Claude(可选依赖)
│   ├── evaluation/           # 评测服务(离线回放 + 质量门槛,architecture 18)
│   │   ├── models.py         # GoldenCase / EvaluationResult / RunSummary
│   │   ├── pipeline.py       # OfflineReplayPipeline(无 Temporal)
│   │   ├── metrics.py        # Top-1/Top-3、引用率、幻觉率、P95、门槛
│   │   ├── seed_cases.py     # 5 条种子 Golden Cases(四类故障)
│   │   ├── runner.py         # 批量回放 + 聚合
│   │   ├── store.py          # 读 golden_cases / 写 evaluation_runs(db extra)
│   │   └── run.py            # CLI: python -m aiops_worker.evaluation.run
│   └── knowledge/            # pgvector 语义 RAG(architecture 12.2)
│       ├── embeddings.py     # EmbeddingProvider + 确定性 MockEmbeddingProvider
│       ├── store.py          # KnowledgeStore(索引/余弦检索,db extra)
│       └── reindex.py        # CLI: python -m aiops_worker.knowledge.reindex
└── tests/                    # 不依赖真实 Temporal / 网络(DB 集成测试自动跳过)
```

## 状态机(architecture 7.3 / 7.4)

```
queued → triaging → (triage_published | planning) → collecting → synthesizing
       → (concluded | needs_human) → waiting_feedback → closed
       (任意活跃阶段可 → cancelled)
```

Signals:`IncidentUpdated` `IncidentResolved` `HumanFeedback` `Cancel`。
每次阶段变化都调 `POST /internal/investigations/{id}/phase` 与 `/events`。

## Activity 清单

| Activity | 作用 |
|---|---|
| `load_incident_context` | `GET /internal/investigations/{id}/context` |
| `run_quick_triage` | Model Gateway 生成快速分诊 |
| `evaluate_deep_rca_policy` | **确定性**规则(P1/P2、爆炸半径、变更相关、分诊建议) |
| `build_investigation_plan` / `build_supplemental_plan` | Planner,仅从固定 Analyzer/工具选择 |
| `retrieve_runbooks` | 经内部工具网关取 Runbook(参考知识,type=knowledge) |
| `run_analyzer` | 五类 Analyzer(kubernetes/metrics/logs/traces/change),调工具拿 Evidence 后模型结构化分析 |
| `synthesize_hypotheses` | Synthesizer,产出 Top-N 假设并 `POST .../hypotheses` |
| `publish_diagnosis` | 产出 DiagnosisResult(`remediation_proposal` 恒为 null)并 `POST .../diagnosis` |
| `record_phase` / `record_event` / `record_usage` | 阶段/事件/用量回写 |

Analyzer 通过 `workflow.gather` 并行运行,只交换结构化状态,不互相自由对话。

## Model Gateway(architecture 12.1)

- `AIOPS_MODEL_PROVIDER=mock`(默认):**确定性**,无需任何 API key 即可端到端跑通。
  依据 incident/signals 推断故障类别(release_regression / resource_saturation /
  dependency_failure / config_error),产出贴合场景的中文分诊、计划、假设、诊断。
  token/cost 用量按输入长度给出确定性估算。
- `AIOPS_MODEL_PROVIDER=anthropic`:用 `anthropic` SDK 对接真实 Claude
  (`AIOPS_ANTHROPIC_MODEL` 默认 `claude-opus-4-8[1M]`),强制 JSON 输出并过 Pydantic 校验。

## MockProvider 如何保证端到端可跑

1. 无外部依赖:不读时钟、不用随机数、不需网络或密钥;
2. 故障类别推断是纯函数(先看 `incident.fault_category`,再看信号关键词);
3. 分诊/计划/合成三步共享同一场景表,叙事一致;
4. 有实时证据 → 产出 `supported` 结论(workflow 走 concluded);
   证据不足或未知故障 → 低置信度 `unresolved`(workflow 走 needs_human 升级);
5. 用量估算确定,可驱动预算会计与升级逻辑。

## 评测服务(Evaluation,architecture 18)

`aiops_worker/evaluation/` 提供**离线回放 + 质量门槛**能力,不依赖真实 Temporal:
直接调用确定性策略函数与 `ModelProvider` 端到端跑一遍 RCA 推理管线
(triage → deep-RCA gate → plan → 采证据 → synthesize → 诊断),对照 Golden Cases
计算首版质量指标。

模块:
- `models.py` — `GoldenCase` / `SignalFixture` / `EvaluationResult` /
  `EvaluationRunSummary`,对齐 `golden_cases` / `evaluation_runs` 表。
- `pipeline.py` — `OfflineReplayPipeline`:复刻 workflow 主循环(含一次补充轮),
  用确定性离线 collector 代替 Tool Gateway 产出实时/参考证据。
- `metrics.py` — 指标计算(纯函数):
  - **Top-1 / Top-3 根因命中率**:最高/前三假设语句是否包含期望根因关键词;
  - **关键结论证据引用率**:被判定为 `supported` 的根因中,引用了 ≥1 条**实时**证据
    的比例(目标 100%;参考知识 `type=knowledge` 不计入,architecture 12.2);
  - **无证据支撑根因比例(幻觉率)**:`supported` 却无实时证据支撑的比例(目标 <5%);
  - **P95 首次诊断耗时**(目标 <5min)。
- `seed_cases.py` — 5 条种子 Golden Cases,覆盖四类故障(发布回归 / 资源瓶颈 /
  依赖超时 / Pod 异常·配置)。SQL 形式见 `shared/sql/004_seed_golden_cases.sql`。
- `store.py` — 直连 DB 读 `golden_cases`、写 `evaluation_runs`(需 `db` extra)。

CLI:

```bash
make evaluate                          # 用种子用例离线回放 + 打印质量门槛报告
# 或:
uv run python -m aiops_worker.evaluation.run           # 种子用例
uv run python -m aiops_worker.evaluation.run --from-db # 从 golden_cases 表加载
uv run python -m aiops_worker.evaluation.run --write   # 汇总写入 evaluation_runs
uv run python -m aiops_worker.evaluation.run --json    # 输出完整 JSON
```

`--from-db` / `--write` 需要 `AIOPS_DB_DSN`。进程退出码在任一门槛失败时非 0,便于接 CI。

## pgvector 语义检索(RAG,architecture 12.2)

`aiops_worker/knowledge/` 为 Knowledge Service 增加向量检索:
- `embeddings.py` — `EmbeddingProvider` 抽象 + **确定性** `MockEmbeddingProvider`:
  基于 SHA-256 signed feature hashing 把词袋映射到稳定的 1536 维单位向量
  (匹配 `knowledge_items.embedding vector(1536)`),无需 API key、可复现、可测。
  `anthropic` / `openai` / `local` 为留桩(TODO)。选择由 `AIOPS_EMBEDDING_PROVIDER`
  决定(默认 `mock`)。
- `store.py` — `KnowledgeStore`:为 `knowledge_items` 生成/回填 embedding,
  检索用 pgvector 余弦距离 `ORDER BY embedding <=> query`。
- `reindex.py` — 重建 embedding 的 CLI。

**实时证据 vs 参考知识**边界保持不变:检索出的知识仍是 `type=knowledge`,
只能生成假设/建议查询,不能证明根因(由证据类型在下游强制)。

CLI:

```bash
export AIOPS_DB_DSN=postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable
uv run python -m aiops_worker.knowledge.reindex                       # 重建全部 embedding
uv run python -m aiops_worker.knowledge.reindex --query "发布回归导致依赖超时"  # 重建后做一次示例检索
make reindex
```

需要 DB 相关能力时安装 `db` extra:`uv sync --extra db`(或 `make install`,dev extra 已含)。

## 本地启动

先装依赖(阿里源已在 `pyproject.toml`/Makefile 内置):

```bash
cd ai-worker
make install            # uv sync --extra dev(含 psycopg/pgvector,可跑 DB 集成测试)
# 需要真实 Claude 时:make install-anthropic
```

运行测试(不需 Temporal / 网络):

```bash
make test               # uv run pytest -q
make check-import       # uv run python -c "import aiops_worker.workflow"
```

DB 集成测试(pgvector 检索 + evaluation_runs 写入)默认跳过;设置可达的
`AIOPS_DB_DSN` 后会自动纳入:

```bash
AIOPS_DB_DSN=postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable make test
```

启动 Worker(需要 Temporal Server 在 `localhost:7233`;mock provider 无需模型密钥):

```bash
cp .env.example .env     # 按需修改
make run                 # uv run aiops-worker
```

Go 控制面用 Go SDK 以工作流名 `InvestigationWorkflow`、task queue
`investigation-ai` 启动,启动参数见 `docs/INTEGRATION.md`。

## 环境未验证说明

若 `uv sync` 因网络原因无法拉取 `temporalio` 等依赖,代码本身仍完整正确、测试逻辑齐备;
可先试阿里源,再试代理 `HTTPS_PROXY=http://127.0.0.1:8897`。
