import type { ToolCall } from '@/api/types'
import { Card, CardHeader } from './ui'
import { formatTime } from '@/lib/format'
import { Wrench } from 'lucide-react'

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
        right={
          <span className="text-xs text-slate-500">
            {toolCalls.length} 次
          </span>
        }
      />
      <div className="p-2">
        {toolCalls.length === 0 ? (
          <p className="py-4 text-center text-xs text-slate-500">
            尚未调用任何工具
          </p>
        ) : (
          <ul className="divide-y divide-surface-800">
            {toolCalls.map((tc, i) => (
              <li
                key={i}
                className="flex items-center justify-between gap-2 px-2 py-2"
              >
                <div className="min-w-0">
                  <span className="font-mono text-sm text-slate-200">
                    {tc.tool_name}
                  </span>
                  {tc.scope && (
                    <span className="ml-2 truncate font-mono text-[11px] text-slate-500">
                      {(tc.scope['namespace'] as string) ?? ''}
                    </span>
                  )}
                  <div className="text-[11px] text-slate-500">
                    {formatTime(tc.finished_at ?? tc.started_at)}
                    {tc.status ? ` · ${tc.status}` : ''}
                  </div>
                </div>
                {tc.evidence_id && (
                  <button
                    onClick={() => onOpenEvidence(tc.evidence_id!)}
                    className="shrink-0 rounded bg-surface-700 px-2 py-0.5 font-mono text-xs text-accent hover:bg-surface-600"
                  >
                    {tc.evidence_id}
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  )
}
