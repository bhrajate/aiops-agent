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
