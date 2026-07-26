# deploy — 本地/开发基础设施

用 docker-compose 一键拉起 AIOps Agent 依赖的全部有状态基础设施(对应架构文档第 13 节)。

## 组件

| 服务 | 端口 | 用途 |
|---|---|---|
| PostgreSQL + pgvector | 5432 | 业务库(Incident/Investigation/Evidence/知识向量),事实源。首次启动自动执行 `../shared/sql/*.sql` |
| Temporal + 专用 PostgreSQL | 7233(gRPC),8233(UI) | 持久化工作流,DB 与业务库隔离 |
| Redpanda(Kafka 兼容) | 19092 | 事件总线,自动创建 `signals`/`incidents`/`investigations` topics |
| MinIO(S3 兼容) | 9000/9001 | 证据快照与报告,自动创建 `aiops-evidence` bucket |
| Redis | 6379 | 限流/缓存(可选) |

## 使用

```bash
make up        # 拉起并等待健康
make ps        # 查看状态
make psql      # 进入业务库
make topics    # 查看 Kafka topics
make logs      # 跟随日志
make down      # 停止(保留数据)
make clean     # 停止并清空数据卷(危险)
```

## 说明

- 业务库 DDL 只在**数据卷首次创建**时自动执行。改了 `shared/sql` 后需 `make clean && make up` 重建,或手动 `make psql` 执行。
- Temporal 的数据库与业务库物理隔离(独立容器),满足文档"独立实例或至少独立 Schema"的要求。
- 生产部署应替换为托管 PostgreSQL / Kafka / 对象存储,并启用 TLS、认证、备份与灾备演练(文档第 15 节)。此 compose 仅用于开发与演示。
