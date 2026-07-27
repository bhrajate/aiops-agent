import type { AlertGroup } from '@/api/types'
import { Card, CardHeader } from './ui'
import { SeverityBadge } from './Badges'
import { formatTime } from '@/lib/format'
import { Layers } from 'lucide-react'

// 两层聚合模型的去重单元展示:
// 一个 Incident(相关性单元)可包含多个 AlertGroup(同资源+同规则的重复告警收敛)。
// 值班人员据此看到"影响了哪几个服务、各自是否已恢复",而不只是一个总数。
export function AlertGroupsPanel({ groups }: { groups: AlertGroup[] }) {
  const active = groups.filter((g) => g.status === 'open').length
  return (
    <Card>
      <CardHeader
        icon={<Layers className="h-4 w-4 text-accent" />}
        title="告警聚合明细"
        right={
          <span className="text-xs text-slate-500">
            {active} 活跃 / {groups.length} 组
          </span>
        }
      />
      <div className="p-2">
        {groups.length === 0 ? (
          <p className="py-4 text-center text-xs text-slate-500">
            暂无告警分组明细
          </p>
        ) : (
          <ul className="divide-y divide-surface-800">
            {groups.map((g) => {
              const resolved = g.status === 'resolved'
              return (
                <li
                  key={g.group_id}
                  className={`flex items-start justify-between gap-3 px-2 py-2 ${
                    resolved ? 'opacity-60' : ''
                  }`}
                >
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-mono text-sm text-slate-200">
                        {g.resource?.kind ? `${g.resource.kind}/` : ''}
                        {g.resource?.name || '(未知资源)'}
                      </span>
                      <SeverityBadge severity={g.severity} />
                      {resolved && (
                        <span className="rounded bg-surface-700 px-1.5 py-0.5 text-[10px] text-slate-400">
                          已恢复
                        </span>
                      )}
                    </div>
                    <div className="truncate text-[11px] text-slate-500">
                      {g.namespace ? `${g.namespace} · ` : ''}
                      {g.title || g.fault_category}
                    </div>
                    <div className="text-[11px] text-slate-500">
                      {formatTime(g.first_seen)} → {formatTime(g.last_seen)}
                    </div>
                  </div>
                  <span
                    className="shrink-0 rounded bg-surface-700 px-2 py-0.5 font-mono text-xs text-slate-300"
                    title="该分组收敛的信号数(去重计数)"
                  >
                    ×{g.signal_count}
                  </span>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </Card>
  )
}
