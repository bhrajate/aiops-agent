import { useEffect, useRef, useState } from 'react'
import { apiUrl, withTokenQuery } from '@/api/client'
import type { InvestigationEvent } from '@/api/types'

export type SSEStatus = 'idle' | 'connecting' | 'open' | 'closed' | 'error'

// 环形缓冲上限:仅保留最近 N 条事件,避免长时间调查时事件数组无限增长撑爆内存。
const MAX_EVENTS = 500

interface UseSSEResult {
  events: InvestigationEvent[]
  status: SSEStatus
  clear: () => void
}

/**
 * 订阅调查实时时间线:GET /v1/investigations/{id}/events (text/event-stream)。
 * 使用浏览器原生 EventSource,自动重连;通过 Vite dev proxy 转发到 :8088。
 * 事件 data 约定为 JSON(InvestigationEvent),兼容纯文本。
 */
export function useSSE(
  investigationId: string | undefined,
  enabled = true,
): UseSSEResult {
  const [events, setEvents] = useState<InvestigationEvent[]>([])
  const [status, setStatus] = useState<SSEStatus>('idle')
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    if (!investigationId || !enabled) {
      setStatus('idle')
      return
    }

    // EventSource 无法设置 Authorization 头,token 以查询串携带(后端支持时生效)。
    // ⚠️ 安全取舍(本轮暂不改后端):access_token 走 query string 可能被写入
    //    nginx/网关访问日志、浏览器历史、Referer 泄露。后续方向:后端改为基于
    //    HttpOnly Cookie 的 SSE 鉴权,或下发短时一次性 SSE ticket 换取连接。
    const url = apiUrl(
      withTokenQuery(
        `/v1/investigations/${encodeURIComponent(investigationId)}/events`,
      ),
    )
    setStatus('connecting')

    const es = new EventSource(url, { withCredentials: false })
    esRef.current = es

    const handle = (raw: MessageEvent, fallbackType: string) => {
      let parsed: InvestigationEvent
      try {
        const obj = JSON.parse(raw.data)
        parsed = {
          event_id: raw.lastEventId || obj.event_id,
          event_type: obj.event_type ?? fallbackType,
          phase: obj.phase,
          payload: obj.payload ?? obj,
          ts: obj.ts ?? new Date().toISOString(),
        }
      } catch {
        parsed = {
          event_id: raw.lastEventId || undefined,
          event_type: fallbackType,
          payload: { raw: raw.data },
          ts: new Date().toISOString(),
        }
      }
      setEvents((prev) => {
        const next = [...prev, parsed]
        // 超过上限时丢弃最旧事件,保留最近 MAX_EVENTS 条(环形缓冲语义)。
        return next.length > MAX_EVENTS ? next.slice(-MAX_EVENTS) : next
      })
    }

    es.onopen = () => setStatus('open')
    es.onmessage = (e) => handle(e, 'message')

    // 常见命名事件类型也监听(后端可能用 event: <type>)
    const named = [
      'phase',
      'phase_changed',
      'tool_call',
      'evidence',
      'hypotheses',
      'diagnosis',
      'usage',
      'log',
    ]
    const listeners: Array<[string, (e: MessageEvent) => void]> = []
    for (const t of named) {
      const fn = (e: MessageEvent) => handle(e, t)
      es.addEventListener(t, fn as EventListener)
      listeners.push([t, fn])
    }

    es.onerror = () => {
      // EventSource 会自动重连;若已被浏览器关闭则标记 closed
      setStatus(es.readyState === EventSource.CLOSED ? 'closed' : 'error')
    }

    return () => {
      for (const [t, fn] of listeners) {
        es.removeEventListener(t, fn as EventListener)
      }
      es.close()
      esRef.current = null
    }
  }, [investigationId, enabled])

  return {
    events,
    status,
    clear: () => setEvents([]),
  }
}
