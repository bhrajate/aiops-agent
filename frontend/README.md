# AIOps 值班台(Frontend)

生产级 AIOps Agent 的前端值班台。Incident-first:入口是"现在有什么在烧",不是聊天框。
消费 control-plane 公共 API(`:8088`)。

## 技术栈

- Vite + React 18 + TypeScript
- Tailwind CSS + 语义化 CSS 变量 token(明暗双主题)
- react-router-dom(路由)
- @tanstack/react-query(轮询 / 缓存 / 失效)
- lucide-react(图标)
- 原生 `EventSource` 订阅 SSE 实时时间线
- 图表手写 SVG(见下)

## 页面

| 路由 | 内容 | 权限 |
|---|---|---|
| `/` | 值班总览:未闭环计数、级别/状态/类别/阶段分布、24h 趋势、成本、管道健康 | 全部 |
| `/incidents` | 告警列表,URL 同步筛选,行内认领 / 发起调查 | 全部 |
| `/incidents/:id` | 概览 + 告警聚合明细 + 疑似同源关联 + 调查入口 | 全部 |
| `/investigations` | 跨 incident 的调查队列,标出疑似卡住项 | 全部 |
| `/investigations/:id` | 假设 / 诊断 / 证据 / 预算 / 实时时间线 / 人工判定 | 全部 |
| `/golden-cases` | 待审评测用例的批准 / 驳回 | sre · admin |
| `/knowledge` | 知识库检索(与 Agent 做 RAG 的同一入口) | 全部 |
| `/audit` | 审计日志,游标翻页,被拒绝访问高亮 | sre · admin |
| `/settings` | 主题、当前会话的 RBAC/ABAC 范围、安全边界说明 | 全部 |

`⌘K` / `Ctrl+K` 打开命令面板:路由跳转,或粘贴 `inc-` / `inv-` 前缀的 ID 直达
(值班时告警群里贴的就是 ID)。

无权限的侧边栏入口整个隐藏,而不是点进去再看 403。**这只是体验优化,后端对每个请求
独立强制** —— 前端放宽不会造成越权。

## 快速开始

```bash
# 安装依赖(已内置镜像源 .npmrc)
npm install

# 开发服务器(:5173,/v1 代理到 http://localhost:8088)
npm run dev

# 类型检查 + 生产构建
npm run build
```

开发登录用 control-plane 的 dev 用户(`AIOPS_AUTH_MODE=hs256` 时可用):
`alice/alice-pass`(sre,全范围)、`bob/bob-pass`(oncall,仅 payment+cart)、
`viewer/viewer-pass`(只读)。三者范围不同是刻意的 —— 切换账号能直接看到 ABAC 生效。
演示凭证只在 `import.meta.env.DEV` 下注入,不进生产 bundle。

## 主题与 token

配色**全部走语义 token**,定义在 `src/index.css` 的 `:root.dark` / `:root.light`,
由 `tailwind.config.js` 映射成 `bg-card` / `text-muted` / `bg-p1` 这类 class。
切换靠 `<html>` 上的 `.dark` / `.light` 类(`src/store/theme.ts`)。

不用硬编码色阶(`bg-zinc-900` 之类)是刻意的:那样补浅色主题时需要逐个 class 写
`html.light` 重映射。从 token 起步可以完全避开这笔债。

新增颜色请加 token,不要直接用 Tailwind 调色板 —— 后者在浅色主题下不会跟着翻。

## 目录结构

```
src/
  api/
    types.ts     # 对齐 shared/schemas/contracts.md 与后端 model
    client.ts    # fetch 封装、401/403 事件、Idempotency-Key
    endpoints.ts # 各端点封装
  auth/          # AuthProvider、token 持久化、路由守卫
  store/
    theme.ts     # 主题三态(跟随系统 / 深色 / 浅色)
    ui.ts        # 侧边栏折叠、命令面板开关
  components/
    ui.tsx       # Card / Button / StatCard / SegmentedControl / Callout …
    charts.tsx   # BarDistribution / TrendChart / Sparkbars
    Sidebar.tsx  CommandPalette.tsx  Layout.tsx
    …            # Badges / 预算 / 假设 / 诊断 / 时间线 / 反馈 / 证据弹窗
  hooks/
    useSSE.ts    # SSE 订阅
    queries.ts   # react-query hooks
  pages/         # 见上表
  lib/format.ts  # 时间 / 成本 / 计数 / 中文枚举名
```

## 几处需要知道的约定

**样本不足显示破折号,不显示 0。** MTTR 为 0 会被读成"秒级解决",真相是没有样本。
后端在这些字段上返回 `null`(`mttr_seconds`、`p95_investigation_seconds`、`queue`),
渲染时不要 `?? 0`。`queue` 为 null 时界面明确说"查询失败,这不代表队列是空的" ——
0 恰好掩盖 outbox 卡死。

**分布数组不要在前端重排。** 后端已定序(级别按 P1→P4,其余按计数降序)。按计数重排
会把 P4 放到 P1 前面,而值班台第一眼要看的是有没有 P1。

**进行中的耗时用挂钟时间,不用 `usage.elapsed_sec`。** 后者由 worker 上报,worker 挂了
就不再更新 —— 而那正是要看见的情况。

**"卡住"判定线是 10 分钟**,与后端 `overview.go` 的 `stallThreshold` 及
`InvestigationList.tsx` 的 `STALL_MS` 三处一致。改一处要改三处,否则总览说"3 个卡住"
而列表里标出 5 个。

**阶段是否终态**在 `components/Badges.tsx` 的 `ACTIVE_PHASES`,须与后端
`overview.go: terminalPhases` 及 `sse_feedback.go: isTerminal` 一致(后端有测试断言
后两者不漂移)。漂移会让面板说有调查在跑、详情页却已收到 done。

**图表手写 SVG 而非引 recharts。** 后者 gzip 后仍约 35KB,而值班台常在带宽受限的跳板机
上打开。分布用横条不用饼图:5 个以上分类时饼图无法比较相邻扇区,而"P2 比 P3 多多少"
正是要判断的。条长按最大值归一化而非总数 —— 后者在 20 个分类时每条都占 5%,
全都短得看不出差别。

## 与后端联调

- dev server 在 `vite.config.ts` 配 `server.proxy`:`/v1` → `http://localhost:8088`。
- SSE 只在非终态阶段建连:终态后服务端会发 `done` 并关流,继续订阅只会拿到一次立即
  断开的连接。数据库是事实源,SSE 断开时轮询仍在兜底。
- `POST /v1/incidents/{id}/investigations` 自动带 `Idempotency-Key`。
- `POST /v1/signals` 用 webhook HMAC 鉴权而非用户 JWT,401 不触发跳登录
  (`skipAuthRedirect`),由 UI 说明真正原因。
- 首版只读:`remediation_proposal` 恒为 `null`,诊断面板做成显式声明而不是省略 ——
  值班人员需要知道"系统不会自己动手",而不是猜它有没有权限。

### 容错说明

- 列表兼容后端返回裸数组或 `{ incidents: [...] }` / `{ items: [...] }` 等包裹形式。
- Incident 详情从 `current_investigation_id` / `investigation_ids` / `investigations[]`
  任一字段解析调查引用(契约未强制,做了容错)。见 `pages/IncidentDetail.tsx` 的
  `resolveInvestigations`。
- `alert_groups` / `relations` 与 incident 平级返回,在 `endpoints.ts` 合并进对象。

## 安全取舍(已知,待后端支持后改)

token 存 localStorage 便于跨标签页共享与 SSE(`EventSource` 无法设请求头),但对 XSS
暴露;SSE 的 token 走 query string,可能被写入网关访问日志。更稳妥的做法是后端下发
HttpOnly + Secure + SameSite Cookie,或为 SSE 下发短时一次性 ticket。见
`src/auth/store.ts` 与 `src/hooks/useSSE.ts` 的注释。
