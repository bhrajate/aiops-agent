import type { AlertGroup } from '@/api/types'
import { Card, CardHeader } from './ui'
import { SeverityBadge } from './Badges'
import { formatTime, relativeTime, formatResource, cn } from '@/lib/format'
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
        subtitle="同资源 + 同规则的重复告警已收敛为一条"
        right={
          <span className="text-2xs text-faint">
            {active} 活跃 / {groups.length} 组
          </span>
        }
      />
      <div className="p-2">
        {groups.length === 0 ? (
          <p className="py-4 text-center text-xs text-faint">
            暂无告警分组明细
          </p>
        ) : (
          <ul className="divide-y divide-line-soft">
            {groups.map((g) => {
              const resolved = g.status === 'resolved'
              return (
                <li
                  key={g.group_id}
                  className={cn(
                    'flex items-start justify-between gap-3 px-2 py-2.5',
                    resolved && 'opacity-55',
                  )}
                >
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-mono text-xs text-content">
                        {formatResource(g.resource) === '—'
                          ? '(未知资源)'
                          : formatResource(g.resource)}
                      </span>
                      <SeverityBadge severity={g.severity} />
                      {resolved && (
                        <span className="rounded bg-ok/15 px-1.5 py-0.5 text-2xs text-ok">
                          已恢复
                        </span>
                      )}
                    </div>
                    <div className="mt-0.5 truncate text-2xs text-muted">
                      {g.namespace ? `${g.namespace} · ` : ''}
                      {g.title || g.fault_category}
                    </div>
                    <div
                      className="text-2xs text-faint"
                      title={`${formatTime(g.first_seen)} → ${formatTime(g.last_seen)}`}
                    >
                      {relativeTime(g.first_seen)} 起 · 最后{' '}
                      {relativeTime(g.last_seen)}
                    </div>
                  </div>
                  <span
                    className="tabular shrink-0 rounded bg-card-soft px-2 py-0.5 font-mono text-2xs text-muted"
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
