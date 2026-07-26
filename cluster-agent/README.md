# cluster-agent

每个 Kubernetes 集群部署一个的**只读 Cluster Agent**。它是一个 Go HTTP 服务,监听 `:9100`,向 Tool Gateway 暴露一组**类型化只读工具**,代理集群内的 Kubernetes / 指标 / 日志 / 链路 / 变更 / 拓扑查询。

契约来源:`docs/INTEGRATION.md`(Cluster Agent 工具协议)、`shared/schemas/contracts.md`、`生产级AIOps-Agent架构设计.md` §4.3 / §9。

## 设计约束

- **默认只读**:没有任何写操作 / exec / SSH。所有工具只做查询,`DataSource` 接口不提供任何变更方法。
- **可插拔数据源**:`internal/datasource.DataSource` 接口抽象后端。提供两种实现,由 `AIOPS_DATASOURCE` 选择(默认 `mock`):
  - **`mock`**(默认):确定性 Mock,同一 scope 永远返回同一份自洽证据,无任何 I/O。
  - **`live`**:真实只读数据源 —— Kubernetes(`client-go`)+ Prometheus / Loki / Tempo(标准库 HTTP)。见下文「Live 真实数据源」。
- **范围注入**:每次调用由 Tool Gateway 传入 `scope`(cluster/namespace/resource/time_range)。`cluster_id` 缺省时回落到 Agent 配置的集群;`namespace` 必填。

## HTTP 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | `{"status":"ok"}` |
| GET | `/tools` | 工具清单 + 每个工具的参数 JSON schema |
| POST | `/tools/{tool_name}` | 调用某个工具 |

调用体与返回体:

```jsonc
// 请求
{ "arguments": { /* 工具参数,可空 */ },
  "scope": { "cluster_id": "prod-cn-1", "namespace": "payment", "resource": "checkout",
             "time_range": { "from": "...", "to": "..." } } }

// 200 返回
{ "source": "prometheus", "summary": "中文自然语言摘要", "raw": { /* 结构化证据 */ }, "freshness": "10s" }

// 4xx 返回
{ "error": { "code": "unknown_tool", "message": "..." } }
```

错误码:`invalid_request`(体无法解析,400)、`invalid_scope`(namespace 缺失,400)、`unknown_tool`(404)、`tool_error`(500)。

## 工具清单

| 工具 | source | 作用 |
|---|---|---|
| `get_workload_state` | kubernetes | Deployment/ReplicaSet/Pod 健康、版本分布、就绪副本 |
| `get_kubernetes_events` | kubernetes | 最近事件(BackOff/OOMKilling/Unhealthy 等) |
| `query_metrics` | prometheus | PromQL 风格指标(错误率、延迟、CPU),按版本分序列 |
| `search_logs` | loki | 关键错误日志行 |
| `get_traces` | tempo | 调用链与瓶颈 span |
| `list_recent_changes` | change-intel | 发布 / 配置 / 基础设施变更(一等证据) |
| `inspect_dependencies` | topology | 上下游依赖边(错误率、延迟、来源、置信度) |

> `retrieve_runbook` 由 control-plane 直接查 Knowledge Service,不经 cluster-agent,故本服务不实现。

## Mock 故障场景

Mock 按 `namespace`/`resource` 映射到一个自洽的故障故事,覆盖设计文档四类故障。所有工具从同一场景推导,证据可串成一条链:

| namespace(示例 resource) | 故障类别 | 故事 |
|---|---|---|
| `payment`(`checkout`) | 发布回归 → 依赖超时 | v2.3.0 上线,**仅新版本实例** 5xx 从 0.1% 升到 8.2%;`list_recent_changes` 显示同发布把连接池 `max_idle_conns` 由 200 下调为 20;日志现连接池耗尽 + 对 `payment-gateway` 请求超时;trace 显示瓶颈在该下游调用。 |
| `cart`(`cart-session`) | Pod 异常 | ConfigMap 把 `-Xmx` 改到 256m,容器启动即 `OOMKilled`,`CrashLoopBackOff`,Ready 副本为 0。 |
| `inventory`(`stock-api`) | 资源瓶颈 | 流量增长 3 倍未扩容,CPU 打满并被 throttle,p99 由 300ms 升到 4200ms。 |
| 其他(如 `orders`) | 依赖超时传播 | 自身无变更,延迟/错误来自下游 `auth-service` 超时并沿调用链传播,伴随熔断打开。 |

旗舰场景 `payment/checkout` 完整复现设计文档的“发布回归 → 新版本 5xx 升高 → 连接池配置变更 → 依赖超时”链条。

## Live 真实数据源

`AIOPS_DATASOURCE=live` 时启用真实只读后端。每个上游**独立可配**,未配置时对应工具**优雅降级**:返回 `source` 带 `/unavailable` 后缀、`raw.available=false` 的 Result,不 panic、不报错,便于部分部署下平滑上线。

| 工具 | 后端 | 只读访问方式 |
|---|---|---|
| `get_workload_state` | Kubernetes | `Deployments().Get` + `Pods().List`(标签选择器),统计就绪副本与镜像 |
| `get_kubernetes_events` | Kubernetes | `Events().List`,按 `involvedObject.name` 过滤目标资源 |
| `list_recent_changes` | Kubernetes | `ReplicaSets().List`,按 `deployment.kubernetes.io/revision` 还原发布/镜像版本历史 |
| `inspect_dependencies` | Kubernetes | `Deployments().Get` + `Services().List`,用 Service selector 匹配出上游入口边 |
| `query_metrics` | Prometheus | `GET /api/v1/query_range`(标准库 HTTP) |
| `search_logs` | Loki | `GET /loki/api/v1/query_range`(标准库 HTTP) |
| `get_traces` | Tempo | `GET /api/search`(标准库 HTTP) |

### 只读铁律(READ-ONLY)

Live 实现从三个层面保证只读,详见 `internal/datasource/live.go`、`kubernetes.go` 顶部注释:

1. **Kubernetes**:只调用 `Get` / `List`,从不构造 `create/update/patch/delete/exec/attach/portforward`,`rest.Config` 仅用于构建读客户端,代码中不封装任何 write verb。client-go 显式设 `QPS=20 / Burst=40`,限制对 API Server 的客户端侧速率(纵深防御)。
2. **Prometheus / Loki / Tempo**:仅访问各自的查询 GET 端点(query_range / search),无 remote-write、admin、delete-series 调用。上游响应用 `io.LimitReader`(32 MiB)封顶后再解码,防止上游把 Agent 撑爆(OOM)。
3. **优雅降级**:上游 URL 或 K8s 客户端缺失、或目标资源 `NotFound` 时返回 `unavailable` Result,不影响其余工具、不误报 500。

### scope 强制注入(namespace 隔离)

`scope` 由 Tool Gateway 传入,cluster-agent **在数据源层强制执行**(不依赖 Gateway 裁剪,见 `internal/datasource/live_scope.go`):

- **Kubernetes**:所有查询走 Namespaced 客户端,只落在 `scope.namespace`。
- **Prometheus / Loki**:默认查询按 `namespace="<ns>"` 构造;调用方自定义 `expr` / `query` 时,向**每个** `{ ... }` 选择器强制注入 `namespace="<ns>"`,并**拒绝**引用其它 namespace 或使用非精确 `namespace` 匹配(`!=` / `=~` / `!~`)的表达式;缺少 `{ }` 选择器(无法限定)的表达式也一并拒绝。
- **注入安全**:`namespace` / `resource` 名先按 DNS-1123 字符白名单校验,无法用引号/花括号等突破 PromQL/LogQL 语法。
- **时间窗**:窗口被夹紧为正向且不超过 24h;Prometheus `step` 按窗口自适应(样本点数 ≲1000)。

### 请求体 / 头部限制(DoS 防护)

- `POST /tools/{tool_name}` 请求体经 `http.MaxBytesReader` 封顶 1 MiB,超限返回 `413 request_too_large`。
- `http.Server.MaxHeaderBytes` 设为 1 MiB。
- 未注册的工具名不进入 Prometheus label:`tool` label 仅取白名单内真实工具名,其余统一记为 `unknown`,杜绝匿名高基数攻击。

### mTLS 服务端(SECURITY §3)

cluster-agent 作为 TLS 服务端,可要求并校验调用方(control-plane)的客户端证书(`RequireAndVerifyClientCert`)。开关关闭时维持明文(开发)。`healthz` 在两种模式下均可用。

## 配置(环境变量)

| 变量 | 默认值 | 说明 |
|---|---|---|
| `AIOPS_CLUSTER_AGENT_ADDR` | `:9100` | 监听地址 |
| `AIOPS_CLUSTER_ID` | `prod-cn-1` | scope 未带 cluster_id 时的回落值 |
| `AIOPS_DATASOURCE` | `mock` | 数据源模式:`mock` \| `live` |
| `AIOPS_PROM_URL` | (空) | live 模式 Prometheus 基址,如 `http://prometheus:9090` |
| `AIOPS_LOKI_URL` | (空) | live 模式 Loki 基址,如 `http://loki:3100` |
| `AIOPS_TEMPO_URL` | (空) | live 模式 Tempo 基址,如 `http://tempo:3200` |
| `AIOPS_KUBECONFIG` | (空) | live 模式 kubeconfig 路径;为空时用 in-cluster,再回落 `~/.kube/config` |
| `AIOPS_AGENT_TLS_ENABLED` | `false` | 开启 mTLS 服务端 |
| `AIOPS_AGENT_TLS_CERT` | — | 服务端证书(PEM) |
| `AIOPS_AGENT_TLS_KEY` | — | 服务端私钥(PEM) |
| `AIOPS_AGENT_TLS_CLIENT_CA` | — | 校验客户端证书的 CA 包(PEM) |

## 构建与运行

```bash
make build      # 编译到 bin/cluster-agent
make run        # 直接运行(:9100)
make test       # 单元测试
make vet        # go vet
make fmt-check  # gofmt 检查
make check      # fmt-check + vet + build + test 一键门禁
make docker     # 多阶段构建镜像(scratch 运行时)
```

## 本地验证

```bash
make run    # 另开一个终端

curl -s -XPOST localhost:9100/tools/query_metrics \
  -H 'Content-Type: application/json' \
  -d '{"arguments":{},"scope":{"cluster_id":"prod-cn-1","namespace":"payment","resource":"checkout"}}'
```

预期返回 `source=prometheus`,`summary` 说明“仅新版本 v2.3.0 实例 5xx 从 0.1% 升至 8.2%”,`raw.series` 含新旧两条版本序列。

## 目录结构

```
cmd/cluster-agent/main.go              进程入口、配置(AIOPS_DATASOURCE + mTLS)、优雅退出
internal/datasource/
  datasource.go                        DataSource 接口 + 共享请求/响应类型
  mock*.go / scenario.go               确定性 Mock 数据源(默认)
  live.go                              Live 数据源:FromEnv 选择、降级、方法调度、只读文档
  live_prometheus.go / live_loki.go / live_tempo.go   标准库 HTTP 只读客户端
  kubernetes.go / kubernetes_query.go / kubernetes_topology.go  client-go 只读查询
internal/tools/                        工具注册表:工具名 → DataSource 方法 + 参数 schema
internal/server/
  server.go                            net/http ServeMux 路由、scope 注入、错误处理
  tls.go                               mTLS 服务端 tls.Config 构建(SECURITY §3)
```

## 测试覆盖

- **Prometheus / Loki / Tempo**:`net/http/httptest` mock 上游,验证 URL/path/query、响应解析、summary 生成与错误路径(`live_http_test.go`)。
- **Kubernetes**:`client-go/kubernetes/fake` fake clientset 验证工作负载状态、事件过滤、ReplicaSet 版本历史排序、Service selector 依赖匹配(`kubernetes_test.go`)。
- **降级**:未配置任何上游时全部工具返回 `unavailable` 且不 panic。
- **mTLS**:测试内自签 CA/服务端/客户端证书,端到端验证「持证客户端可访问 / 无证书被握手拒绝」(`server/tls_test.go`)。
