# cluster-agent

每个 Kubernetes 集群部署一个的**只读 Cluster Agent**。它是一个 Go HTTP 服务,监听 `:9100`,向 Tool Gateway 暴露一组**类型化只读工具**,代理集群内的 Kubernetes / 指标 / 日志 / 链路 / 变更 / 拓扑查询。

契约来源:`docs/INTEGRATION.md`(Cluster Agent 工具协议)、`shared/schemas/contracts.md`、`生产级AIOps-Agent架构设计.md` §4.3 / §9。

## 设计约束

- **默认只读**:没有任何写操作 / exec / SSH。所有工具只做查询,`DataSource` 接口不提供任何变更方法。
- **可插拔数据源**:`internal/datasource.DataSource` 接口抽象后端。首版是**确定性 Mock**(同一 scope 永远返回同一份自洽证据),已为真实 client-go / Prometheus / Loki / Tempo 预留实现位(在 `main.go` 一处替换即可)。
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

## 配置(环境变量)

| 变量 | 默认值 | 说明 |
|---|---|---|
| `AIOPS_CLUSTER_AGENT_ADDR` | `:9100` | 监听地址 |
| `AIOPS_CLUSTER_ID` | `prod-cn-1` | scope 未带 cluster_id 时的回落值 |

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
cmd/cluster-agent/main.go        进程入口、配置、优雅退出(在此替换数据源实现)
internal/datasource/             DataSource 接口 + 共享请求/响应类型 + 确定性 Mock
internal/tools/                  工具注册表:工具名 → DataSource 方法 + 参数 schema
internal/server/                 net/http ServeMux 路由、scope 注入、错误处理
```
