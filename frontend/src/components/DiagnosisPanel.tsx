import type { DiagnosisResult } from '@/api/types'
import { Card, CardHeader } from './ui'
import { DiagnosisStatusBadge } from './Badges'
import {
  CheckCircle2,
  HelpCircle,
  ArrowRight,
  Lock,
  Stethoscope,
} from 'lucide-react'

function ListBlock({
  icon,
  title,
  items,
  empty,
}: {
  icon: React.ReactNode
  title: string
  items: string[]
  empty: string
}) {
  return (
    <div>
      <div className="mb-1.5 flex items-center gap-1.5 text-xs font-semibold text-muted">
        {icon}
        {title}
        <span className="text-faint">({items?.length ?? 0})</span>
      </div>
      {items && items.length > 0 ? (
        <ul className="ml-1 space-y-1">
          {items.map((it, i) => (
            <li
              key={i}
              className="flex gap-2 rounded-lg border border-line-soft bg-bg-soft px-2.5 py-1.5 text-sm text-content"
            >
              <span className="tabular shrink-0 text-faint">{i + 1}.</span>
              <span className="min-w-0">{it}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-2xs text-faint">{empty}</p>
      )}
    </div>
  )
}

export function DiagnosisPanel({ diagnosis }: { diagnosis: DiagnosisResult }) {
  return (
    <Card>
      <CardHeader
        icon={<Stethoscope className="h-4 w-4 text-accent" />}
        title="诊断结果"
        subtitle="每条结论都可追溯到 Evidence ID"
        right={<DiagnosisStatusBadge status={diagnosis.status} />}
      />
      <div className="space-y-4 p-4">
        <ListBlock
          icon={<CheckCircle2 className="h-3.5 w-3.5 text-ok" />}
          title="已确认事实"
          items={diagnosis.confirmed_facts}
          empty="暂无已确认事实"
        />
        <ListBlock
          icon={<HelpCircle className="h-3.5 w-3.5 text-warn" />}
          title="缺失信息"
          items={diagnosis.missing_information}
          empty="无"
        />
        <ListBlock
          icon={<ArrowRight className="h-3.5 w-3.5 text-accent" />}
          title="建议后续动作"
          items={diagnosis.next_actions}
          empty="无"
        />

        {/* 首版恒为 null:明确显示只读、无自动修复。
            刻意做成一个显式声明而不是省略 —— 值班人员需要知道
            "系统不会自己动手",而不是猜它有没有权限。 */}
        <div className="flex items-center gap-2 rounded-lg border border-dashed border-line px-3 py-2 text-2xs text-muted">
          <Lock className="h-3.5 w-3.5 shrink-0 text-faint" />
          <span>
            修复建议 remediation_proposal:
            <span className="font-mono text-faint"> null</span>
            {' '}· 系统默认只读,不会执行任何写操作
          </span>
        </div>
      </div>
    </Card>
  )
}
