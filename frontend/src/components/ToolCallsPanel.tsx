import type { ToolCall } from '@/api/types'
import { Card, CardHeader } from './ui'
import { formatTime } from '@/lib/format'
import { Wrench, ShieldCheck } from 'lucide-react'

export function ToolCallsPanel({
  toolCalls,
  onOpenEvidence,
}: {
  toolCalls: ToolCall[]
  onOpenEvidence: (id: string) => void
}) {
  return (
    <Card>
      <CardHeader
        icon={<Wrench className="h-4 w-4 text-accent" />}
        title="已调用工具"
        subtitle="全部只读,逐次审计"
        right={
          <span className="text-2xs text-faint">{toolCalls.length} 次</span>
        }
      />
      <div className="p-2">
        {toolCalls.length === 0 ? (
          <p className="py-4 text-center text-xs text-faint">
            尚未调用任何工具
          </p>
        ) : (
          <ul className="divide-y divide-line-soft">
            {toolCalls.map((tc, i) => {
              const ns = tc.scope?.['namespace']
              return (
                <li
                  key={i}
                  className="flex items-center justify-between gap-2 px-2 py-2"
                >
                  <div className="min-w-0">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate font-mono text-xs text-content">
                        {tc.tool_name}
                      </span>
                      {tc.status === 'redacted' && (
                        <span
                          className="inline-flex items-center gap-0.5 rounded bg-warn/15 px-1 py-0.5 text-2xs text-warn"
                          title="返回内容包含敏感数据,已脱敏"
                        >
                          <ShieldCheck className="h-2.5 w-2.5" />
                          已脱敏
                        </span>
                      )}
                    </div>
                    <div className="mt-0.5 truncate text-2xs text-faint">
                      {typeof ns === 'string' && ns ? `${ns} · ` : ''}
                      {formatTime(tc.finished_at ?? tc.started_at)}
                    </div>
                  </div>
                  {tc.evidence_id && (
                    <button
                      onClick={() => onOpenEvidence(tc.evidence_id!)}
                      title="查看该次调用冻结的证据"
                      className="shrink-0 rounded bg-card-soft px-2 py-0.5 font-mono text-2xs text-accent transition-colors hover:bg-accent/15"
                    >
                      {tc.evidence_id}
                    </button>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </Card>
  )
}
