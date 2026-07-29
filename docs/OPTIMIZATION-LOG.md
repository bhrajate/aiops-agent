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
| F5 | Alertmanager `fingerprint` 解析后丢弃,幂等声明不成立 | 中 | ✅ 已修(第五轮) |
| F6 | 信号入口无任何限流 | 中 | ✅ 已修 |
| F7 | `EvaluateAuto` 四个分支全返回 true,是常量不是策略 | 中 | ✅ 已修(第五轮) |
| F8 | 离线评测门槛未接 CI | 中 | ✅ 已修 |
| F9 | CI 无任何安全扫描 | 中 | ✅ 已修 |
| F10 | 无业务成效与成本指标(MTTR / 采纳率 / token 费用) | 中 | ✅ 已修(第五轮) |
| F11 | `ClassifyFault` 匹配标签名,几乎所有告警被判为发布回归 | 高 | ✅ 已修(第五轮,F7 端到端发现) |
| F12 | 重复投递仍无条件写 outbox,`signal_count` 虚增 | 高 | ✅ 已修(第五轮,F5 端到端发现) |
| P1 | **无任何数据库迁移机制**:DDL 只在数据卷首次创建时执行,生产托管 PG 无此钩子 | 阻塞 | ✅ 已修(第三轮) |
| P2 | 三个观测后端共用一个集群 label 名,而命名法互不兼容(点号是 PromQL 语法错误) | 高 | ✅ 已修(第三轮) |
| P3 | `/healthz` 状态码恒 200,readiness 无法摘除 DB 断连的副本 | 高 | ✅ 已修(第四轮) |
| P4 | 队列不可观测:无 outbox 深度 / lag / DLQ 存量,relay 卡住时静默失败 | 高 | ✅ 已修(第四轮) |
| P5 | 无 PrometheusRule / ServiceMonitor:指标存在但无人抓取告警 | 中 | ✅ 已修(第四轮) |
| P6 | `IncIncident` 定义了却从未调用,`incidents_created` series 根本不存在 | 中 | ✅ 已修(第四轮,P5 校验器发现) |
| C1 | 拓扑/服务图关联:`topology_refs` 有列无写入路径 | 能力 | ✅ 已实现(第六轮) |
| C2 | 反馈闭环学习:反馈持久化但从不回流为评测用例 | 能力 | ✅ 已实现(第六轮) |
| C3 | 主动异常检测:系统完全被动,缓慢退化看不见 | 能力 | ✅ 已实现(第六轮) |

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

---

# 第四轮:上生产前的最后三项(P3 / P4 / P5)

三项都已闭合。它们围绕同一个主题:**让故障可见**。上一轮的结论是"能部署了",
这一轮解决的是"部署后出问题能不能发现"。

## P3 探针语义分离(已闭合)

### 原问题

`/healthz` 在 store 降级时**只改响应体、状态码恒 200**,而 readiness 与 liveness
都指向它。kubelet 只看状态码,所以数据库断连的副本永远"探针通过",不会被摘出
Service endpoints —— 继续接流量,然后每个请求 500。

### 决策

拆成两个端点,因为它们回答不同的问题:

| 端点 | 回答 | 行为 |
|---|---|---|
| `/readyz` | 现在能处理请求吗 | 关键依赖不可用 → 503 → 摘出 endpoints,恢复后自动放回 |
| `/healthz` | 进程还活着吗 | 恒 200,**不查任何依赖** |

**liveness 绝不能查数据库。** 数据库挂了重启进程修不了数据库,只会:所有副本
同时进入 CrashLoopBackOff(恢复后还要等退避)、丢掉进程内状态(限流令牌桶)、
并把最需要的日志冲掉。liveness 只该检测重启确实能修的问题。

依赖分 Critical / 非 Critical:Temporal 与对象存储不可用时控制面仍能接收信号、
聚合 incident、提供查询,把这类副本摘掉只会让可用性更差 —— 故标 `degraded` 但仍
ready。这个区分不是过度设计:它决定了"部分降级"时是否触发流量转移。

### 验证方式上的一条经验

`scripts/check-probes.sh` **真的停掉 postgres**,而不是只测正常路径 —— 正常路径
此前也是通的,测它证明不了任何东西。9/9 通过,含"恢复后自动放回"。

调试中踩到一下:上一次手工测试刚停过 postgres,脚本启动时进程在 schema 校验处就
退了,于是九条断言全得到 `000`。原始脚本会等满 40 轮才报一堆 `000`,完全看不出
根因。已改为进程退出即摊开日志 —— **断言拿到空值时,先怀疑前置条件而不是被测对象**。

## P4 队列可观测性(已闭合)

### 原问题

导出的 10 个指标**全是计数器**,没有任何"当前存量"。于是 outbox relay 卡住时:
`/v1/signals` 照样 202、signals 计数照涨,但 incidents 不再增长,**前端看着一切正常**;
`aiops_dead_letters_total` 也不动 —— 它只在彻底放弃投递时才 +1,而"卡住"恰恰是
还没放弃。

这是最危险的一类静默失败:所有既有信号都指示健康,唯一的异常是"某些东西不再发生"。
而"不再发生"没有对应的指标。

### 两条关键决策

**1. 主指标是最老待投递年龄,不是条数。** 告警风暴下积压几千条但几秒排空是正常的;
只积压 3 条却卡了 20 分钟才是故障。按条数告警会在风暴时误报、在真卡住时漏报
—— 正好把两种情形都搞反。

**2. 查询失败时不上报任何队列 gauge,只上报 `scrape_failed=1`。**
上报 0 会被读成"队列是空的",恰好在最需要告警时给出虚假的正常。缺失才能让告警
规则的 `absent()` 把"监控本身坏了"表达为一个**独立于**"队列健康"的状态。
空队列则**要**上报 0 —— 它是已知事实(查成功了,结果是 0),与"不知道队列多深"
必须能区分。三种情形各有用例锁住。

用 `prometheus.Collector` 在抓取时查库,而不是后台轮询 + `Gauge.Set()`。
轮询的问题是**失败时 Gauge 里留着上一次的成功值**:数据库挂了、查询一直失败,
而仪表盘上显示的还是昨天那个健康数字。已用测试锁住"每次抓取都查库"。

### 测出来才知道的两处

* `EXTRACT(EPOCH FROM ...)` 返回 `numeric`,pgx 不会扫进 `float64`,
  必须显式 `::double precision`;
* `min(created_at)` 在空队列上是 NULL,需要 `COALESCE`,否则空队列直接扫描失败。

用例同时锁住"`failed` 计入待投递" —— 与 `DrainOutbox` 取件条件一致,漏掉会让
"卡在重试"这一最常见的卡住形态在指标上完全不可见。

`scripts/check-queue-metrics.sh` 同样真的停掉 postgres,12/12:含"四个 gauge 全部
缺失且 `scrape_failed=1`"、"其余指标仍正常"(证明是队列查询失败而非端点挂了)、
"DB 恢复后自动回来"。

## P5 告警规则与采集(已闭合)

### 原问题

`deploy/` 下既无 PrometheusRule 也无 ServiceMonitor —— 指标存在但没人抓、没人告警,
等于埋在进程里。P4 做完了指标,不接告警就还是没人知道。

### 决策

七条规则各对应一个**具名故障模式**,而不是给每个指标配一个阈值。后者只会产出
没人看的告警,最后被整体静音 —— 那比没有告警更糟,因为它还占着"我们有监控"的名分。

`AiopsQueueMetricsMissing` 用 `absent(oldest_pending_age_seconds)` 而非
`absent(outbox_pending)`:后者是带 label 的 gauge,**空队列时本就缺失**,
对它 `absent()` 会在正常运行时一直告警。这条规则让 P4 的"查询失败不上报 0"
有了消费者。

### 校验器抓到的真实缺陷

`scripts/check-alert-rules.sh` 渲染 chart → 起控制面抓**真实 /metrics** →
用真实 PromQL 解析器对账。抓两类目视发现不了的问题:

1. 语法错误 —— Prometheus 加载时整组拒绝规则;
2. **引用不存在的 series —— 不报错、规则永不触发。** 这类更危险。

它第一次运行就抓到:`telemetry.IncIncident` **定义了却从未被任何代码调用**,
于是 `aiops_incidents_created_total` 这个 series 根本不存在,
我刚写的 `AiopsSignalsWithoutIncidents` 会永不触发。已在 `incident.Manager` 接上。

**这正是"对账基准必须是真实输出而非代码常量"的理由。** 用常量名对账等于自己和
自己核对,抓不到"常量定义了但没人调"。与上一轮那个永不执行的告警循环是同一类问题
—— 都是"代码存在"被当成了"功能存在"。

也验证了校验器**本身**能抓到这两类并 exit 1:一个什么都放过的校验器等于没有。

### 脚本调试中的一个非显然点

固定 namespace 会让**上一次运行**留下的活跃 incident 把本次信号相关性合并进去
(不新建),`incidents_created` 就不出现、被误判为未导出。清库解决不了 ——
incident 是在本次运行**中途**创建的,而清理发生在启动前。改为每次运行用唯一
namespace(`$$`),从根上消除这类干扰。

这和前几轮"断言前不确认前置状态"是同一个根因的又一种形态:这次干扰源不是别的
脚本,而是**这个脚本自己的上一次运行**。

## 缺陷清单更新

| # | 缺陷 | 状态 |
|---|---|---|
| P3 | `/healthz` 状态码恒 200,readiness 无法摘除断连副本 | ✅ 已修 |
| P4 | 队列不可观测,relay 卡住时静默失败 | ✅ 已修 |
| P5 | 无 PrometheusRule / ServiceMonitor | ✅ 已修 |
| P6 | `IncIncident` 定义了从未调用,`incidents_created` series 不存在 | ✅ 已修(P5 校验器发现) |

## 仍未处理

前几轮遗留的 **F5 / F7 / F10** 仍在。其中 **F7 与 F10 建议在放开自动触发前处理**:

* **F7**:`EvaluateAuto` 四个分支全返回 true,每个 incident 都消耗一次 triage
  模型调用(含 P4 单信号)。它是常量不是策略。
* **F10**:`usage` 有 token 数据却从未导出到 Prometheus —— 既没节流,也没仪表。
  P4/P5 补上了**队列**的可观测性,但**成本**仍然不可见。

三个结构性空白(拓扑关联、反馈闭环学习、主动异常检测)仍在,属于产品能力而非
生产就绪项。

## 过程教训

**这一轮三项都用"真的把依赖弄坏"来验证,而不是只测正常路径。** P3 停 postgres、
P4 停 postgres、P5 故意写坏表达式验证校验器自身。理由是这三项修的都是
"故障时行为不对"——正常路径在修之前就是通的,测它无法区分"修好了"和"没修"。

**校验基准要用真实输出,不要用代码里的常量。** P5 的校验器正因为对账真实
`/metrics` 才抓到 `IncIncident` 从未被调用。这个原则上一轮就吃过一次亏
(永不执行的告警循环),这次是它的第二种形态。

---

# 第五轮:清掉最后三项遗留(F5 / F7 / F10)

生产就绪项在第四轮已清空,这一轮处理前几轮识别但一直未做的三项。
主题是**成本与正确性**:前两项直接影响模型调用量,第三项让这件事可度量。

## F5 Alertmanager 幂等(已闭合)

### 原问题

`fill` 的注释写着"幂等:同一 payload 短时间内重复投递生成相同前缀 + 时间片",
实现却是 `hex(payloadHash[:8]) + "-" + randHex(4)` —— **随机后缀保证每次重投递都得到
不同 `signal_id`**,于是 `ON CONFLICT (signal_id) DO NOTHING` 永不冲突。
去重机制早就在库上就位,是这个随机后缀把它废掉的。
而 Alertmanager 解析出的 `fingerprint`(正是它提供的稳定身份)被读出来后直接丢弃。

Alertmanager 是至少一次投递,重投递是**预期行为**而非异常。

### 决策:身份 = fingerprint + status + startsAt

三者各有必要,少一个就错:

| 成分 | 少了会怎样 |
|---|---|
| `fingerprint` | 退化为 payload 哈希,无关字段(generatorURL/annotations)变化就绕过去重 |
| `status` | firing 与 resolved 折叠成一条,丢掉"已恢复"这个事实 |
| `startsAt` | 恢复后再次故障被当成重投递吃掉,**丢掉第二轮故障** |

`fingerprint` 是对**标签集**的 fnv64a 哈希,标识"哪条告警"而非"哪一次通知" ——
官方明确多条 alert 可共享同一 fingerprint、要求接收方自行处理。
时间统一到 UTC、status 统一小写:否则上游换时区或大小写就绕过去重。

### 端到端才暴露的第二处缺陷

去掉随机后缀后 signals 表已正确去重到 1 行,但 `incidents.signal_count` **仍是 5**。

原因:`InsertSignalWithOutbox` 在 `ON CONFLICT` 跳过插入后**仍无条件写 outbox**,
重复投递各自发布一次事件,Incident Manager 每收一次就 +1。
而 `signal_count` 正是 `EvaluateAuto` 的 `signal_count >= 3` 判据的输入 ——
于是**一条告警重投三次就被当成"信号突发"**,拉起不必要的深度 RCA。

这个缺口单看代码不会发现:两行代码各自都对(ON CONFLICT 正确、enqueue 正确),
错的是它们的组合。**只有对着真实数据库跑一遍才看得见。**

改为仅在真正插入时入队。两者在同一事务内,enqueue 失败会连带回滚 insert,
不会留下"有行无事件"。`signals_ingested` 计数器同理只在新信号时 +1。

## F7 EvaluateAuto 从常量变成策略(已闭合)

### 原问题

四个分支**全部返回 true**,只产出不同的 reason 字符串。名字、注释和
`Decision.Trigger` 字段都暗示"会拦一些",实际一条也不拦。
每个 incident 都消耗一次 triage 模型调用,含 P4 单信号。

### 为什么现在能收紧

调查内部还有第二道闸门(`evaluate_deep_rca_policy` 决定是否从快速分诊进入深度 RCA)。
两道闸门问的是不同问题:

* 内层:**挖多深**;
* 外层:**这个 incident 值不值得花一次模型调用**。

外层一律放行等于放弃成本控制的第一道防线。

### 决策

判据按优先级:P1/P2 无条件 → 变更关联 → 信号突发(≥3)→ 影响面已扩大 →
可跳过集合(默认仅 P4)→ 兜底触发。

两条刻意的保守选择:

* **P3 不列入可跳过。** 它是最常见级别、混着不少真问题,拦它会显著改变值班预期。
  需要更省由部署方显式加。
* **未知/非法严重度兜底触发。** 拿不准时宁可多花一次调用,
  也不要静默跳过一个可能重要的故障。

**跳过必须留痕**:写 `trigger_skipped` 审计 + `aiops_trigger_decisions_total`
(按 triggered/reason 分维度)。跳过不等于忽略 —— incident 仍入库、仍在前端可见、
仍可人工发起调查;但若不留痕,"为什么这个故障没有诊断"将无从回答,
**静默丢弃比不拦更糟**。按 reason 分维度是关键:只看总量无法回答"跳过的都是些什么",
而那正是调阈值时唯一需要的信息。

### 端到端才暴露的、让整个策略形同失效的缺陷

验证时发现指标里**只有 `recent_change_correlation` 一种 reason** —— 策略等于没生效。

根因在 `ClassifyFault`:它把 `labelBlob` 拼成 `"key=value key=value ..."` 后做子串匹配,
于是**标签名参与判定**。而几乎每条真实 K8s 告警都带 `deployment=<名字>`,
其中含 `deploy`,于是无条件命中 `release_regression` 分支。

后果有两层,第二层更隐蔽:

1. `fault_category` 是变更关联判据的输入 → **每个 incident 都因"变更关联"被触发**;
2. `fault_category` 会下发给 planner → 把 RCA 的先验偏向"发布回归",
   而真实根因可能完全无关。**这一层在诊断结论里看不出来** ——
   结论自洽、证据齐全,只是从错误的方向开始查。

现有测试只用 `alertname` 标签,**结构性地碰不到**这个情形。
修法是只匹配标签**值**,并把范围类标签(namespace/cluster/pod/deployment 等)
排除在变更关键词匹配之外 —— 否则一个叫 `deploy-tools` 的 namespace
会让该空间下所有告警都被判为发布回归。

## F10 成效与成本指标(已闭合)

### 原问题

`investigations.usage` 里一直有 tokens / cost_usd / elapsed_sec / tool_calls /
ungrounded_downgrades,但**从未导出到 Prometheus**。成本只能查库逐条累加;
诊断结论的采纳率数据躺在 `human_feedback` 表里,没有任何聚合视图。

P4 解决了"系统是否在工作",这一项解决"**工作得值不值**"。

### 四条决策

**1. 成本同时用 Counter 与 Histogram。** 二者答不同问题:Counter 答"这个月花了多少"
(`rate()` 看烧钱速度),Histogram 答"是否有单次调查异常昂贵"。
只有 Counter 时,一次失控的调查会被整体均值稀释掉。

**2. 人工反馈按 action 分维度,不预先算比率。** 采纳率 =
`confirm / sum(confirm,correct,reject)`,PromQL 现算即可。固化成比率会丢掉分子分母,
而"**低采纳率**"与"**没人给反馈**"是完全不同的问题 —— 合成一个数就分不开了。

**3. 刻意不导出 MTTR。** 它混合了系统性能与人的响应速度(值班多久看到、多久动手),
当系统指标会得出错误结论。导出的是 `diagnosis_latency_seconds`
(调查开始→首次结论,纯系统耗时)。真正的 MTTR 应由事件管理侧统计,
那里才有人工响应的时间戳。

**4. `ungrounded_downgrades` 单独导出。** 它是模型质量信号(模型声称已确认却拿不出
实时证据,被 evidence-first 守卫降级),不是成本维度。
**应在放开自动化范围之前先看它** —— 它直接度量"结论有没有事实支撑"。

### 零值一律跳过观测

tokens/cost 为 0 通常是中途上报或降级路径(模型未被调用)。计入会把分布拉向 0
并压低 P99 —— 于是"某次调查异常昂贵"这件事在指标上被稀释掉,
而那正是这个直方图唯一的用途。时延为 0 同理:观测 0 会让"诊断变慢"在指标上消失。
这与 P4 的"查询失败不上报 0"是同一个原则的两种应用:
**不知道 ≠ 是零**,把二者混为一谈会让指标在最需要它时给出虚假的正常。

### 告警的排查顺序

三条成本告警的 runbook 刻意把"看触发策略的 reason 分布"和"看单次费用 P99"
放在"调模型"之前 —— 前两者更常是真因。
"无证据结论增多"一节明确写出两种原因处置完全不同,且**观测后端查不到数据更常见**:
集群 label 配错会静默查空,让模型反复尝试却拿不到证据,
表现为"模型质量下降"但实际是配置问题。

## 缺陷清单更新

| # | 缺陷 | 状态 |
|---|---|---|
| F5 | Alertmanager `fingerprint` 解析后丢弃,幂等声明不成立 | ✅ 已修 |
| F7 | `EvaluateAuto` 四个分支全返回 true,是常量不是策略 | ✅ 已修 |
| F10 | 无业务成效与成本指标(MTTR / 采纳率 / token 费用) | ✅ 已修 |
| F11 | `ClassifyFault` 匹配标签名,几乎所有告警被判为发布回归 | ✅ 已修(F7 端到端验证时发现) |
| F12 | 重复投递仍无条件写 outbox,`signal_count` 虚增 | ✅ 已修(F5 端到端验证时发现) |

至此前五轮识别的全部缺陷已闭合。

## 仍未处理(产品能力,非缺陷)

三个结构性空白,属于"还没做"而非"做错了":

* **拓扑/服务图关联**:`topology_refs` 有列无写入路径。相关性合并目前只按
  tenant|cluster|namespace,跨服务依赖链上的故障传播识别不了。
* **反馈闭环学习**:人工反馈已持久化,但从不回流为 golden case 或 runbook 更新。
  采纳率现在可度量了(F10),但度量之后的改进仍是手工的。
* **主动异常检测 / SLO 燃尽**:目前完全是信号驱动的被动响应。

## 过程教训

**这一轮两处最重要的缺陷都是端到端验证时发现的,单测结构性地发现不了。**

* F12(`signal_count` 虚增):两行代码各自都对,错的是它们的组合。
  单测会分别 mock 掉另一半。
* F11(`ClassifyFault` 匹配标签名):现有单测只用 `alertname` 标签,
  从没试过带 `deployment` 标签的真实告警 —— 而那是每条真实 K8s 告警都有的。

共同点:**缺陷都在"真实输入的形状"里,不在逻辑分支里。** 补的用例因此都刻意用
完整的真实标签集,而不是最小构造。这与第四轮"校验基准要用真实输出而非代码常量"
是同一条经验的另一面 —— 输入和输出两侧都不能用自己构造的简化版来自我核对。

另外重复了一次已知的坏习惯:在测试里手写数字格式化函数(两处),
标准库 `strconv` 就有。测试代码里的手写工具函数是纯粹的风险 ——
它自己可能有 bug,而它的职责恰恰是发现 bug。两处都已改回标准库。

---

# 第六轮:三个结构性空白(拓扑关联 / 反馈闭环 / 主动异常检测)

前五轮清掉的是**缺陷**(声明与实现不符)。这一轮做的是**能力**:设计文档里提过、
数据模型里留了位置,但从没实现的三件事。

## 拓扑关联(已闭合)

### 原问题

`incidents.topology_refs` 从 000001 起就有这一列,**从来没有写入路径**。
相关性合并只按 tenant|cluster|namespace,调用链上的故障传播识别不了:
checkout 挂了导致 payment-api 超时,值班人员看到两个互不相关的 incident,
根因只有一个 —— 得他们自己在脑子里把这两条连起来。

### 决策一:边来自 Tempo service graph,不用 K8s Service selector

selector 只表达**入口**关系(哪个 Service 选中了哪个工作负载),回答不了
"checkout 调用了谁"。而 Tempo 的 metrics-generator 从 trace 的父子 span 推导真实调用,
导出 `traces_service_graph_request_total{client,server,connection_type}`,
每条时间序列就是一条边。

这些指标落在 Prometheus 里,而控制面已经连了 Prometheus —— 所以这条同步
**不需要任何新基础设施,也不需要 cluster-agent 参与**。

### 决策二:不合并 incident,只建立"疑似同源"链接

合并的诱惑很大(值班人员只看一个),但风险不对称:

* `correlation_key` 上有部分唯一索引,跨 namespace 合并会破坏它;
* 更要紧的是**一条误判的边会把两次无关故障焊死成一个 incident,而拆分比合并难得多**
  —— 已写入的 signal 与证据没法回滚归属,时间线也无法还原。

链接给出同样的信息(且带方向:上游那个更可能是根因),但可以随时撤销,
两边各自保留独立时间线与影响面。

### 决策三:两级置信度

这是本项最实质的取舍:

| 用途 | 阈值 | 理由 |
|---|---|---|
| 进 `topology_refs` | 0.5 | 只是给 planner 更多上下文,错了代价小 |
| 链接 incident | 0.8 | 会出现在值班人员界面上,错了会**误导排查方向** |

Tempo 真实调用边 0.9(够链接),K8s selector 边 0.7(只够进 refs)。
两个阈值相等就失去了这个分级 —— selector 边会把"同一 Service 后的两个无关工作负载"
判为同源。

### 最容易静默失效的一处

**服务名口径。** 拓扑边存裸名(Tempo 的 `client`/`server` 就是裸名),而 incident 的
资源可能是 Pod(`checkout-7d9f4b8c6d-x2k9p`)。不归约就永远匹配不上,
而**别处看不出任何异常**:incident 照常创建、诊断照常产出,只是 `topology_refs`
永远是空的。

故新增 `model.WorkloadName`(与 `ServiceKey` 的区别仅在无 namespace 前缀),
并用两条用例分别锁住"Pod 要归约"和"不能带 `/` 前缀"。后者尤其容易在重构时被
"统一用 ServiceKey"的好意改坏。这与 F3 修过的 `blast_radius` 是同一类坑。

### 新增的内部查询路径

`obsquery.InternalInstantQuery` 是一条**非工具**路径。工具路径强制注入 namespace
约束(表达式可能来自模型),而拓扑同步是服务端自己发起、天生跨 namespace,
硬套 namespace 只会查不到东西。

安全边界写在文件头并用测试锁住:**该方法不接受调用方提供的表达式**,PromQL 硬编码
在代码里。一旦允许传表达式,它就成了绕过范围注入的后门。集群维度仍强制,
且复用同一套 AST 注入而非另写一份 —— 另写会让两条路径的行为漂移。

### 可观测性

同步查到 0 条边时**告警一次**(状态变化才打,避免每周期一条淹日志):
未启用 metrics-generator 时拓扑关联完全不生效,而那在别处毫无线索。
`aiops_topology_edges` 用 Gauge(存量;突然掉到 0 正是 generator 挂了的信号),
失败时刻意**不更新**它 —— 写 0 会与"真的没有边"混淆。这与 P4 的
"查询失败不上报 0"是同一个原则。

## 反馈闭环(已闭合)

### 原问题

`human_feedback` 表从 000001 起就在收反馈,schema 注释也写明"先进入审核队列,
审核后才能成为 Golden Case",但**从来没有实现那条通路**。反馈躺在表里,
既不回流为评测用例也不改进 runbook —— 系统学不到任何东西。

而读取端(`evaluation/store.py`)一直按 `review_status='approved'` 过滤:
**消费方早就准备好了,只缺生产方。**

### 决策一:一律写 pending,并把默认值也改成 pending

`review_status` 的原默认值是 `'approved'` —— 与"先进审核队列"的设计意图相反。
任何忘记显式设置的写入方都会让用例**未经审核直接进入评测集**。

而评测集决定发布质量门槛,一条错误标注会让门槛失真,且这种失真**极难发现**:
门槛照常通过或照常失败,只是标准错了。默认值应指向安全的一侧。

自动提升省掉的是"从头写一条用例"的工作量,**不是"确认它对不对"的责任**。

### 决策二:审核权限只给 sre/admin

值班人员在故障处置中提交反馈是本职,但决定"什么算正确答案"应由更少的人负责。
审核**不可翻转** —— 反复改会让"评测集当前包含什么"变得不可知;要改判就 reject 后新建。

### 决策三:期望关键词用确定性切分,不用模型抽取

评测集的期望值必须稳定,否则评测结果会随抽取模型的版本漂移 ——
那会让"这次发布是否退化"失去意义(分不清是模型退化还是标准变了)。

切不出来时兜底为整条根因:**空的 `expected_top_causes` 会让该用例在评测里恒判命中**
(没有期望就无法不命中),比没有用例更糟。长度阈值按 rune 数算 ——
中文根因("连接池耗尽")按字节会误判为够长。

### 其他

* 同一次调查只产出一条用例。反馈可以来多次(先 correct 再 confirm),但描述同一次
  故障;不去重会让一次故障在评测集里占多个席位,等于加权,而评测集的意义在于
  覆盖多样的故障类型。
* `signal_fixture` 不存整个 incident:它含 `updated_at`/`last_seen` 等易变字段,
  会让回放不可复现。信号存根限 20 条 —— 风暴产生的 incident 可能有上千条。
* 补 provenance 列:人工标注的 golden case 是**资产**,必须可追溯到人和时间,
  否则几个月后没人敢动它(不知道删掉会不会丢掉重要的回归覆盖)。

## 主动异常检测(已闭合)

### 原问题

系统完全**被动**:只在告警流入时才有反应。没有告警规则覆盖的缓慢退化
(错误率从 0.05% 爬到 0.4%,不触发任何静态阈值)完全看不见,直到变成用户投诉。

### 决策一:选 SLO 燃尽率,不选统计异常检测

统计方法(3σ / 时序分解 / 孤立森林)听起来更"智能",但对这个系统是错的选择:

* **不可解释。** 能说"偏离基线 4.2σ",说不出"所以呢"。而本系统的产出是给人看的
  诊断结论,一个无法解释的触发理由会污染整条推理链;
* **误报率高且难调。** 季节性、发布窗口、流量自然波动都会触发。而误报会训练值班
  人员忽略告警 —— 比没有检测更糟;
* **与用户影响脱钩。** 偏离基线 ≠ 用户受损。

燃尽率的优点正好相反:可解释(消耗了 2% 的月度预算)、阈值有业界共识
(SRE workbook 表 5-8)、直接对应用户影响。

### 决策二:多窗口,参数照 workbook

单窗口的已知问题是错误停止后长窗均值要过整个窗口才降下来,告警持续 fire 一小时。
短窗(长窗的 1/12)作为"仍在燃烧"的确认:**长窗超但短窗未超 = 燃烧已停止,不触发**。

一次燃烧同时满足多档时只报最严重的 —— 14.4× 必然也满足 1×,全报会让同一次故障
发三条 signal(workbook 里的"三条通知"问题)。

### 决策三:合成 signal 走既有入口

不直接建 incident:走既有入口才能自动获得两层聚合、触发策略、幂等去重、审计。
一条新路径会把这些全绕过,还会产出一类"不像别的 incident"的 incident,
前端与 RCA 都要额外处理。

### 最微妙的一处:燃烧片段身份

`signal_id` 由 fingerprint + startsAt 决定(F5)。两种直觉做法都错:

* 每轮用 `now()` → 持续燃烧每轮产出新 signal,`signal_count` 暴涨并误触发
  `signal_burst`(正是 F5 修过的坑);
* 用固定值 → 恢复后的再次燃烧被当成重投递吃掉,**丢掉第二次故障**。

故引入"燃烧片段":同片段内 `startsAt` 不变(去重),新片段有新 `startsAt`(新故障)。
进程内状态重启即丢 —— 可接受(两层聚合会并进同一 alert_group,只是 `signal_count`
多 1),落库的代价大于收益,已注明。

`signal_id` 推导复用 `model.DeriveSignalID`(从 api 包上移):两条路径必须共用一套
幂等规则,否则合成信号与告警信号的去重行为不一致,而这种不一致只会在生产的重复
数据里显现。

### 无数据 ≠ 越限

服务刚上线或指标名写错都会走到无数据分支,当越限处理会产出假故障。
多序列取最大值而非平均 —— 取平均会让局部故障被健康维度稀释掉。
启动时不立即评估:长窗最长 3 天,刚启动拿到的是不完整窗口的结果,
会在部署后立刻产出一批假故障。

### 配置 fail-fast

SLI 定义任一条非法即**整体**拒绝启动:部分生效会让运维以为都在监视,
而实际有一个 SLO 静默没在看。`$WINDOW` 占位符缺失时明确报错 ——
没有它多窗口退化成"同一窗口比两次",两个条件恒同真同假,短窗过滤完全失效
而**表现上一切正常**(告警照常触发,只是不再防抖)。

## 过程教训

**这一轮最大的时间花在验证脚本身上,而三个问题都是同一类:断言拿着不可信数据。**

`check-slo-burnrate.sh` 连撞三次:

1. `host.docker.internal` 在 WSL 下抓不到 —— 所有断言拿到空值。改为 exporter 也跑成
   容器、同网络互访,并**加抓取目标健康预检**:失败时直接摊开 `lastError`,
   而不是让 7 条断言各报一次空值。
2. "namespace 下存在 incident"被残留数据满足 —— 实测拿到与上一轮**失败运行**完全
   相同的 `incident_id`。改为经 `signals.incident_id` 反查,断言 incident 确由本条
   SLO signal 产生。
3. 前置清库用单条多语句 `psql -c`,外键阻塞导致**整批回滚**、连 signals 的删除一起
   撤销,清库静默失效 —— 而这一次它先造成假通过("1 条"),修了关联断言后又造成
   假失败("2 条")。同一个根因,两种相反的表现。

第 3 条尤其值得记:`psql -c` 是单事务,多语句里任一条失败会回滚全部。
其他脚本用 `TRUNCATE ... CASCADE` 恰好绕过了这个问题,所以之前没暴露。

**另外确认了一处"不是缺陷"的失败**:`check-feedback-loop.sh` 原用随机 namespace,
而 bob 的 ABAC 范围只有 payment/cart,反馈被正确拒掉。改用 payment 后顺带验证了
"oncall 在自己范围内可提交反馈"。

## 缺陷与能力清单更新

| 项 | 状态 |
|---|---|
| 拓扑/服务图关联(`topology_refs` 有列无写入路径) | ✅ 已实现(第六轮) |
| 反馈闭环学习(反馈持久化但从不回流) | ✅ 已实现(第六轮) |
| 主动异常检测 / SLO 燃尽 | ✅ 已实现(第六轮) |

前五轮的全部缺陷 + 这三项能力空白均已闭合。

## 仍需部署方确认的事

三处配置只有部署方知道,配错都会**静默失效**(不报错、日志无异常):

1. **三个观测后端的集群 label 名**(前几轮遗留)。默认 `cluster`/`cluster`/
   `k8s.cluster.name`,写错则查询静默返回空。
2. **Tempo 是否启用了 metrics-generator 的 service-graphs**。未启用则拓扑关联
   完全不生效(有一次告警,但只在启动后第一轮打)。核对:
   `count(traces_service_graph_request_total)`。
3. **SLI 定义**。只有部署方知道"什么算错误请求"。未提供则 SLO 监视不做任何事
   (启动时会告警)。
