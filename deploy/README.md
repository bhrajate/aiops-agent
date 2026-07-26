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

## 生产部署编排(本目录)

| 路径 | 内容 |
|---|---|
| [`DEPLOY.md`](DEPLOY.md) | **生产部署总指南**:K8s/Helm 步骤、Secret、mTLS、备份恢复、灰度 |
| `k8s/` | 原始 Kubernetes 清单(namespace / configmap / secret 模板 / 四组件 / RBAC / NetworkPolicy / Ingress) |
| `helm/aiops/` | Helm chart(`values.yaml` + `values-dev.yaml` + `values-prod.yaml`,一键切开发/生产) |
| `certs/gen-certs.sh` | mTLS 自签证书生成脚本(CA + agent 服务端 + client 客户端;产物已 gitignore) |
| `docker/frontend.Dockerfile` | 前端生产镜像(多阶段构建 + nginx-unprivileged 托管静态) |
| `docker-compose.prod.yml` | 生产化 compose overlay(四组件容器化 + mTLS + 密钥文件),叠加在开发基础设施之上 |
| `.env.prod.example` | overlay 密钥模板(复制为 `.env.prod` 填值,勿提交) |

CI 定义见仓库根 [`.github/workflows/ci.yml`](../.github/workflows/ci.yml)。

### 快速命令

```bash
# Helm 生产部署(需先备好 Secret 与 mTLS 证书,见 DEPLOY.md §3/§4)
helm upgrade --install aiops ./helm/aiops -n aiops --create-namespace \
  -f helm/aiops/values-prod.yaml

# 或原始清单
kubectl apply -f k8s/00-namespace.yaml && kubectl apply -f k8s/ -R

# 生产化 compose overlay(本机验证生产拓扑)
bash certs/gen-certs.sh && cp .env.prod.example .env.prod   # 编辑填值
docker compose -f docker-compose.yml -f docker-compose.prod.yml --env-file .env.prod up -d --build
```
