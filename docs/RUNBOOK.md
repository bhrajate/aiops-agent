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
| 改了 `shared/sql` 未生效 | DDL 仅首次建卷执行;`make clean && make up` 或手动 `make psql` 执行 |
| Worker 无诊断 | 确认 `AIOPS_MODEL_PROVIDER=mock`(默认无需 API key);查看 worker 日志 |

## 验收清单

对照 [`../生产级AIOps-Agent架构设计.md`](../生产级AIOps-Agent架构设计.md) 第 22 节。首版落地情况见 [`ACCEPTANCE.md`](ACCEPTANCE.md)。
