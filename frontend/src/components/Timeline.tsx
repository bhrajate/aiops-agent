import type { InvestigationEvent } from '@/api/types'
import type { SSEStatus } from '@/hooks/useSSE'
import { Card, CardHeader } from './ui'
import { formatTime, cn } from '@/lib/format'
import { Activity, Radio } from 'lucide-react'

const EVENT_LABEL: Record<string, string> = {
  phase: '阶段流转',
  phase_changed: '阶段流转',
  tool_call: '工具调用',
  evidence: '新证据',
  hypotheses: '假设更新',
  diagnosis: '诊断产出',
  usage: '用量更新',
  log: '日志',
  message: '事件',
}

const EVENT_DOT: Record<string, string> = {
  phase: 'bg-indigo-400',
  phase_changed: 'bg-indigo-400',
  tool_call: 'bg-blue-400',
  evidence: 'bg-emerald-400',
  hypotheses: 'bg-violet-400',
  diagnosis: 'bg-sky-400',
  usage: 'bg-slate-400',
  log: 'bg-slate-500',
}

function summarize(ev: InvestigationEvent): string {
  const p = ev.payload ?? {}
  if (ev.phase) return `→ ${ev.phase}`
  if (typeof p['phase'] === 'string') return `→ ${p['phase']}`
  if (typeof p['tool'] === 'string') return String(p['tool'])
  if (typeof p['tool_name'] === 'string') return String(p['tool_name'])
  if (typeof p['summary'] === 'string') return String(p['summary'])
  if (typeof p['message'] === 'string') return String(p['message'])
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
        'inline-flex items-center gap-1 rounded px-2 py-0.5 text-xs',
        live
          ? 'bg-emerald-500/15 text-emerald-300'
          : status === 'error' || status === 'connecting'
            ? 'bg-amber-500/15 text-amber-300'
            : 'bg-slate-500/15 text-slate-400',
      )}
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
    <Card className="flex h-full flex-col">
      <CardHeader
        icon={<Activity className="h-4 w-4 text-accent" />}
        title="实时时间线"
        right={<LiveIndicator status={sseStatus} />}
      />
      <div className="flex-1 overflow-auto p-4">
        {events.length === 0 ? (
          <p className="py-6 text-center text-xs text-slate-500">
            {sseStatus === 'open'
              ? '等待事件…'
              : '暂无事件。调查进行时事件将实时追加。'}
          </p>
        ) : (
          <ol className="relative space-y-3 border-l border-surface-700 pl-4">
            {events.map((ev, i) => {
              const type = ev.event_type ?? 'message'
              return (
                <li key={ev.event_id ?? i} className="relative">
                  <span
                    className={cn(
                      'absolute -left-[21px] top-1.5 h-2.5 w-2.5 rounded-full ring-2 ring-surface-850',
                      EVENT_DOT[type] ?? 'bg-slate-500',
                    )}
                  />
                  <div className="flex items-baseline justify-between gap-2">
                    <span className="text-sm font-medium text-slate-200">
                      {EVENT_LABEL[type] ?? type}
                    </span>
                    <span className="shrink-0 font-mono text-[11px] text-slate-500">
                      {formatTime(ev.ts)}
                    </span>
                  </div>
                  {summarize(ev) && (
                    <p className="mt-0.5 break-words text-xs text-slate-400">
                      {summarize(ev)}
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
