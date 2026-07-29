# 运行手册(RUNBOOK)

本手册说明如何本地端到端启动生产级 AIOps Agent 并跑通一次调查。

## 前置

- Docker + Docker Compose
- Go 1.26、Python 3.11 + uv、Node ≥ 20
- 本机 8088 / 8090 / 9100 / 5173 / 5432 / 7233 / 19092 / 9000-9001 / 6380 可用
  - 说明:公共 API 用 **8088**(避开本机占用 8080 的服务),Redis 宿主端口 **6380**。

## 启动顺序

```bash
# 0) 基础设施
cd deploy && make up            # postgres+pgvector / temporal / redpanda / minio / redis
                                # 首次启动自动建表并注入 runbook 种子

# 1) Cluster Agent(只读工具,:9100)
cd ../cluster-agent && make run

# 2) Python RCA Worker(连 Temporal,注册 InvestigationWorkflow)
cd ../ai-worker && make install && make run

# 3) 控制面(:8088 公共 / :8090 内部)
cd ../control-plane
export $(grep -v '^#' ../deploy/.env.example | xargs)   # 或自定义 .env
make run

# 4) 前端 Workbench(:5173)
cd ../frontend && npm install && npm run dev
```

浏览器打开 http://localhost:5173 。

## 端到端演示

在前端「模拟注入 Signal」面板提交,或用 curl:

```bash
curl -s localhost:8088/v1/signals -H 'Content-Type: application/json' -d '{
  "alerts":[{"status":"firing","labels":{
    "alertname":"HighErrorRate","severity":"critical",
    "namespace":"payment","deployment":"checkout",
    "cluster":"prod-cn-1","rule_id":"r-101"
  },"startsAt":"2026-07-26T10:00:00Z"}]
}'
```

预期链路:
1. Signal Ingress 快速 2xx → 写库 + outbox → Kafka `signals`
2. Incident Manager 消费 → 去重聚合为 Incident(P1 / release_regression)→ Kafka `incidents`
3. Trigger Policy 判定触发 → 创建 Investigation → 启动 Temporal 工作流
4. Python Worker 执行状态机:Triage → Planning → Collecting(经 Tool Gateway 调只读工具产出 Evidence)→ Synthesizing → 产出带证据引用的 DiagnosisResult
5. 前端时间线(SSE)实时展示阶段流转、工具调用、假设与证据;值班人员确认/纠错/关闭

## 排障

| 现象 | 处理 |
|---|---|
| 控制面 `bind: address already in use :8088` | 改 `AIOPS_PUBLIC_ADDR`;检查本机占用 |
| Redpanda `Permission denied` | 已用命名卷;`make clean && make up` 重建 |
| Temporal 连接失败 | 控制面会降级(调查记录仍持久化);确认 `make up` 后 temporal 健康 |
| 改了 schema 未生效 | 新增迁移后需 `cd deploy && make migrate`;确认版本:`make migrate-version` |
| 控制面启动报 schema 版本落后 | 先跑迁移:`make migrate`(生产由 Helm pre-upgrade Job 执行)。控制面刻意不自动迁移 |
| schema 处于 dirty 态 | 上次迁移中途失败。人工确认哪些 SQL 已生效、补齐剩余部分,再 `control-plane migrate force <version>` 对齐版本表 |
| Worker 无诊断 | 确认 `AIOPS_MODEL_PROVIDER=mock`(默认无需 API key);查看 worker 日志 |

## 安全模式(生产化)

默认已启用认证与 webhook 签名。相关环境变量(见 [`SECURITY.md`](SECURITY.md)):

```bash
# 认证(开发用内置 hs256 签发;生产切 oidc)
export AIOPS_AUTH_MODE=hs256
export AIOPS_AUTH_HS256_SECRET=<改成强随机值>
# 内部 API 共享密钥(control-plane 与 ai-worker 必须一致)
export AIOPS_INTERNAL_TOKEN=<强随机值>
# Signal webhook HMAC 签名密钥(告警源与 ingress 共享)
export AIOPS_WEBHOOK_SECRET=<强随机值>
# 可观测性(可选,设置后导出 OTLP trace)
export AIOPS_OTLP_ENDPOINT=localhost:4318
# cluster-agent mTLS(生产开启)
export AIOPS_AGENT_MTLS_ENABLED=true   # 需先跑 deploy/certs/gen-certs.sh
```

演示账号(hs256 模式):`alice/alice-pass`(sre)、`bob/bob-pass`(oncall,payment+cart)、`viewer/viewer-pass`(只读 payment)。

一键安全验证:
```bash
bash scripts/check-auth.sh            # 认证/RBAC/ABAC/幂等/webhook/内部 token(14 项)
bash scripts/prod-e2e.sh              # 认证+签名全开的完整 RCA E2E
bash scripts/check-frontend-auth.sh   # 前端经代理的登录链路
bash scripts/check-metrics.sh         # Prometheus /metrics 抓取
```

生产 Kubernetes 部署见 [`../deploy/DEPLOY.md`](../deploy/DEPLOY.md)。

## 告警处置

七条告警(`deploy/helm/aiops/templates/prometheusrule.yaml`)各对应一个具名故障模式。
规则表达式引用的指标名由 `scripts/check-alert-rules.sh` 对着**真实 /metrics 输出**
校验——引用不存在的 series 时 Prometheus 不报错、规则永不触发,那比没有告警更糟。

### outbox 投递卡住

`AiopsOutboxDeliveryStuck`:最老待投递记录超过 10 分钟。

这是**领域事件停止发布**。表现具有欺骗性:`/v1/signals` 仍返回 202、
`aiops_signals_ingested_total` 仍在涨,但 incidents 不再增长,前端看起来一切正常。

1. 确认 outbox 投递循环还在跑:副本是否启用了 `outbox` 角色(`AIOPS_ROLES`);
2. 确认 Kafka 可达:日志里找 `publish outbox failed`;
3. 看按 topic 的分布:`aiops_outbox_pending{topic}` —— 只有单个 topic 积压
   通常是该 topic 的分区不可用,全部积压通常是 Kafka 整体或 relay 本身的问题。

**切勿直接删除 outbox 行。** 那是尚未发布的领域事件,删掉即永久丢事件:
incident 留在库里但下游永远收不到,状态与事实源不一致且无法补偿。

### 队列指标缺失

`AiopsQueueMetricsMissing`:`aiops_queue_scrape_failed == 1` 或队列 series 缺失。

**它不表示队列有问题,而表示"看不见队列状态"。** 此时
`AiopsOutboxDeliveryStuck` 不会触发,因为它依赖的 series 不存在——
即处于"不知道队列多深"而非"队列没问题"。这两者必须区分,否则静默失败会换个形式回来。

1. 控制面能否连上业务库(看 `/readyz` 响应体);
2. 日志里找 `queue depth scrape failed`,里面带具体 SQL 错误。

### 死信堆积

`AiopsDeadLettersAccumulating`:死信存量超过 10 条。

死信是重试耗尽后的消息,**需要人工判断**,不能一键重放:

1. 先归类错误:`SELECT topic, error, count(*) FROM dead_letters GROUP BY 1,2;`
2. 载荷格式问题(反序列化失败)→ 要改代码,重放无用;
3. 下游临时不可用 → 可重放,但需确认下游已恢复且重放不会产生重复副作用
   (系统是至少一次投递 + 幂等键,多数路径重放安全)。

### outbox 投递已放弃

`AiopsOutboxRecordsAbandoned`:`aiops_outbox_dead > 0`。

比死信更严重:这些领域事件**永远不会发布**。对应 incident 已在库里,
下游状态与事实源不一致。同样**切勿直接删除**——先确认是哪些事件、
下游是否需要人工补齐:

```sql
SELECT id, topic, key, attempts, error FROM outbox WHERE status='dead' ORDER BY id;
```

### 信号进来但没有 incident

`AiopsSignalsWithoutIncidents`:15 分钟内有信号流入但无 incident 产出。

断链兜底,覆盖 outbox 之外的断点(此时 outbox 是空的,前几条都不会触发):

1. `signals` topic 是否有新消息、消费延迟多少;
2. 启用 `ingest` 角色的副本是否存活;
3. `SELECT count(*) FROM dead_letters WHERE topic='signals';` —— 信号是否直接进了死信。

注意:若信号被相关性合并进**已存在**的活跃 incident,`incidents_created` 不会增长,
这是正常行为而非故障。判断依据是 `incidents` 表的 `version` 是否在涨。

### 副本持续 not ready

`AiopsReplicaNotReady`:副本 `/readyz` 持续返 503,已被摘出 Service endpoints。

1. 看 `/readyz` 响应体的 `dependencies`,它指名了是哪个依赖挂了;
2. **若是 database:重启副本无用。** 数据库挂了重启进程修不了数据库,
   只会丢进程内状态(限流令牌桶)并把最需要的日志冲掉。先修数据库,
   副本会在依赖恢复后自动放回 endpoints;
3. 若响应体是 `degraded`(Temporal / 对象存储不可用)则副本仍 ready,
   不会触发本告警——那些依赖不可用时控制面仍能接收信号与提供查询。

### 信号入口限流

`AiopsIngressThrottling`:10 分钟内有信号被限流拒绝。

被拒的信号**不会进入系统**。先判断性质:

1. 真实告警风暴 → 限流是预期行为,保护 DB/outbox 不被打穿,无需处置;
2. 配额过低 → 调 `AIOPS_INGRESS_RATE_PER_SEC` / `AIOPS_INGRESS_BURST`。

注意限流是**进程内**的,每副本独立配额,集群总容量 = 配置值 × 副本数。
排查时不要用单副本配额去推算集群容量。

## 验收清单

对照 [`生产级AIOps-Agent架构设计.md`](生产级AIOps-Agent架构设计.md) 第 22 节。落地情况见 [`ACCEPTANCE.md`](ACCEPTANCE.md)。
