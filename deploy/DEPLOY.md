# 生产部署指南(K8s / Helm)

本文覆盖 AIOps Agent 四组件(control-plane / ai-worker / cluster-agent / frontend)的
Kubernetes 生产部署、Secret 与 mTLS 配置、备份恢复、灰度发布。开发/本地请见
[`RUNBOOK.md`](../docs/RUNBOOK.md) 与 [`README.md`](README.md)。

> 依赖的 PostgreSQL / Temporal / Kafka(Redpanda)/ 对象存储在生产中应为**托管服务或独立
> Helm chart**,本仓库的清单只给出连接配置与占位(ConfigMap/Secret),不部署这些有状态组件。

## 0. 组件与端口

| 组件 | 副本 | 端口 | 入站来源 | 说明 |
|---|---|---|---|---|
| control-plane | ≥2 | 8088(公共)/ 8090(内部) | Ingress+frontend(8088);ai-worker(8090) | 唯一 DB 写入方,Tool Gateway |
| ai-worker | ≥2 | 无入站 | — | Temporal Worker,主动连出 |
| cluster-agent | ≥2 | 9100 | **仅 control-plane** | 只读工具,mTLS 服务端 |
| frontend | ≥2 | 8080 | Ingress | nginx 托管静态 + /v1 反代 |

## 1. 前置

- Kubernetes ≥ 1.26,CNI 支持 NetworkPolicy(Calico / Cilium)。
- Ingress 控制器(ingress-nginx),命名空间带标签 `kubernetes.io/metadata.name: ingress-nginx`。
- cert-manager(签发公共 API 的 TLS 证书;或手工准备 `aiops-public-tls`)。
- 已构建并推送四个镜像到镜像仓库(见 §6 CI)。
- 托管的 PostgreSQL(pgvector)/ Temporal / Kafka / S3 兼容对象存储,及其连接信息。

## 2. 两种部署方式

### 方式 A:原始清单(deploy/k8s/)

适合审阅与最小依赖场景。按序 apply:

```bash
cd deploy

# 1) 命名空间
kubectl apply -f k8s/00-namespace.yaml

# 2) 配置(先按环境编辑 k8s/10-configmap.yaml 的连接占位)
kubectl apply -f k8s/10-configmap.yaml

# 3) 密钥(勿用示例明文;见 §3。命令式创建更安全)
#    如需快速起步:kubectl apply -f k8s/20-secret.example.yaml(改值后)

# 4) mTLS 证书 Secret(见 §4)
bash certs/gen-certs.sh
kubectl -n aiops create secret generic aiops-agent-tls \
  --from-file=certs/ca.crt --from-file=certs/agent.crt --from-file=certs/agent.key
kubectl -n aiops create secret generic aiops-client-tls \
  --from-file=certs/ca.crt --from-file=certs/client.crt --from-file=certs/client.key

# 5) 应用组件 + 网络策略 + Ingress
kubectl apply -f k8s/ -R

kubectl -n aiops get pods,svc,ingress
```

### 方式 B:Helm(deploy/helm/aiops/,推荐)

```bash
cd deploy/helm

# 开发/演示(单副本、mock、mTLS 关、内置明文 Secret)
helm upgrade --install aiops ./aiops -n aiops --create-namespace \
  -f aiops/values-dev.yaml

# 生产(多副本、OIDC、anthropic、mTLS 必开、外部 Secret)
#   先创建证书与外部 Secret(见 §3、§4),再:
helm upgrade --install aiops ./aiops -n aiops --create-namespace \
  -f aiops/values-prod.yaml \
  --set config.clusterId=prod-cn-1 \
  --set ingress.host=aiops.corp.example
```

`values-prod.yaml` 默认 `secrets.create=false`,即不渲染明文 Secret,要求名为 `aiops-secrets`
的外部 Secret 已存在(由 Vault / External Secrets Operator 注入)。

渲染预览(不接触集群):

```bash
helm template aiops ./aiops -n aiops -f aiops/values-prod.yaml | less
```

## 3. Secret 配置

字段(全部 `AIOPS_` 前缀,见 [`SECURITY.md`](../docs/SECURITY.md)):

| Key | 用途 |
|---|---|
| `AIOPS_DB_DSN` | 业务库连接(生产 `sslmode=require`) |
| `AIOPS_INTERNAL_TOKEN` | 内部 API `:8090` 共享密钥(`X-Internal-Token`) |
| `AIOPS_WEBHOOK_SECRET` | `POST /v1/signals` HMAC 校验 |
| `AIOPS_AUTH_HS256_SECRET` | 开发签发密钥(OIDC 模式留空) |
| `AIOPS_OIDC_ISSUER` / `AIOPS_OIDC_JWKS_URL` | 生产 OIDC 验签 |
| `AIOPS_S3_ACCESS_KEY` / `AIOPS_S3_SECRET_KEY` | 对象存储凭据 |
| `AIOPS_ANTHROPIC_API_KEY` | `provider=anthropic` 时必填 |

**生产推荐:不落明文。** 用 External Secrets Operator 从 Vault/KMS 同步生成名为 `aiops-secrets`
的 Secret(架构文档 §19 凭据管理),Helm 设 `secrets.create=false`。

临时/自管时命令式创建(避免明文入库):

```bash
kubectl -n aiops create secret generic aiops-secrets \
  --from-literal=AIOPS_DB_DSN='postgres://aiops:***@pg:5432/aiops?sslmode=require' \
  --from-literal=AIOPS_INTERNAL_TOKEN='...' \
  --from-literal=AIOPS_WEBHOOK_SECRET='...' \
  --from-literal=AIOPS_S3_ACCESS_KEY='...' \
  --from-literal=AIOPS_S3_SECRET_KEY='...' \
  --from-literal=AIOPS_ANTHROPIC_API_KEY='...'
```

## 4. mTLS 证书流程(Tool Gateway ↔ Cluster Agent)

cluster-agent 作 TLS 服务端并校验客户端证书;control-plane 持客户端证书。文件名遵循
[`SECURITY.md`](../docs/SECURITY.md) §3:`ca.crt` / `agent.crt`+`agent.key` / `client.crt`+`client.key`。

```bash
# 1) 生成自签 CA + 服务端 + 客户端证书(SAN 含 cluster-agent / localhost)
bash deploy/certs/gen-certs.sh          # 生成到 deploy/certs/,已 gitignore

# 2) 落为两个 Secret
kubectl -n aiops create secret generic aiops-agent-tls \
  --from-file=deploy/certs/ca.crt --from-file=deploy/certs/agent.crt --from-file=deploy/certs/agent.key
kubectl -n aiops create secret generic aiops-client-tls \
  --from-file=deploy/certs/ca.crt --from-file=deploy/certs/client.crt --from-file=deploy/certs/client.key
```

- `aiops-agent-tls` 挂到 cluster-agent 的 `/certs`;`aiops-client-tls` 挂到 control-plane 的 `/certs`。
- Helm `mtls.enabled=true` 时,ConfigMap 自动注入 `AIOPS_AGENT_*` 路径,且 `AIOPS_CLUSTER_AGENT_URL`
  自动切 `https://`。
- **生产用企业 CA / Vault PKI 并启用轮换**,自签脚本仅用于开发/演示。证书到期前滚动更新 Secret 即可。

## 5. cluster-agent 只读 RBAC(安全核心)

`k8s/cluster-agent/rbac.yaml`(Helm 同源 `templates/rbac.yaml`)授予的 ClusterRole
**只含 `get`/`list`/`watch`**,覆盖 pods、pods/log、services、endpoints、events、nodes、
namespaces、deployments、replicasets、statefulsets、daemonsets、jobs、cronjobs、
ingresses、networkpolicies、hpa 等只读对象。**绝无** `create`/`update`/`patch`/`delete`,
也**没有** `pods/exec`、`pods/attach`、`pods/portforward` 等可产生副作用的子资源。

这从 Kubernetes API 层面兜底了「默认只读、LLM 无生产写权限」的设计约束。任何对该 ClusterRole
的变更都应作为高危项评审。control-plane / ai-worker / frontend 的 ServiceAccount 不绑定任何
RBAC(`automountServiceAccountToken: false`)。

## 6. CI(.github/workflows/ci.yml)

- **go**:matrix 跑 control-plane / cluster-agent 的 `go vet` + `go test -race`(`GOPROXY=goproxy.cn`)。
- **ai-worker**:`uv sync --extra dev` + `uv run pytest`(`UV_INDEX_URL` 阿里源)。
- **frontend**:`npm ci` + `npm run build`。
- **deploy-lint**:`helm lint` + `helm template`(dev/prod)+ kubeconform 校验清单与渲染产物。
- **db-tests(可选)**:起 `pgvector/pgvector:pg16` service,加载 `shared/sql/*.sql` 后跑 DB 相关测试。
- **docker**:构建四个镜像;仅默认分支登录并推送 GHCR(PR 只构建不推)。frontend 用
  `deploy/docker/frontend.Dockerfile`(构建上下文为仓库根)。

## 7. 备份与恢复(架构文档 §15.2)

目标:业务状态 **RPO ≤ 5 分钟**,控制面 **RTO ≤ 30 分钟**,月可用性 ≥ 99.9%。
四类有状态存储**分别备份**:

| 存储 | 备份方式 | 频率 | 恢复要点 |
|---|---|---|---|
| PostgreSQL(业务库,事实源) | 托管快照 + WAL 归档(PITR) | WAL 持续,快照每日 | 先恢复库再拉起 control-plane;它是唯一写入方,恢复后状态即一致 |
| Temporal(工作流) | 其后端 PG 快照 + WAL | 同上 | 恢复后 Worker 从检查点继续;进行中的调查可重放 |
| Kafka/Redpanda(事件总线) | topic 数据卷快照 / MirrorMaker 异地 | 视保留期 | 至少一次投递 + 幂等键消除重复副作用,少量重放安全 |
| 对象存储(证据快照) | 桶版本化 + 跨区复制 | 持续 | `evidence.raw_ref` 指向 object key,恢复桶即恢复原文 |

- 业务库为唯一事实源;Kafka/对象存储可容忍有界重放(幂等保证)。
- **每季度执行实际恢复演练**(真正拉起副本并验证一次完整调查),而非仅检查备份任务状态。
- 控制面完全不可用时,原监控告警链路仍独立工作(故障隔离,§15.1)。

## 8. 灰度发布(架构文档 §18.3)

模型 / Prompt / 工具 / 策略每次升级都要过离线回归 + 小流量 Canary:

1. **历史回放**:离线重放历史事故,对比 Top-1/Top-3 命中率与证据引用率。
2. **影子(Shadow)**:生产 Incident 触发新版本调查,结果**仅评测团队可见**,不进时间线。
   实现上可部署第二个 `control-plane`/`ai-worker` Deployment(不同 `AIOPS_MODEL_PROVIDER`/版本),
   消费同一事件但写独立结果集。
3. **建议模式**:向值班人员展示但不自动确认。
4. **Canary**:按副本比例灰度新镜像(如 `kubectl set image` 分批 / Argo Rollouts),
   `maxUnavailable: 0` 滚动,配合就绪探针与指标(§16)守门,异常即回滚。
5. **正式**:自动触发并进入 Incident 时间线。

发布门槛(§18.1):关键结论证据引用率 100%、未授权写操作 0、P95 首诊 < 5 分钟。

## 9. 故障排查

| 现象 | 处理 |
|---|---|
| cluster-agent 探针失败 | mTLS 开启时探针用 HTTPS scheme;确认 `aiops-agent-tls` 已挂载 `/certs` |
| control-plane 无法连 cluster-agent | 检查 `aiops-client-tls`、`AIOPS_CLUSTER_AGENT_URL` 是否 `https://`、CA 是否匹配 |
| ai-worker 一直 NotReady | 无 HTTP 探针,用进程存在性;查日志确认已连上 Temporal 并轮询 task queue |
| NetworkPolicy 阻断 Ingress→control-plane | 确认 ingress 控制器命名空间带 `kubernetes.io/metadata.name` 标签 |
| Pod 因 readOnlyRootFilesystem 崩溃 | 确认可写临时卷(frontend 的 /var/cache/nginx、/tmp;worker 的 /tmp)已挂载 |

