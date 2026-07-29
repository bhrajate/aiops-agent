# 优化记录(第三轮 review 后)

本轮起因:一次对照 [架构设计文档](生产级AIOps-Agent架构设计.md) 的全量 review。
结论是**骨架正确、工程基础扎实,但 RCA 能力链路上有一处关键断点**。

评分(review 时):工程基础设施 8.5/10,RCA 能力实质 5/10。
差距不在"没做完",而在最关键的一条链路没接上(F1)。

本文件记录**为什么这么改**和**权衡是什么**——这些信息不在代码注释里就会丢失。

## 缺陷清单与状态

| # | 缺陷 | 严重度 | 状态 |
|---|---|---|---|
| F1 | 模型无法参数化工具查询,`objective` 是死字段 | 最高 | ✅ 已修 |
| F2 | evidence-first 只在离线度量,运行时不强制 | 高 | ✅ 已修 |
| F3 | `blast_radius.services` 数的是资源数,误触发深度 RCA 闸门 | 高 | ✅ 已修 |
| F4 | 零数据保留策略,高写入表无界增长 | 高 | ✅ 已修 |
| F5 | Alertmanager `fingerprint` 解析后丢弃,幂等声明不成立 | 中 | ⬜ 待做 |
| F6 | 信号入口无任何限流 | 中 | ✅ 已修 |
| F7 | `EvaluateAuto` 四个分支全返回 true,是常量不是策略 | 中 | ⬜ 待做 |
| F8 | 离线评测门槛未接 CI | 中 | ✅ 已修 |
| F9 | CI 无任何安全扫描 | 中 | ✅ 已修 |
| F10 | 无业务成效与成本指标(MTTR / 采纳率 / token 费用) | 中 | ⬜ 待做 |
| P1 | **无任何数据库迁移机制**:DDL 只在数据卷首次创建时执行,生产托管 PG 无此钩子 | 阻塞 | ✅ 已修(第三轮) |
| P2 | 三个观测后端共用一个集群 label 名,而命名法互不兼容(点号是 PromQL 语法错误) | 高 | ✅ 已修(第三轮) |
| P3 | `/healthz` 状态码恒 200,readiness 无法摘除 DB 断连的副本 | 高 | ⬜ 待做 |
| P4 | 队列不可观测:无 outbox 深度 / lag / DLQ 存量,relay 卡住时静默失败 | 高 | ⬜ 待做 |
| P5 | 无 PrometheusRule / ServiceMonitor:指标存在但无人抓取告警 | 中 | ⬜ 待做 |

---

## F1 模型参数化工具查询 —— 本轮最重要的一项

**问题不是"少个功能",是能力被架空。** 守卫层一直支持模型自带查询:

```go
// obsquery/prometheus.go
expr, _ := args["expr"].(string)
if strings.TrimSpace(expr) == "" { expr = defaultLiveMetricExpr(...) }
scoped, err := scopePromQL(expr, required...)   // AST 注入,已测
```

但 worker 侧只发 `{"analyzer": "metrics"}`。于是**每次 `query_metrics` 都执行同一条
默认 PromQL**,Loki/Tempo 同理。Planner 产出的 `objective` 被送进 `analyze()` 当提示词,
却完全不影响取什么数据——五个 analyzer 退化为五个固定采集器,LLM 的角色是
"给一块固定看板写摘要"。

`ARCHITECTURE.md` 原先记录的取舍是"计划先定、逐个执行",对"选哪些工具"准确,
但**低估了实际边界**:不是"不能中途追问",而是连"这一轮问什么"都无法表达。

**修法**:`AnalyzerSpec.queries{tool: args}`,`TOOL_ARG_KEYS` 限定每工具可参数化的键。

**参数按"清洗"而非"拒绝"处理**:未知键/超长/非字符串一律丢弃、降级为后端默认查询;
但引用**未授权工具**仍是硬错误。理由:模型一处笔误不该让整轮采集失败,
而越权访问是完全不同性质的信号。

**Gateway 独立复制一份白名单**,不复用 worker 的过滤结果——Gateway 是策略边界,
不能假设调用方已经过滤过。

**范围仍不可协商**:cluster/namespace 由 obsquery 在 PromQL AST / LogQL 流选择器层
强制注入,越界 matcher 直接拒绝。模型能收窄"问什么",不能扩大"在哪问"。

真实基础设施上已确认生效(此前该字段恒为 `{"analyzer": ...}`):

```
query_metrics | {"expr": "sum by (version) (rate(http_requests_total{status=~\"5..\"}[5m]))", ...}
search_logs   | {"query": "{} |~ \"(?i)(exception|stack trace|5[0-9][0-9])\"", ...}
```

## F2 运行时强制 evidence-first

`has_supported_conclusion` 只看模型**自报**的 status。全仓库唯一实现
"supported 必须引用实时证据"的地方是 `evaluation/metrics.py`——那是离线评测。

于是存在这条路径:`status=supported` + `supporting_evidence_ids=[]`(或只引用 runbook)
→ CONCLUDED → `DiagnosisStatus.RESOLVED` → 前端展示"已确认根因",背后零实时证据。

**修法**:`policy.enforce_evidence_grounding`,放在 `policy.py` 是刻意的——
**检查不能交给被检查的模型**,必须是确定性代码。在**持久化之前**执行,
业务库(事实源)因此永不存在无据结论。降级保留 statement(仍是线索)但去掉断言,
并补记缺失项;计入 `usage.ungrounded_downgrades` + 发 `hypothesis_downgraded` 事件,
让"管线拒绝了一个未证实的根因"这件事可见,而不是静默发生。

离线评测管线 replay 同一守卫,避免线上线下口径分叉(否则评测可能放过运行时会拒的东西)。

## F3 blast_radius 语义

`services` 数的是**资源数**,且与 `groups` 同值——后者本身就说明这个字段没想清楚。
同一 Deployment 下 3 个 Pod 各自告警 → `services=3` → 命中
`policy.py` 的 `blast.get("services") > 1` → 单服务故障被判定为影响面扩大,
拉起昂贵的多轮 RCA。

**修法**:`model.ServiceKey` 把 Pod 归约到所属工作负载;`blast_radius` 拆为
`services` / `resources` / `groups` 三个维度。

**保守处理是有意的**:纯字母尾段不剥(如 `auth-service` 的 `service`)。
误剥会把不同服务合并成一个,比不剥更危险——前者让影响面**偏小**(漏报),
后者只是偏大(误报)。

顺带修 ingress 一处相关 bug:`Kind` 曾固定为 `"Deployment"` 而 `Name` 可能取自
`pod` 标签,于是 Pod 被标成 Deployment,`ServiceKey` 根本不会归约它。
现在 Kind 与 Name 来源一致,服务级标签优先。前端原先写 `services ?? resources`
兜底——那正是在掩盖这个 bug。

## F4 数据保留

22 个索引,`retention` / `PARTITION` / 定期清理**一条都没有**。
`signals`、`investigation_events`、`audit_log`、`evidence` 全部无界增长。

**两条不动摇的安全不变量**(有真实 DB 用例覆盖,风险全在 SQL 里,替身测不出来):
1. 活跃 incident(open/acknowledged)永不清理;
2. incident 已终态但**仍有未结束调查** → 不清理,避免运行中的 RCA 丢上下文。

其他决定:`outbox` 只删 `published`——`pending`/`failed` 是待重试事件,删掉等于丢事件;
`signals` 只删未归属 incident 的孤儿,已归属的随整案清理以保持可回溯。

单轮每目标批次有上限:即使积压很久也不会一轮跑几十分钟、长时间占着 advisory lock。
剩余部分下一轮继续(清理是幂等可重入的)。

**未采用分区表**:首版数据量下 range 分区的迁移与运维成本高于收益。
阈值已写进迁移注释——单表超 ~1e8 行应改为按 `created_at` 分区并 DROP 旧分区。

## F6 入口限流

**关键决定是按信号条数计费,不是按请求数**:一个 Alertmanager webhook 可以带
几百条告警,按请求计费等于形同虚设。

**为什么进程内而不是 Redis**:限流目的是挡住风暴打穿 ingress/DB/outbox,
不需要全局精确配额;引入 Redis 等于给信号入口加一个新的必经故障点
(Redis 挂了不能让信号入口跟着挂)。代价是每副本独立配额,
集群总容量 = 配置值 × 副本数——已写进配置注释。需要全局精确配额时换实现即可。

**`weight > burst` 的处理改过一次**。最初无条件放行(理由:否则大批量投递永远
无法通过)。`check-ratelimit.sh` 第 5 项立刻暴露这是个后门——把风暴打包成超大批量
即可完全绕过限流。现在改为**仅在桶已攒满时**放行一次并清空,两个性质同时成立:
不饿死(桶总会补满)、不可绕过(连续超大批量之间必须等桶重新攒满)。

另修一处自己写出来的 GC bug:空闲桶的 `tokens` 是过期快照(补充是惰性的),
用它判断"是否已满"会导致桶永不回收。判据改为"若此刻访问会不会已补满"。

## F8 / F9 CI 护栏

质量门槛建好了、CLI 明确写了 "usable in CI",但 CI 从未调用。
对 AI 系统来说这是最重要的回归护栏,不接等于没有。

**同时补了门槛自身的测试**——未测过的护栏与不存在的护栏无法区分。
逐项验证四个门槛能各自独立失败,并覆盖一个容易漏的退化场景:
管线什么都不结论时,引用率/幻觉率是**空真**(1.0 / 0.0),只有召回门槛抓得住,
否则"全部弃权"会假装通过。

**这道门当前的能力边界(已写进 CI 注释)**:golden case 是照着 mock provider 写的,
所以它守的是**管线不回归**(证据链、F2 降级、打分逻辑),**不是真实模型质量**;
接入真实 provider 后它才开始度量模型本身。不让绿勾暗示超出其实际范围的东西。

安全扫描在本地实跑三个扫描器验证(没跑过的 CI 步骤只是猜测),由此发现两个真问题:

- **Go stdlib 12 个"已调用"漏洞**:`go.mod` 钉 `go 1.26.1`,而 crypto/tls、net/http、
  net/textproto、crypto/x509、html/template 的修复在 1.26.2~1.26.5。
  镜像用 `golang:1.26` 浮动 tag 恰好取到已修补版本,但 **CI 的 setup-go 读 go.mod**
  → 会用带漏洞的工具链构建。两模块都加 `toolchain go1.26.5`,重扫降到 0。
- **vite 1 个 high**(路径穿越 / `server.fs.deny` 绕过)。只在构建期、不进产物,
  但选择**修依赖而不是放宽门槛**:升到 vite 7,tsc + build 通过,high 清零。

`npm audit` 卡在 high 而非 moderate:moderate 多来自构建期传递依赖,
卡那一档会让 CI 长期红着,反而没人看告警。

---

## 两个过程教训(比代码更容易重复踩)

**1. 假通过比失败更危险。** 本轮撞了两次:

- `check-ratelimit.sh` 第一版打到了 E2E 残留进程上,得到一次通过。
  现在脚本开头强制检查端口空闲。
- `check-two-tier` / `check-correlation-window` / `check-blast` 都不自己起后端。
  忘了起时 curl 静默失败,断言却照着库里**残留数据**打分——我因此一度把它读成
  "F3 引起的回归",回退到 `1e3cd38` 才发现同样失败,才意识到是脚本的问题。
  现在三者都 fail-fast + 开跑前清库,并新增 `with-backend.sh` 提供环境。
- `prod-e2e.sh` 直接跑 `bin/` 下的**预编译**产物,从不重编——我的 F3 改动
  因此没进那次 E2E,它在一个不存在的版本上跑出了绿色。现在脚本自己先 build。

共同点:**脚本"绿"了,但它验证的不是我以为的那个东西。**
凡是依赖外部状态(端口、进程、库内数据、编译产物)的验证,都要显式断言前提。

**2. 工具输出可疑时不要往下推。** 中途有一段 grep / build 返回了明显不合理的结果。
做法是停止基于可疑输出继续推进,改为直接读文件确认,并如实告知——
而不是把不确定的结论当成事实继续往上叠。

---

## 验证状态

| 范围 | 结果 |
|---|---|
| control-plane | build / vet / test 全绿(含 8 项真实 DB 用例) |
| cluster-agent | build / vet / test 全绿 |
| ai-worker | 112 passed / 3 skipped |
| frontend | tsc + build 通过(vite 7) |
| helm | lint + 生产渲染 + 角色拆分渲染通过 |
| prod-e2e | evidence 7 / 0 拒绝,diagnosis resolved |
| check-two-tier | 14/14 |
| check-correlation-window | 6/6 |
| check-blast | PASS(含 blast_radius 四维断言) |
| check-ratelimit | 7/7 |
| check-roles | 11/11 |
| 安全扫描 | govulncheck 0(两模块)、npm audit high 0、pip-audit 0 |

---

## 待做项(修法已定位)

### F5 Alertmanager fingerprint 幂等

`fingerprint` 被解析进 struct 后**全仓库再无引用**;`signal_id` 拼了随机数,
注释却写"幂等";`payload_hash` 无唯一约束。Alertmanager 在 5xx / 超时 /
`repeat_interval` 都会重投,每次产生新 `signals` 行并让 `alert_groups.signal_count + 1`。

影响面有限(`grouping_key` 仍会收敛到同一 incident,不会重复建 incident),
但 `signal_count` 是用户可见字段,又是 `signal_burst` 触发原因的判据。

**修法**:`signal_id` 由 `fingerprint + status + startsAt` 确定性推导(缺 fingerprint
时退回 `payload_hash`);`InsertSignalWithOutbox` 改 `ON CONFLICT DO NOTHING` 并返回
是否真的插入,ingress 据此区分 `accepted` / `duplicate`。
**验证**:同一 payload 投两次 → `signals` 只 1 行、`signal_count` 不变。

### F7 EvaluateAuto 是常量不是策略

四个分支全返回 true,只有 reason 字符串不同。P4 单信号也会起一次 triage 模型调用。

**修法**(二选一,倾向前者):给低价值 incident(如 P4 + 单信号 + 无变更关联)
一条真实的"不调查"路径并审计跳过原因;或者诚实改名,别假装有策略。

### F10 业务成效与成本指标

telemetry 只有信号/工具/拒绝计数,缺 MTTR、诊断准确率、人工采纳率、token/费用。
成本只落库、不可告警——对 LLM 系统这是最该看的指标。
`usage` 已有全部原始数据,缺的是导出为 Prometheus 指标。

---

## 需要你确认的一件事

**`AIOPS_CLUSTER_LABEL` 的真实值。** 生产启动现在强制要求设置它,
但默认填的 `cluster` 是否与你们中心 Prometheus / Loki / Tempo 实际使用的
label 名一致,我无法自行验证。常见变体:`cluster_id` / `k8s_cluster`,
Thanos/Mimir 可能是自定义 external_label,Tempo 侧常为 `k8s.cluster.name`。

**这个名字不对,注入的过滤条件就不生效**——共享后端下等于跨集群串数据。
告诉我实际名称,我把默认值与部署示例一起校准。

---

# 第三轮:生产前置项(P1 阻塞 + P2 配置类缺陷)

本轮起点是"当前项目是否能直接上生产"的评审。结论:**不能**,存在一个硬阻塞。
下面记录两项已闭合工作,以及评审识别但本轮未处理的项。

## P1 数据库迁移机制(硬阻塞,已闭合)

### 原问题

`shared/sql/` 下 6 个 DDL 文件,唯一执行路径是 docker-compose 把该目录挂到
postgres 的 `/docker-entrypoint-initdb.d`。而这个钩子**只在数据卷首次创建时生效**。

于是:

* 生产用托管 PostgreSQL(`DEPLOY.md` 自己写明),没有这个钩子 → **首次部署起不来**;
* Helm 里没有任何 Job 执行 SQL,`main.go` 启动时也不建表;
* 即使手工建了表,**没有任何地方记录"这个库跑到哪一版"** → 后续每次升级靠人记,
  早晚重复跑(报错)或漏跑(线上缺列)。

`RUNBOOK.md` 里原本还专门有一条排查项"改了 `shared/sql` 未生效",
说明这个坑在开发期就已经反复踩到,只是没被识别为生产阻塞。

### 决策

用 golang-migrate。选它而不是 atlas:控制面本来就是 Go,SQL 可 `go:embed` 进二进制,
不需要额外镜像或语言栈;atlas 的声明式模式对 pgvector 这类扩展类型要额外写声明,
而这个项目用不到它的 schema diff 能力。

**最关键的一条决策:迁移与启动分离。**

控制面启动时**只校验版本、落后即拒绝启动**,不自动迁移。理由是多副本滚动更新期间
新旧副本共存,自迁移会让尚未替换的旧副本面对新 schema。迁移是独立步骤
(Helm `pre-install,pre-upgrade` hook Job,weight `-10`)。
`AIOPS_AUTO_MIGRATE` 仅供开发/单副本,生产模式下开启会额外告警。

失败即拒绝启动而不是降级运行:带着不匹配的 schema 跑,会在第一次查询时炸,
且那时错误信息是"某列不存在",比"schema 版本落后"难定位得多。

错误信息按**落后 / 超前 / dirty** 三种情形分别给出可操作指引。"超前"值得单独说:
它通常意味着回滚了镜像但 schema 没跟着回滚,而不是配置错误。

不提供 `down-all`:一次性删库不应该只差一条命令。

### 过程中发现的真实缺陷

`000003`(两层告警聚合模型)新建的 `uniq_incidents_active_correlation` 要求活跃
incident 的 `correlation_key` 唯一。但**旧模型允许同一 namespace 有多条活跃
incident**——这正是两层模型要消除的碎片化。所以任何有存量数据的库,
必然存在重复 `correlation_key`,升级必然失败。

这个缺陷**空库迁移测试结构性地发现不了**,只有第一次真实生产升级才会暴露。
我是刻意构造了一个"已有两条同 namespace 活跃 incident"的库才撞出来的。
这条经验值得记下:迁移的正确性测试必须包含**带存量数据的升级**,
而不只是"空库能否建出正确的终态 schema"。

修法是建索引前先归并:每个 `correlation_key` 保留 `last_seen` 最新的一条,
其余标记 `closed` 并用新增的 `superseded_by` 指向保留者。
**刻意不删除**——值班人员可能正在看被归并的 incident,直接删会让链接 404;
保留 + 指针可追溯,也让"我的 incident 为什么突然关闭了"有答案。
对应的 down 明确声明该归并不可逆,并给出追溯 SQL。

### 顺带修掉的种子幂等性缺陷

`shared/seed/001_knowledge.sql` 写了 `ON CONFLICT DO NOTHING`,但它**永远不触发**
——`knowledge_id` 默认 `gen_random_uuid()`,每次执行生成新主键,没有任何约束可冲突。
实测连跑两遍从 3 行变 6 行。这个守卫是装饰性的。

影响不止"表里多几行":重复 runbook 会被 RAG 反复检索、挤占 context 预算,
并让同一份知识在证据里出现多次,**看起来像"多个独立来源支持同一结论"**。

修法是把幂等性做成 **schema 约束**(`000005` 加 `(tenant_id, kind, title)` 唯一索引)
而非脚本内去重:任何写入路径都受约束,不必指望每个调用方自己记得去重。
选这三列而非仅 title:不同 kind 允许同名,多租户下各租户知识库互不干扰。

与 `000003` 同一个坑——已重复跑过种子的库直接建唯一索引必然失败,故索引前先去重。
这里可以安全**删除**重复行(内容完全相同,是脚本 bug 的产物),
不像 incident 那样承载独立业务语义。

同时修正一处误判:`golden_cases` 从来没有重复问题,它的 `case_id` 是显式的
(`gc-release-001` 等),`ON CONFLICT (case_id)` 本来就有效。只有 `knowledge_items` 坏了。

### 验证

对真实 PostgreSQL 逐项验证,而非只跑单测:

| 场景 | 结果 |
|---|---|
| 空库 → v5 | 16 张表 |
| 重复 `migrate up` | 幂等,报 already up to date |
| 四步 `down` → v0 | 只剩 `schema_migrations` |
| 5 个并发 `migrate up` | advisory lock 串行化,终态 v5 且不 dirty |
| dirty 态 `force` 修复 | 成功,可继续 up |
| **带存量数据升级** | 归并正确:保留者 open,另一条 closed 且 `superseded_by` 指向它 |
| 带重复种子的库升 v5 | 去重正确(6→3),索引建立成功 |
| 种子连跑三遍 | 行数稳定(knowledge=3 golden=5) |
| 强制退回 v3 后启动控制面 | 拒绝启动,退出码 1,给出可操作指引 |

## P2 集群 label 按后端拆分(已闭合)

### 原问题

三个后端共用同一个 `clusterLabel` 字段,而 `client.go` 自己的注释就写了
"Tempo 侧常为 `k8s.cluster.name`"——写注释时已知命名法不同,却只留了一个配置项。

这不只是"默认值不好"。**点号在 PromQL/LogQL 里是语法错误**,不是"查不到数据"。
所以两种配法都是坏的,无法调参绕过:

* 配 `cluster` → Tempo 静默返回空结果;
* 配 `k8s.cluster.name` → Prometheus/Loki **每次查询都语法错**。

### 调研结论

业界**没有统一标准**。成熟做法是按后端可配 + 各自默认值——Grafana 自家 mixin
就用 `per_cluster_label` 把它暴露为可配项,而不是硬编码。

| 后端 | 惯例 | 注入方式 |
|---|---|---|
| Prometheus / Mimir | `cluster` | `external_labels` |
| Loki(Alloy/Promtail) | `cluster` | relabel |
| Loki(OTLP 原生) | `k8s_cluster_name` | OTel resource attribute 提升为索引 label;LogQL label 名须符合 Prometheus 命名法,故点号转下划线 |
| Tempo | `k8s.cluster.name` | OTel 语义约定,原生保留点号 |

### 决策

照业界做法实现:`AIOPS_{PROM,LOKI,TEMPO}_CLUSTER_LABEL`,回落链为
后端专属 > `AIOPS_CLUSTER_LABEL`(全局,向后兼容)> 各后端内置默认值。

**校验按两类配错的不同表现分流**,这是设计的核心:

* **语法非法**(点号给了 Prom/Loki):启动时即可判定 → fail-fast,
  指名具体环境变量并给出改法(点号转下划线)。不拖到运行期每次查询才失败。
* **名字合法但不匹配**(实际 `cluster_id` 却配了 `cluster`):后端不报错、
  只静默返回空结果。**代码里判定不了** → 只能靠文档要求上线前核对。
  故 `INTEGRATION.md` 给出三个后端各自的核对命令,并说明为何这类最难排查:
  RCA 跑完了,证据一条也没采到,日志里没有任何异常。

单集群场景改为要求**显式** `AIOPS_CLUSTER_LABEL_DISABLED=true`,不再"留空即关闭":
留空静默不隔离会让 RCA 读到其他集群同名 namespace 的数据,
而这个错误**在诊断结论里看不出来**——证据齐全、逻辑自洽,只是来自错误的集群。
这类"看起来完全正常的错误"必须要求显式表态。

### 顺带修掉一个自己引入的死代码

原打算逐后端告警 unenforced。但各后端都有非空默认值,经 env 路径唯一能出现
unenforced 的是显式 `DISABLED`,而该分支又被守卫排除——**写了个永不执行的循环**。
用一个临时测试确认不可达后,改为在真正可达的 `DISABLED` 路径上给一条醒目告警。

`EnvVarFor` 用显式映射而非字符串拼接:拼接对 `prometheus` 会生成不存在的
`AIOPS_PROMETHEUS_CLUSTER_LABEL`,让运维去改一个没用的变量。已用测试锁死映射。

### 顺带补上 Helm 路径的另一个真实缺口

chart 里**完全没有**观测后端 URL——`values.yaml` / `values-prod.yaml` /
`configmap.yaml` 三处都缺。而生产校验要求至少配一个后端,
所以**用 Helm 装生产会直接启动失败**。裸 k8s 清单有这些键,只有 Helm 路径坏了。
已补三个 URL 与四个集群 label 键,`values-prod` 附核对命令。

### 澄清一处此前的疑虑

`obsquery/tempo.go` 用纯名字 `k8s.namespace.name=` 是**对的**。
Tempo legacy `/api/search?tags=` 就是 logfmt 纯名字格式,
`resource.` 前缀只用于 TraceQL 的 `q` 参数。此处无缺陷。

## 评审识别但本轮未处理

| 项 | 问题 | 修法 |
|---|---|---|
| P3 | `/healthz` 在 store 降级时只改响应体、状态码恒 200,而 readiness/liveness 都指向它 → DB 断连的副本不会被摘出 Service endpoints,继续接流量报 500。两个探针共用一个语义本身也不对:DB 挂了应摘流量,重启进程修不了数据库 | 新增 `/readyz`,degraded 返 503,readiness 指它;`/healthz` 保留 200 给 liveness |
| P4 | 队列不可观测:已导出 10 个指标全是**计数器**,没有 outbox 待投递深度 / 消费者 lag / DLQ 存量。outbox relay 卡住时 `/v1/signals` 照样 202、signals 计数照涨,但 incidents 不再增长,**前端看着一切正常**,`DeadLetters` 也不动(它只在彻底放弃时才 +1)。这是最危险的静默失败 | 用 Collector 在抓取时查库(不用后台轮询);查询失败**不上报**该指标而非上报 0——0 会被误读为"队列是空的",缺失则让告警规则的 `absent()` 生效 |
| P5 | `deploy/` 下无 PrometheusRule 也无 ServiceMonitor:指标存在但没人抓、没人告警 | 随代码一起发布规则,保持规则集精简,每条对应一个具名故障模式并附 runbook 指引 |

前两轮遗留的 F5 / F7 / F10 仍未处理(修法见上文各节)。其中 **F7 与 F10 建议在
放开自动触发前处理**:`EvaluateAuto` 四个分支全返回 true,每个 incident 都消耗
一次 triage 模型调用(含 P4 单信号);而 `usage` 有 token 数据却从未导出到
Prometheus——既没节流,也没仪表。

## 过程教训

本轮**文件读取工具多次返回损坏内容**(重复行、不存在的键、错乱行号)。
处理方式:凡受影响的判断,一律改用真正解析文件的命令交叉验证
(`helm lint`、`go build`、`gofmt`、`yaml.safe_load`),编辑改用脚本而非依赖读回结果。

具体收益:靠 `helm template` + `yaml.safe_load` 才确认 Job 渲染正确(目视读回的
内容是重复错乱的);靠一个临时测试才确认那段告警循环不可达。
**读回内容不可信时,用解析器复核,不要目视比对。**
