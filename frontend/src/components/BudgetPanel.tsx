import type { Budget, Usage } from '@/api/types'
import { ProgressBar, Callout } from './ui'
import { formatCost, formatDuration, formatCount } from '@/lib/format'
import { ShieldAlert } from 'lucide-react'

interface Row {
  label: string
  used: number
  max: number
  render: (v: number) => string
  hint?: string
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
      render: (v) => formatDuration(v),
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
      render: formatCount,
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
              <span className="text-muted">{r.label}</span>
              <span className="tabular font-mono text-muted">
                {r.render(r.used)}
                <span className="text-faint"> / {r.render(r.max)}</span>
              </span>
            </div>
            <ProgressBar value={r.used} max={r.max} tone={tone(ratio)} />
          </div>
        )
      })}

      {/* 无实时证据支撑的结论被确定性降级的次数。>0 说明模型这轮声称已确认
          但拿不出实时证据 —— 这是模型质量信号,不是系统错误,所以用提示条
          而不是错误态。混在进度条里会看不见:它没有"上限"这个维度。 */}
      {usage.ungrounded_downgrades != null &&
        usage.ungrounded_downgrades > 0 && (
          <Callout tone="warn" icon={<ShieldAlert className="h-3.5 w-3.5" />}>
            有 {usage.ungrounded_downgrades} 条结论因缺少实时证据被自动降级。
            模型声称已确认但拿不出证据,结论可信度应相应打折。
          </Callout>
        )}
    </div>
  )
}
