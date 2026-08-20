// 通用格式化 / 展示辅助。

import type { BlastRadius } from '@/api/types'

export function cn(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ')
}

export function formatTime(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

// 只要时分秒(趋势图坐标轴、时间线)
export function formatClock(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
  })
}

export function relativeTime(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const diff = Date.now() - d.getTime()
  // 未来时间(时钟偏差)不显示成"-3 秒前"
  if (diff < 0) return '刚刚'
  const sec = Math.round(diff / 1000)
  if (sec < 60) return `${sec} 秒前`
  const min = Math.round(sec / 60)
  if (min < 60) return `${min} 分钟前`
  const hr = Math.round(min / 60)
  if (hr < 24) return `${hr} 小时前`
  const day = Math.round(hr / 24)
  if (day < 30) return `${day} 天前`
  const mon = Math.round(day / 30)
  return `${mon} 个月前`
}

export function formatCost(v?: number): string {
  if (v === undefined || v === null) return '$0.00'
  // 小额成本显示到 4 位:$0.0031 与 $0.00 是不同的信息,
  // 后者会被读成"没花钱"。
  if (v > 0 && v < 0.01) return `$${v.toFixed(4)}`
  return `$${v.toFixed(2)}`
}

export function formatDuration(sec?: number | null): string {
  if (sec === undefined || sec === null) return '—'
  if (sec < 1) return '<1s'
  if (sec < 60) return `${Math.round(sec)}s`
  const m = Math.floor(sec / 60)
  const s = Math.round(sec % 60)
  if (m < 60) return `${m}m${s ? ` ${s}s` : ''}`
  const h = Math.floor(m / 60)
  const rm = m % 60
  if (h < 24) return `${h}h${rm ? ` ${rm}m` : ''}`
  const d = Math.floor(h / 24)
  return `${d}d${h % 24 ? ` ${h % 24}h` : ''}`
}

export function pct(v: number): string {
  return `${Math.round(v * 100)}%`
}

// 大数字加千分位并在表格里配合 .tabular 用等宽数字
export function formatCount(n?: number): string {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString('zh-CN')
}

// token 数用 k/M 收敛:200000 在卡片里占太宽,且精确值没有意义
export function formatTokens(n?: number): string {
  if (!n) return '0'
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`
  return `${(n / 1_000_000).toFixed(1)}M`
}

// 后端返回 { namespaces, resources };早期契约用 services。两者都兼容展示。
export function formatBlastRadius(b?: BlastRadius): string {
  if (!b) return '—'
  const parts: string[] = []
  // 服务数是影响面的主口径(单服务多 Pod 不应显示为多个服务);
  // 资源数在与服务数不同时补充展示,避免丢掉 Pod 级细节。
  if (b.services !== undefined) parts.push(`${b.services} 服务`)
  if (b.resources !== undefined && b.resources !== b.services) {
    parts.push(`${b.resources} 资源`)
  }
  if (b.namespaces !== undefined) parts.push(`${b.namespaces} 命名空间`)
  return parts.length ? parts.join(' / ') : '—'
}

// 资源引用的紧凑显示:Deployment/payment-api
export function formatResource(r?: {
  kind?: string
  name?: string
  namespace?: string
}): string {
  if (!r || (!r.kind && !r.name)) return '—'
  const base = [r.kind, r.name].filter(Boolean).join('/')
  return base || '—'
}

// 中文动作名。审计与反馈的 action 是英文枚举,直接显示会让值班人员
// 需要额外记一层映射。未知值原样返回,不掩盖新枚举的出现。
const ACTION_LABEL: Record<string, string> = {
  read_incident: '读取 Incident',
  read_evidence: '读取证据',
  start_investigation: '发起调查',
  investigation_cancel: '取消调查',
  cancel_investigation: '取消调查',
  human_feedback: '人工反馈',
  review_golden_case: '审核评测用例',
  golden_case_promoted: '提升评测用例',
  incident_status_change: '变更状态',
  tool_invoke: '工具调用',
  read_audit: '读取审计',
}

export function actionLabel(action: string): string {
  return ACTION_LABEL[action] ?? action
}

const FEEDBACK_LABEL: Record<string, string> = {
  confirm: '确认',
  correct: '纠正',
  reject: '否决',
  close: '关闭',
}

export function feedbackLabel(action: string): string {
  return FEEDBACK_LABEL[action] ?? action
}

const EVIDENCE_LABEL: Record<string, string> = {
  metric: '指标',
  log: '日志',
  trace: '链路',
  kubernetes: 'K8s',
  change: '变更',
  knowledge: '知识',
}

export function evidenceTypeLabel(t: string): string {
  return EVIDENCE_LABEL[t] ?? t
}

const KNOWLEDGE_LABEL: Record<string, string> = {
  runbook: 'Runbook',
  architecture: '架构文档',
  service_catalog: '服务目录',
  historical_incident: '历史故障',
  postmortem: '复盘',
}

export function knowledgeKindLabel(k: string): string {
  return KNOWLEDGE_LABEL[k] ?? k
}

const FAULT_LABEL: Record<string, string> = {
  release_regression: '发布回归',
  pod_workload: '工作负载',
  resource: '资源',
  dependency: '依赖',
  saturation: '饱和',
  config: '配置',
}

export function faultCategoryLabel(c: string): string {
  return FAULT_LABEL[c] ?? c
}
