# control-plane — 基础设施控制平面(Go)

确定性、与模型无关的核心。单进程装配以下子系统(生产可拆分为独立部署单元):

| 子系统 | 包 | 职责 |
|---|---|---|
| Signal Ingress | `internal/api`(`ingress.go`) | Webhook 鉴权、快速 2xx、标准化、持久化 + outbox。支持原生 Signal 与 Alertmanager 格式 |
| Incident Manager | `internal/incident` | 消费 `signals`,归一化、去重聚合(grouping_key)、版本、生命周期、故障分类 |
| Trigger Policy | `internal/trigger` | 确定性触发/硬停止判断(**不交给 LLM**),编排调查并启动 Temporal 工作流 |
| Tool Gateway | `internal/gateway` | 工具白名单、范围注入、Schema 校验、**脱敏**、审计、冻结 Evidence |
| 公共 API | `internal/api`(`public_api.go` / `sse_feedback.go`) | 前端 + webhook,含 SSE 时间线 |
| 内部 API | `internal/api`(`internal_api.go`) | AI Worker 唯一回写入口(业务库为单一事实源) |
| Outbox | `internal/outbox` | Outbox Pattern 投递领域事件到 Kafka |
| 持久化 | `internal/store` | pgx,业务库访问 |
| Temporal | `internal/temporalx` | 启动/信号/取消工作流,不可用时降级 |

## 运行

```bash
# 依赖 deploy/ 的基础设施已 make up
cp ../deploy/.env.example .env && set -a && . ./.env && set +a   # 或手动 export
make run
```

- 公共 API:`:8088`(健康 `GET /healthz`)
- 内部 API:`:8090`

## 端到端冒烟(需基础设施 + cluster-agent + ai-worker)

```bash
# 注入一条 Alertmanager 风格告警
curl -s localhost:8088/v1/signals -H 'Content-Type: application/json' -d '{
  "alerts":[{"status":"firing","labels":{"alertname":"HighErrorRate","severity":"critical","namespace":"payment","deployment":"checkout","cluster":"prod-cn-1"},"startsAt":"2026-07-26T10:00:00Z"}]
}'

# 稍候查看聚合出的 Incident
curl -s 'localhost:8088/v1/incidents?status=open' | jq
```

## 测试

```bash
make test   # 覆盖 grouping_key 去重、严重级别归一化、故障分类、触发策略、脱敏、工具白名单
```

## 设计约束落地

- **默认只读**:`internal/api/internal_api.go` 在写 diagnosis 时强制 `remediation_proposal=null`;工具白名单不含任何写操作。
- **确定性护栏**:触发与停止条件在 `internal/trigger` 用普通代码实现。
- **单一事实源**:AI Worker 不直连 DB,全部经内部 API 回写。
- **故障隔离**:Temporal / Kafka / cluster-agent 不可用时降级而非崩溃(见 `temporalx/noop.go`、消费者重试)。

## 生产 TODO(首版未覆盖)

- Ingress webhook 签名校验、OIDC/RBAC(`userOrDefault` 目前从 header 取用户,生产接 SSO)。
- 触发策略的维护窗口/静默、预算耗尽、冷却、并发上限接配置源。
- mTLS 到 cluster-agent。
- pgvector 语义检索(现为关键词检索,DDL 与列已就绪)。
