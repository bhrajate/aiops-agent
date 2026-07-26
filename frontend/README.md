# AIOps Incident Workbench (Frontend)

生产级 AIOps Agent 系统的前端值班台。采用 **Incident-first、Chat-second** 形态,
消费 control-plane 公共 API(`:8080`),提供 Incident 总览、调查视图与人工控制。

## 技术栈

- Vite + React 18 + TypeScript
- Tailwind CSS(深色 SRE 值班台主题)
- react-router-dom(路由)
- @tanstack/react-query(数据请求 / 轮询 / 缓存)
- lucide-react(图标)
- 原生 `EventSource` 订阅 SSE 实时时间线

## 目录结构

```
src/
  api/          # 类型(对齐 shared/schemas/contracts.md)+ 请求封装
    types.ts    # Incident / Investigation / Evidence / Hypothesis / DiagnosisResult ...
    client.ts   # fetch 封装、错误处理、Idempotency-Key 生成
    endpoints.ts# 各公共 API 端点封装
  components/   # Badges / 预算面板 / 假设卡片 / 诊断面板 / 时间线 / 反馈控制 / 证据弹窗 / Signal 注入
  hooks/
    useSSE.ts   # SSE 订阅 hook
    queries.ts  # react-query hooks
  pages/
    IncidentList.tsx      # 主界面:Incident 列表(status/severity 过滤)
    IncidentDetail.tsx    # 概览 + 调查入口
    InvestigationView.tsx # 假设 / 诊断 / 预算 / 实时时间线 / 人工控制
  lib/format.ts # 格式化辅助
```

## 快速开始

```bash
# 安装依赖(已内置淘宝镜像 .npmrc)
npm install

# 启动开发服务器(:5173,/v1 代理到 http://localhost:8080)
npm run dev

# 类型检查 + 生产构建
npm run build

# 预览构建产物
npm run preview
```

## 环境变量

复制 `.env.example` 为 `.env` 按需修改:

- `VITE_API_BASE`:公共 API 基地址。**留空**时走 Vite dev server 的 `/v1` 代理
  (本地开发推荐);独立部署时指向真实网关。

## 与后端联调

- dev server 已在 `vite.config.ts` 配置 `server.proxy`:`/v1` → `http://localhost:8080`。
- SSE:`GET /v1/investigations/{id}/events`(text/event-stream),前端用 `EventSource`
  订阅,自动重连;非终态阶段才建立连接。
- 发起调查:`POST /v1/incidents/{id}/investigations`,自动带 `Idempotency-Key` header。
- 人工控制:确认 / 纠错 / 关闭 → `POST /v1/investigations/{id}/feedback`;
  取消 → `POST /v1/investigations/{id}/cancel`。
- 首版只读:`remediation_proposal` 恒为 `null`,界面明确标注“无自动修复”。

### 列表 / 调查引用兼容说明

- Incident 列表兼容后端返回裸数组或 `{ items: [...] }` 等包裹形式。
- Incident 详情页从 `current_investigation_id` / `investigation_ids` /
  `investigations[]` 任一字段解析调查引用(契约未强制,做了容错)。若后端字段命名不同,
  调整 `src/pages/IncidentDetail.tsx` 的 `resolveInvestigationIds` 即可。
