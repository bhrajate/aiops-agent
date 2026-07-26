# 生产验收清单落地情况

对照 [`../生产级AIOps-Agent架构设计.md`](../生产级AIOps-Agent架构设计.md) 第 22 节。标注首版实现程度。

| # | 验收项 | 状态 | 落地位置 / 说明 |
|---|---|---|---|
| 1 | 原告警通知不依赖 AIOps Agent | ✅ 设计保证 | Signal Ingress 只接收信号并快速 2xx,不参与原告警通知链路;Agent 全链路异步 |
| 2 | Signal 可持久化、重放并进入 DLQ | ◐ 部分 | 持久化 + Outbox + Kafka 至少一次(`store`/`outbox`/`bus`);DLQ 为 TODO(消费失败当前退避重投) |
| 3 | Incident 去重、聚合、版本、幂等测试通过 | ✅ | `internal/incident` + `manager_test.go`,`grouping_key` 唯一约束,版本递增,已 E2E 验证 |
| 4 | Temporal Workflow 可在 Worker 重启后恢复 | ✅ 机制具备 | Temporal 持久化工作流 + 确定性 workflow;Activity 幂等 |
| 5 | 所有外部调用均在 Activity 中执行 | ✅ | Worker 的模型调用/内部 API/工具调用全部封装为 Activity |
| 6 | LLM 无生产凭据,不能调用任意命令 | ✅ | 模型只经 Model Gateway;工具白名单仅只读;凭据不进模型 |
| 7 | Tool Gateway 完成范围注入、授权、脱敏和审计 | ✅ | `internal/gateway`:白名单 + scope 注入 + 脱敏正则 + 审计 + 冻结 Evidence |
| 8 | 每个关键结论都有 Evidence ID | ✅ | Hypothesis 绑定 supporting/contradicting evidence_ids;DiagnosisResult 携带引用 |
| 9 | 系统能够返回"证据不足" | ✅ | DiagnosisResult 支持 `inconclusive/unresolved`,预算耗尽升级给人 |
| 10 | Prompt Injection 和越权测试通过 | ◐ 部分 | 工具结果作为数据、输出 schema 校验、脱敏;专项测试用例部分覆盖 |
| 11 | Golden Dataset 回归达到质量门槛 | ✗ 未覆盖 | 评测服务为后续阶段(文档阶段 3);首版预留数据模型 |
| 12 | 影子运行和 Canary 门禁通过 | ✗ 未覆盖 | 文档阶段 3/4;首版聚焦诊断链路 |
| 13 | PostgreSQL/Temporal/事件总线恢复演练 | ◐ 机制具备 | 存储物理隔离、命名卷/备份可做;定期演练为运维流程 |
| 14 | 控制面异常不影响监控与告警主链路 | ✅ 设计保证 | 完全解耦、异步投递、降级(Temporal/Kafka/agent 不可用不崩溃) |
| 15 | 首版部署不存在任何生产写权限 | ✅ | 工具全只读;`remediation_proposal` 强制为 null;无 exec/SSH/写 API |

图例:✅ 已落地 / ◐ 部分落地或机制具备 / ✗ 首版非目标(后续阶段)

## 核心设计原则落地

| 原则 | 落地 |
|---|---|
| Incident-first | 消费聚合后的 Incident 而非原始告警风暴 |
| Evidence-first | 结论绑定 Evidence ID,允许"无法确定" |
| Workflow-first | Temporal 管可靠执行,LLM 只在有界 Activity 推理 |
| Read-only by default | 全部工具只读,无写权限 |
| Deterministic guardrails | 触发/停止/预算/脱敏由确定性 Go 代码执行 |
| Least privilege | Gateway 按 Incident/集群/命名空间/时间窗注入范围 |
| Fail safely | 冲突/权限不足/预算耗尽/低置信度 → 升级给人 |
| Replayable | 时间线事件 + 审计日志 + 版本记录 |
| Human-owned | 人工确认/纠错/关闭闭环 |
| Independent alerting | 告警链路完全解耦 |
