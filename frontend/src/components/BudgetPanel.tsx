import type { Budget, Usage } from '@/api/types'
import { ProgressBar } from './ui'
import { formatCost, formatDuration } from '@/lib/format'

interface Row {
  label: string
  used: number
  max: number
  render: (v: number) => string
}

function tone(ratio: number): 'accent' | 'warn' | 'danger' {
  if (ratio >= 0.9) return 'danger'
  if (ratio >= 0.7) return 'warn'
  return 'accent'
}

export function BudgetPanel({
  budget,
  usage,
}: {
  budget: Budget
  usage: Usage
}) {
  const rows: Row[] = [
    {
      label: '耗时',
      used: usage.elapsed_sec,
      max: budget.max_duration_sec,
      render: formatDuration,
    },
    {
      label: '轮次',
      used: usage.rounds,
      max: budget.max_rounds,
      render: (v) => String(v),
    },
    {
      label: 'Token',
      used: usage.tokens,
      max: budget.max_tokens,
      render: (v) => v.toLocaleString('en-US'),
    },
    {
      label: '成本',
      used: usage.cost_usd,
      max: budget.max_cost_usd,
      render: formatCost,
    },
    {
      label: '工具调用',
      used: usage.tool_calls,
      max: budget.max_tool_calls,
      render: (v) => String(v),
    },
  ]

  return (
    <div className="space-y-3 p-4">
      {rows.map((r) => {
        const ratio = r.max > 0 ? r.used / r.max : 0
        return (
          <div key={r.label}>
            <div className="mb-1 flex items-baseline justify-between text-xs">
              <span className="text-slate-400">{r.label}</span>
              <span className="font-mono text-slate-300">
                {r.render(r.used)}
                <span className="text-slate-500"> / {r.render(r.max)}</span>
              </span>
            </div>
            <ProgressBar value={r.used} max={r.max} tone={tone(ratio)} />
          </div>
        )
      })}
    </div>
  )
}
