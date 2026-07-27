// 通用格式化 / 展示辅助。

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

export function relativeTime(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const diff = Date.now() - d.getTime()
  const sec = Math.round(diff / 1000)
  if (sec < 60) return `${sec} 秒前`
  const min = Math.round(sec / 60)
  if (min < 60) return `${min} 分钟前`
  const hr = Math.round(min / 60)
  if (hr < 24) return `${hr} 小时前`
  const day = Math.round(hr / 24)
  return `${day} 天前`
}

export function formatCost(v?: number): string {
  if (v === undefined || v === null) return '$0.00'
  return `$${v.toFixed(2)}`
}

export function formatDuration(sec?: number): string {
  if (!sec) return '0s'
  if (sec < 60) return `${Math.round(sec)}s`
  const m = Math.floor(sec / 60)
  const s = Math.round(sec % 60)
  return `${m}m${s ? ` ${s}s` : ''}`
}

export function pct(v: number): string {
  return `${Math.round(v * 100)}%`
}

import type { BlastRadius } from '@/api/types'

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
