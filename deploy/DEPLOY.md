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

### 2.1 生产护栏:两个必须显式声明的开关

`config.env` 与 `config.datasource` 决定**其余所有安全配置有没有人在检查**。
两者的代码默认值都是为本地零依赖开发准备的(`development` / `mock`),
而漏配的表现是:不报错、日志无异常、指标正常、`/readyz` 正常 —— 只有护栏不生效、
证据是编造的。基线 `values.yaml` 已取 production/live,`values-dev.yaml` 显式放松。

| 开关 | 生产值 | 漏配后果 |
|---|---|---|
| `config.env` → `AIOPS_ENV` | `production` | 控制面启动校验的**整个严格分支不执行**:auth disabled、默认/过短 HS256 密钥、缺 internal token 与 webhook secret、mock 观测源、未配观测后端、未配集群隔离,全部静默放行。fail-fast 退化成 fail-silent |
| `config.datasource` → `AIOPS_DATASOURCE` | `live` | cluster-agent 用 mock:`get_workload_state` / `get_kubernetes_events` / `list_recent_changes` / `inspect_dependencies` 返回**虚构但自洽**的故障数据,照常冻结成 Evidence 并进入诊断结论。evidence-grounding 只校验结论是否引用了证据,不校验证据是否真实 |

基线 `values.yaml` 已取 production/live,因此**chart 不再能无 values 文件裸装** ——
`helm install` 不带 `-f` 会因为缺 OIDC issuer/JWKS 与观测后端 URL 而拒绝启动。
这是刻意的:一个读生产观测数据、产出根因结论的系统,宁可拒启动也不该静默用假证据跑。
本地开发用 `-f aiops/values-dev.yaml`。

护栏本身也需要被验证 —— 一条永不执行的校验和一条正确的校验表现完全一样。
上线前(及每次改动 chart 后)跑:

```bash
bash scripts/check-prod-guards.sh   # 24 项,无需任何基础设施;已接入 CI
```

它对着**渲染后的清单**断言这两项,把渲染出的环境变量真的喂给二进制跑一遍校验,
并带反向用例(逐项抽掉必需项,必须被拒),以证明护栏不是空转。

单独 dry-run 一份配置(不连任何基础设施,退出码即结论):

```bash
# 集群内用同一镜像核对实际注入的环境变量
kubectl -n aiops exec deploy/control-plane -- control-plane validate-config
```

它会打印 `production=true/false` 与观测数据源的实际解析结果。
**若看到 `production=false`,说明这套部署的安全校验一条都没在跑。**

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
- **db-tests(可选)**:起 `pgvector/pgvector:pg16` service,用 `control-plane migrate up` 建 schema 后跑 DB 相关测试(与生产同一条迁移路径)。
- **docker**:构建四个镜像;仅默认分支登录并推送 GHCR(PR 只构建不推)。frontend 用
  `deploy/docker/frontend.Dockerfile`(构建上下文为仓库根)。

## 7. 数据库 schema 迁移

业务库 schema 由 **control-plane 自己管**:迁移 SQL 通过 `go:embed` 打进二进制
(`control-plane/internal/migrate/migrations/`),同镜像即同版本 SQL,不需要额外挂载。

### 7.1 生产:Helm hook Job 自动执行

`helm install` / `helm upgrade` 会先跑 `pre-install,pre-upgrade` hook Job
(`templates/migrate-job.yaml`,weight `-10`),再启动控制面副本。顺序是必需的:
控制面启动时**只校验版本、落后即拒绝启动**,不会自己迁移。

刻意不让控制面自迁移:滚动更新期间新旧副本共存,自迁移会让尚未替换的旧副本
面对新 schema。

失败的 Job **保留**(`hook-delete-policy` 不含 `hook-failed`):Pod 日志是定位
"哪条 SQL 失败、库是否停在 dirty 态"的唯一现场。

### 7.2 手工执行

```bash
# 集群内(与控制面同镜像)
kubectl -n aiops run migrate --rm -it --restart=Never \
  --image=ghcr.io/aiops/control-plane:v0.1.0 \
  --overrides='{"spec":{"containers":[{"name":"migrate","image":"ghcr.io/aiops/control-plane:v0.1.0",
    "args":["migrate","up"],"envFrom":[{"configMapRef":{"name":"aiops-config"}},
    {"secretRef":{"name":"aiops-secrets"}}]}]}}'

# 查看版本(不匹配或 dirty 时退出码 3,便于脚本判断)
... args: ["migrate","version"]
```

本地开发:`cd deploy && make migrate`(等价于 `control-plane migrate up`),
`make migrate-version` 查版本,`make seed` 灌开发种子(幂等)。

### 7.3 接管一个"已有表但无版本记录"的库

在迁移机制之前建的库(靠 compose 的 initdb 钩子,或手工灌 SQL)会有完整的表结构
但**没有 `schema_migrations` 表**,`migrate version` 报 `current=0`。

直接 `migrate up` 即可,不需要先 `force`:所有 DDL 都写成 `IF NOT EXISTS` /
`ADD COLUMN IF NOT EXISTS`,重放是幂等的。已在一份现网库的副本上验证:
升级后 `current=5`、incident 的 status 分布与原库一致、`superseded_by` 全为空
(即 `000003` 的归并未误关任何 incident——唯一索引已存在,说明本就无重复)。

若库的表结构与迁移文件**不一致**(手工改过 schema),`migrate up` 可能失败或
留下 dirty 态。此时按 §7.5 处理:人工对齐到某个干净版本后 `force` 标记。

### 7.4 并发安全

golang-migrate 用 PostgreSQL advisory lock 串行化:多副本/多 Job 同时执行会排队,
不会重复应用同一迁移。已验证 5 个并发 `migrate up` 终态正确且不 dirty。

### 7.5 迁移失败与 dirty 态

迁移中途失败会让版本表停在 `dirty=true`,此时控制面**拒绝启动**(这是正确行为:
带着半截 schema 跑会在第一次查询时炸,且更难定位)。恢复步骤:

1. 看 Job 日志确认失败的 SQL;
2. 连库确认哪些语句已生效、哪些没有,手工补齐或撤销,使库处于某个**干净版本**;
3. `control-plane migrate force <version>` 把版本表标到该版本并清 dirty
   (它**不执行任何 SQL**,只改版本表);
4. 重新 `migrate up`。

### 7.6 回滚镜像时的 schema 兼容性

`Expected` 版本编译在二进制里。回滚到旧镜像时,若新迁移不向后兼容,旧镜像会因
"schema 版本超前"拒绝启动。此时需先 `migrate down` 回到旧版本对应的版本号 ——
但 down 可能涉及**不可逆**操作,执行前必须确认备份可用(见 §8)。

已知不可逆点:`000003` 为建立 `correlation_key` 部分唯一索引,会把同 namespace 的
多条活跃 incident 归并为一条(保留 `last_seen` 最新者,其余置 `closed` 并用
`superseded_by` 指向保留者)。回滚**不会**把它们改回 `open`——无法区分"被归并而
关闭"与"本就该关闭",盲目改回会让已处理完的故障重新出现在值班列表。需要追溯时:

```sql
SELECT incident_id, superseded_by FROM incidents WHERE superseded_by IS NOT NULL;
```


## 8. 备份与恢复(架构文档 §15.2)

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

## 9. 灰度发布(架构文档 §18.3)

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

## 10. 故障排查

| 现象 | 处理 |
|---|---|
| cluster-agent 探针失败 | mTLS 开启时探针用 HTTPS scheme;确认 `aiops-agent-tls` 已挂载 `/certs` |
| control-plane 无法连 cluster-agent | 检查 `aiops-client-tls`、`AIOPS_CLUSTER_AGENT_URL` 是否 `https://`、CA 是否匹配 |
| ai-worker 一直 NotReady | 无 HTTP 探针,用进程存在性;查日志确认已连上 Temporal 并轮询 task queue |
| NetworkPolicy 阻断 Ingress→control-plane | 确认 ingress 控制器命名空间带 `kubernetes.io/metadata.name` 标签 |
| Pod 因 readOnlyRootFilesystem 崩溃 | 确认可写临时卷(frontend 的 /var/cache/nginx、/tmp;worker 的 /tmp)已挂载 |

