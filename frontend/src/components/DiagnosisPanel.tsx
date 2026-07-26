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
      <div className="mb-1.5 flex items-center gap-1.5 text-xs font-semibold text-slate-300">
        {icon}
        {title}
        <span className="text-slate-600">({items?.length ?? 0})</span>
      </div>
      {items && items.length > 0 ? (
        <ul className="ml-1 space-y-1">
          {items.map((it, i) => (
            <li
              key={i}
              className="flex gap-2 rounded bg-surface-800 px-2.5 py-1.5 text-sm text-slate-200"
            >
              <span className="text-slate-600">{i + 1}.</span>
              <span>{it}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-xs text-slate-600">{empty}</p>
      )}
    </div>
  )
}

export function DiagnosisPanel({ diagnosis }: { diagnosis: DiagnosisResult }) {
  return (
    <Card>
      <CardHeader
        icon={<Stethoscope className="h-4 w-4 text-accent" />}
        title="诊断结果 DiagnosisResult"
        right={<DiagnosisStatusBadge status={diagnosis.status} />}
      />
      <div className="space-y-4 p-4">
        <ListBlock
          icon={<CheckCircle2 className="h-3.5 w-3.5 text-emerald-400" />}
          title="已确认事实"
          items={diagnosis.confirmed_facts}
          empty="暂无已确认事实"
        />
        <ListBlock
          icon={<HelpCircle className="h-3.5 w-3.5 text-amber-400" />}
          title="缺失信息"
          items={diagnosis.missing_information}
          empty="无"
        />
        <ListBlock
          icon={<ArrowRight className="h-3.5 w-3.5 text-sky-400" />}
          title="建议后续动作"
          items={diagnosis.next_actions}
          empty="无"
        />

        {/* 首版恒为 null:明确显示只读、无自动修复 */}
        <div className="flex items-center gap-2 rounded-md border border-dashed border-surface-600 bg-surface-900/60 px-3 py-2 text-xs text-slate-400">
          <Lock className="h-3.5 w-3.5 text-slate-500" />
          <span>
            修复建议 remediation_proposal:
            <span className="font-mono text-slate-500"> null</span> ·
            首版只读,不提供自动修复
          </span>
        </div>
      </div>
    </Card>
  )
}
