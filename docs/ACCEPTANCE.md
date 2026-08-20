# 生产验收清单落地情况

对照 [`生产级AIOps-Agent架构设计.md`](生产级AIOps-Agent架构设计.md) 第 22 节。标注首版实现程度。

> 状态更新(生产化阶段):认证/授权、mTLS、可观测性、评测、部署编排、可靠性硬化均已落地并端到端验证。

| # | 验收项 | 状态 | 落地位置 / 说明 |
|---|---|---|---|
| 1 | 原告警通知不依赖 AIOps Agent | ✅ | Signal Ingress 只接收并快速 2xx,不参与原通知链路;全链路异步 |
| 2 | Signal 可持久化、重放并进入 DLQ | ✅ | 持久化 + Outbox + Kafka 至少一次;重试超限进 `dead_letters`(SECURITY §7),已验证 |
| 3 | Incident 去重、聚合、版本、幂等测试通过 | ✅ | **两层模型**:`alert_groups` 去重(同资源同规则收敛)+ `incidents` 聚合(correlation_key 合并同 namespace 多资源)。E2E 14/14:去重不增 group、跨资源合并进同一 incident、blast 随影响面增减、单 group 恢复不误关 incident。相关性维度为 namespace 而非拓扑(见 [能力边界](ARCHITECTURE.md#能力边界设计意图-vs-当前实现)) |
| 4 | Temporal Workflow 可在 Worker 重启后恢复 | ✅ | Temporal 持久化 + 确定性 workflow + 幂等 Activity |
| 5 | 所有外部调用均在 Activity 中执行 | ✅ | 模型/内部 API/工具调用全封装为 Activity |
| 6 | LLM 无生产凭据,不能调用任意命令 | ✅ | 模型只经 Model Gateway;工具白名单只读;凭据不进模型 |
| 7 | Tool Gateway 完成范围注入、授权、脱敏和审计 | ✅ | `internal/gateway`:白名单 + scope 注入 + 脱敏 + 审计 + 冻结 Evidence + S3 快照 |
| 8 | 每个关键结论都有 Evidence ID | ✅ | Hypothesis 绑定 supporting/contradicting evidence_ids;E2E 验证 7 证据 |
| 9 | 系统能够返回"证据不足" | ✅ | DiagnosisResult 支持 inconclusive/unresolved;预算耗尽升级 |
| 10 | Prompt Injection 和越权测试通过 | ✅ | 工具结果作数据、输出 schema 校验、脱敏;**RBAC/ABAC 越权 14/14 测试通过** |
| 11 | Golden Dataset 回归达到质量门槛 | ✅ | Evaluation 服务:**Top-3 100% / 证据引用 100% / 幻觉 0% / P95<300s,4/4 门槛 PASS** |
| 12 | 影子运行和 Canary 门禁通过 | ◐ | 评测/回放框架就绪(离线回放已跑通);影子/Canary 流程见 DEPLOY.md,需生产接入 |
| 13 | PostgreSQL/Temporal/事件总线恢复演练 | ◐ | 存储物理隔离 + 备份说明(DEPLOY.md);定期演练为运维流程 |
| 14 | 控制面异常不影响监控与告警主链路 | ✅ | 完全解耦、异步、降级(Temporal/Kafka/agent/S3 不可用不崩溃) |
| 15 | 首版部署不存在任何生产写权限 | ✅ | 工具全只读(K8s 仅 Get/List,ClusterRole 仅 get/list/watch);remediation 恒 null |

图例:✅ 已落地并验证 / ◐ 框架就绪,需生产运维接入

## 生产化硬化(本阶段新增,全部端到端验证)

| 能力 | 落地 | 验证 |
|---|---|---|
| 认证 OIDC/JWT | HS256 开发签发 + OIDC 就绪;Bearer 中间件 | 未认证 401、登录、垃圾 token 拒绝 |
| 授权 RBAC/ABAC | 角色权限 + 集群/命名空间范围;`用户∩Agent∩Incident` | viewer 启动被拒 403、bob 越权 inventory 403、列表过滤 |
| mTLS | Gateway↔Agent 双向 TLS(RequireAndVerifyClientCert) | 证书脚本 + 配置开关 |
| Webhook 签名 | Signal Ingress HMAC-SHA256 | 无签名 401、正确签名 202 |
| 幂等 | Idempotency-Key 落库 | 同 key 返回同一调查 |
| 证据对象存储 | 脱敏 raw 快照 → MinIO | E2E 验证 5 个 S3 对象 |
| DLQ | 消费重试超限 → dead_letters + 告警 | 表 + 指标就绪 |
| 内部 API 鉴权 | X-Internal-Token 共享密钥 | 无 token 401、带 token 200 |
| 可观测性 | Prometheus /metrics(control-plane + cluster-agent)+ OTLP 追踪 | 工具调用计数/延迟实时抓取 |
| 评测体系 | Golden Dataset 离线回放 + 质量门槛 | 4/4 门槛 PASS |
| 部署编排 | K8s manifests + Helm + CI + mTLS 证书脚本 | YAML 校验 + 只读 RBAC |

## 验证脚本

`scripts/` 下每个专项检查对应一条不变量。注意有几个脚本**不自己起后端**,
必须用 `with-backend.sh` 运行(否则 curl 静默失败、断言照着库里残留数据打分,
通过与失败都没有意义——这是曾经踩过的坑,现在这些脚本会 fail-fast):

**本机 5432 被占时怎么跑。** 脚本的连库方式由 [`scripts/lib/db.sh`](../scripts/lib/db.sh)
按优先级解析:`AIOPS_PSQL` → `AIOPS_PG_CONTAINER` → compose 里的 postgres → 本机
`psql` + `AIOPS_DB_DSN`。所以不必停掉占用 5432 的其他服务:

```bash
docker run -d --name my-pg -e POSTGRES_USER=aiops -e POSTGRES_PASSWORD=aiops \
  -e POSTGRES_DB=aiops -p 55434:5432 pgvector/pgvector:pg16
export AIOPS_DB_DSN="postgres://aiops:aiops@localhost:55434/aiops?sslmode=disable"
export AIOPS_PG_CONTAINER=my-pg      # 脚本自己的断言查这个容器
cd control-plane && go run ./cmd/control-plane migrate up && cd ..
for f in shared/seed/*.sql; do docker exec -i my-pg psql -U aiops -d aiops -q -f - < "$f"; done
```

`db.sh` 在连不上或 SQL 出错时**立刻终止脚本**,绝不返回空串 —— 此前那些
`psql ... 2>/dev/null` 会让断言收到空值并照着空数据打分(见下方"曾经踩过的坑")。

> 用 Redpanda 时注意:topic 里的消息跨脚本运行保留,而脚本只清库不清 topic。
> 上一次的消息被重放会产生 `load incident: not found`。逐个脚本之间重建 topic:
> `docker exec <rp> rpk topic delete signals incidents investigations && rpk topic create ...`。
> 另外杀掉遗留的 control-plane 进程 —— 消费组里的僵尸成员会占住唯一那个 partition,
> 导致新实例收不到任何消息(`rpk group describe incident-manager` 看 MEMBERS)。

```bash
./scripts/prod-e2e.sh                       # 全链路(自带构建 + 起后端)
./scripts/with-backend.sh scripts/check-two-tier.sh \
                          scripts/check-correlation-window.sh \
                          scripts/check-blast.sh
./scripts/check-ratelimit.sh                # 自带构建 + 起独立实例
# 下面三个也自带构建 + 起独立实例,同样**不要**用 with-backend.sh 包裹。
# 前两个会**临时停掉 postgres** 来验证故障路径(退出时用 trap 恢复):
./scripts/check-probes.sh
./scripts/check-queue-metrics.sh
./scripts/check-alert-rules.sh
./scripts/check-signal-idempotency.sh
./scripts/check-trigger-policy.sh
./scripts/check-outcome-metrics.sh
./scripts/check-feedback-loop.sh
# 下面这个会额外起一个 Prometheus 容器与 exporter 容器(退出时自动清理):
./scripts/check-slo-burnrate.sh
# 这个**不需要任何基础设施**(只渲染 chart + 跑 validate-config),已接入 CI:
./scripts/check-prod-guards.sh
```

| 脚本 | 验证的不变量 | 当前结果 |
|---|---|---|
| `prod-e2e.sh` | 全链路:信号→incident→调查→证据→诊断→反馈 | evidence 7 / 0 拒绝 |
| `check-auth.sh` | 认证与 RBAC/ABAC 拒绝路径 | 14/14 |
| `check-two-tier.sh` | 去重(group)与聚合(incident)分离、单 group 恢复不误关 incident | 14/14 |
| `check-correlation-window.sh` | 相关性合并受时间窗约束;陈旧 incident 自动 resolved | 6/6 |
| `check-blast.sh` | 影响面扩大可见;`blast_radius` 四维齐备(F3) | PASS |
| `check-orphan-reconcile.sh` | 孤儿调查补偿 | 4/4 |
| `check-roles.sh` | 按角色拆分部署单元 | 11/11 |
| `check-ratelimit.sh` | 入口限流:429 + Retry-After + **按条计费** + 指标(F6) | 7/7 |
| `check-metrics.sh` | Prometheus 指标暴露 | PASS |
| `check-frontend-auth.sh` | 前端登录与越权 | 5/5 |
| `check-probes.sh` | 探针语义分离:**真的停 postgres**,`/readyz` 返 503(副本被摘出 endpoints)、`/healthz` 仍 200(不重启进程)、恢复后自动放回(P3) | 9/9 |
| `check-queue-metrics.sh` | 队列积压指标:**真的停 postgres**,四个队列 gauge 全部缺失且 `scrape_failed=1`(缺失而非上报 0)、其余指标仍正常、恢复后回来(P4) | 12/12 |
| `check-alert-rules.sh` | 告警规则对着**真实 /metrics** 校验:语法 + metric 名存在性(引用不存在的 series 会永不触发)(P5) | 10/10 |
| `check-signal-idempotency.sh` | Alertmanager 重投递去重:重投 5 次只落 1 条且 `signal_count` 不虚增;反向 `firing→resolved→firing` 仍是 3 条(F5) | 6/6 |
| `check-trigger-policy.sh` | 自动触发策略真的会拦:P4 单信号被跳过且写审计;P1 与"P4 但变更关联"必触发(F7) | 7/7 |
| `check-outcome-metrics.sh` | 成效与成本指标:token/费用/时延/采纳率九个指标齐备并有值(F10) | 10/10 |
| `check-feedback-loop.sh` | 反馈闭环:confirm 反馈 → **pending** 用例 → sre 审核 → 入评测集;oncall 不可审核;不可翻转 | 13/13 |
| `check-slo-burnrate.sh` | 主动异常检测:起**真实 Prometheus** + 可控错误率;未越限不产出、越限产出并聚合为 P1 incident、持续燃烧仍 1 条 | 17/17 |
| `test_pydantic_ai_provider.py` | pydantic-ai provider 保住手写版的四类性质:失败绝不抛异常而是兜底(4 项,反验证过)、白名单违规触发带原因的重问、凭空 evidence_id 被丢弃、usage 真实填充;两个 provider 共用同一份提示词与净化口径 | 12/12 |
| `check-prod-guards.sh` | 生产护栏**真的生效**:渲染后的清单必须显式声明 `AIOPS_ENV`/`AIOPS_DATASOURCE`;渲染结果喂给 `validate-config` 必须通过(否则生产起不来);反向逐项抽掉必需项必须被拒(证明护栏不空转);cluster-agent 与 ai-worker 在生产拒绝 mock | 24/24 |
| `go test ./internal/store/ -run DB` | 保留清理两条安全不变量(活跃/在跑不删,F4) | 8/8 |

## 核心设计原则落地

Incident-first / Evidence-first / Workflow-first / Read-only / Deterministic guardrails /
Least privilege / Fail safely / Replayable / Human-owned / Independent alerting —— 全部落地,详见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 仍建议在真实生产环境补齐(诚实说明)

- 接入企业真实 OIDC IdP(当前 hs256 为开发签发;OIDC verifier 已留接口)。
- cluster-agent live 数据源对接真实 Prometheus/Loki/Tempo 端点并压测(实现已就绪,默认 mock)。
- 生产 Secret 走 Vault/KMS 与自动轮换(当前用 K8s Secret 模板)。
- 执行季度级备份恢复与灾备演练(流程见 DEPLOY.md)。
- 影子运行 + 小流量 Canary 灰度(门禁框架就绪)。
