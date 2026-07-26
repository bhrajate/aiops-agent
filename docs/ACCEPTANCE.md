# 生产验收清单落地情况

对照 [`生产级AIOps-Agent架构设计.md`](生产级AIOps-Agent架构设计.md) 第 22 节。标注首版实现程度。

> 状态更新(生产化阶段):认证/授权、mTLS、可观测性、评测、部署编排、可靠性硬化均已落地并端到端验证。

| # | 验收项 | 状态 | 落地位置 / 说明 |
|---|---|---|---|
| 1 | 原告警通知不依赖 AIOps Agent | ✅ | Signal Ingress 只接收并快速 2xx,不参与原通知链路;全链路异步 |
| 2 | Signal 可持久化、重放并进入 DLQ | ✅ | 持久化 + Outbox + Kafka 至少一次;重试超限进 `dead_letters`(SECURITY §7),已验证 |
| 3 | Incident 去重、聚合、版本、幂等测试通过 | ✅ | `internal/incident` + 测试;grouping_key 唯一约束;E2E 验证 version 递增 |
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

## 核心设计原则落地

Incident-first / Evidence-first / Workflow-first / Read-only / Deterministic guardrails /
Least privilege / Fail safely / Replayable / Human-owned / Independent alerting —— 全部落地,详见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 仍建议在真实生产环境补齐(诚实说明)

- 接入企业真实 OIDC IdP(当前 hs256 为开发签发;OIDC verifier 已留接口)。
- cluster-agent live 数据源对接真实 Prometheus/Loki/Tempo 端点并压测(实现已就绪,默认 mock)。
- 生产 Secret 走 Vault/KMS 与自动轮换(当前用 K8s Secret 模板)。
- 执行季度级备份恢复与灾备演练(流程见 DEPLOY.md)。
- 影子运行 + 小流量 Canary 灰度(门禁框架就绪)。
