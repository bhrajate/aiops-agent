import type { InvestigationEvent } from '@/api/types'
import type { SSEStatus } from '@/hooks/useSSE'
import { Card, CardHeader } from './ui'
import { formatTime, cn } from '@/lib/format'
import { Activity, Radio } from 'lucide-react'

const EVENT_LABEL: Record<string, string> = {
  phase: '阶段流转',
  phase_changed: '阶段流转',
  tool_call: '工具调用',
  tool_called: '工具调用',
  evidence: '新证据',
  evidence_added: '新证据',
  hypotheses: '假设更新',
  hypothesis_updated: '假设更新',
  diagnosis: '诊断产出',
  diagnosis_published: '诊断产出',
  usage: '用量更新',
  human_feedback: '人工反馈',
  escalated: '已升级',
  log: '日志',
  message: '事件',
  done: '已结束',
}

const EVENT_DOT: Record<string, string> = {
  phase: 'bg-info',
  phase_changed: 'bg-info',
  tool_call: 'bg-accent',
  tool_called: 'bg-accent',
  evidence: 'bg-ok',
  evidence_added: 'bg-ok',
  hypotheses: 'bg-info',
  hypothesis_updated: 'bg-info',
  diagnosis: 'bg-accent',
  diagnosis_published: 'bg-accent',
  usage: 'bg-faint',
  human_feedback: 'bg-warn',
  escalated: 'bg-danger',
  log: 'bg-faint',
  done: 'bg-ok',
}

function summarize(ev: InvestigationEvent): string {
  const p = ev.payload ?? {}
  if (ev.phase) return `→ ${ev.phase}`
  if (typeof p['phase'] === 'string') return `→ ${p['phase']}`
  if (typeof p['tool'] === 'string') return String(p['tool'])
  if (typeof p['tool_name'] === 'string') return String(p['tool_name'])
  if (typeof p['summary'] === 'string') return String(p['summary'])
  if (typeof p['message'] === 'string') return String(p['message'])
  if (typeof p['action'] === 'string') {
    const author = typeof p['author'] === 'string' ? ` by ${p['author']}` : ''
    return `${p['action']}${author}`
  }
  if (typeof p['raw'] === 'string') return String(p['raw'])
  const keys = Object.keys(p)
  return keys.length ? JSON.stringify(p).slice(0, 120) : ''
}

const STATUS_TEXT: Record<SSEStatus, string> = {
  idle: '未连接',
  connecting: '连接中',
  open: '实时',
  closed: '已断开',
  error: '重连中',
}

function LiveIndicator({ status }: { status: SSEStatus }) {
  const live = status === 'open'
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-2xs',
        live
          ? 'bg-ok/15 text-ok'
          : status === 'error' || status === 'connecting'
            ? 'bg-warn/15 text-warn'
            : 'bg-card-soft text-faint',
      )}
      title={
        status === 'closed'
          ? 'SSE 已断开。数据库是事实源,页面仍在轮询兜底。'
          : undefined
      }
    >
      <Radio className={cn('h-3 w-3', live && 'animate-pulse')} />
      {STATUS_TEXT[status]}
    </span>
  )
}

export function Timeline({
  events,
  sseStatus,
}: {
  events: InvestigationEvent[]
  sseStatus: SSEStatus
}) {
  return (
    <Card className="flex h-full min-h-0 flex-col">
      <CardHeader
        icon={<Activity className="h-4 w-4 text-accent" />}
        title="实时时间线"
        right={<LiveIndicator status={sseStatus} />}
      />
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        {events.length === 0 ? (
          <p className="py-6 text-center text-xs text-faint">
            {sseStatus === 'open'
              ? '等待事件…'
              : '暂无事件。调查进行时事件将实时追加。'}
          </p>
        ) : (
          <ol className="relative space-y-3 border-l border-line pl-4">
            {events.map((ev, i) => {
              const type = ev.event_type ?? 'message'
              const detail = summarize(ev)
              return (
                <li key={ev.event_id ?? i} className="relative">
                  <span
                    className={cn(
                      'absolute -left-[21px] top-1.5 h-2.5 w-2.5 rounded-full ring-2 ring-card',
                      EVENT_DOT[type] ?? 'bg-faint',
                    )}
                  />
                  <div className="flex items-baseline justify-between gap-2">
                    <span className="text-xs font-medium text-content">
                      {EVENT_LABEL[type] ?? type}
                    </span>
                    <span className="tabular shrink-0 font-mono text-2xs text-faint">
                      {formatTime(ev.ts)}
                    </span>
                  </div>
                  {detail && (
                    <p className="mt-0.5 break-words text-2xs text-muted">
                      {detail}
                    </p>
                  )}
                </li>
              )
            })}
          </ol>
        )}
      </div>
    </Card>
  )
}
