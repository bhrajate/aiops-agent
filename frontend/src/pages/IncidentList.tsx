import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { IncidentStatus, Severity } from '@/api/types'
import { useIncidents } from '@/hooks/queries'
import { SeverityBadge, StatusBadge } from '@/components/Badges'
import { Card, Spinner, ErrorState, EmptyState, Button } from '@/components/ui'
import { SignalInjector } from '@/components/SignalInjector'
import { formatTime, relativeTime, formatBlastRadius, cn } from '@/lib/format'
import { HttpError } from '@/api/client'
import { FlaskConical, RefreshCw, ChevronRight } from 'lucide-react'

const STATUS_OPTS: Array<{ v: IncidentStatus | ''; label: string }> = [
  { v: '', label: '全部状态' },
  { v: 'open', label: '进行中' },
  { v: 'acknowledged', label: '已认领' },
  { v: 'resolved', label: '已解决' },
  { v: 'closed', label: '已关闭' },
]
const SEV_OPTS: Array<{ v: Severity | ''; label: string }> = [
  { v: '', label: '全部级别' },
  { v: 'P1', label: 'P1' },
  { v: 'P2', label: 'P2' },
  { v: 'P3', label: 'P3' },
  { v: 'P4', label: 'P4' },
]

export function IncidentListPage() {
  const [status, setStatus] = useState<IncidentStatus | ''>('')
  const [severity, setSeverity] = useState<Severity | ''>('')
  const [showInjector, setShowInjector] = useState(false)

  const { data, isLoading, error, refetch, isFetching } = useIncidents({
    status: status || undefined,
    severity: severity || undefined,
    limit: 100,
  })

  const selectCls =
    'rounded-md border border-surface-600 bg-surface-850 px-2.5 py-1.5 text-sm text-slate-200 outline-none focus:border-accent'

  return (
    <div className="mx-auto max-w-7xl px-6 py-6">
      <div className="mb-4 flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold text-slate-100">Incident 列表</h1>
          <p className="text-xs text-slate-500">
            Incident-first · 值班总览
          </p>
        </div>
        <div className="flex items-center gap-2">
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value as IncidentStatus | '')}
            className={selectCls}
          >
            {STATUS_OPTS.map((o) => (
              <option key={o.v} value={o.v}>
                {o.label}
              </option>
            ))}
          </select>
          <select
            value={severity}
            onChange={(e) => setSeverity(e.target.value as Severity | '')}
            className={selectCls}
          >
            {SEV_OPTS.map((o) => (
              <option key={o.v} value={o.v}>
                {o.label}
              </option>
            ))}
          </select>
          <Button variant="ghost" onClick={() => refetch()}>
            <RefreshCw
              className={cn('h-4 w-4', isFetching && 'animate-spin')}
            />
          </Button>
          <Button variant="primary" onClick={() => setShowInjector(true)}>
            <FlaskConical className="h-3.5 w-3.5" />
            注入 Signal
          </Button>
        </div>
      </div>

      <Card>
        {isLoading ? (
          <Spinner label="加载 Incident…" />
        ) : error ? (
          <ErrorState
            message={
              error instanceof HttpError
                ? `${error.message}(${error.code})`
                : '加载失败,请确认后端 :8080 已启动'
            }
            onRetry={() => refetch()}
          />
        ) : !data || data.length === 0 ? (
          <EmptyState
            title="暂无 Incident"
            hint="可通过右上角“注入 Signal”模拟端到端流程"
          />
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-surface-700 text-left text-xs text-slate-500">
                <th className="px-4 py-2 font-medium">级别</th>
                <th className="px-4 py-2 font-medium">状态</th>
                <th className="px-4 py-2 font-medium">故障类别</th>
                <th className="px-4 py-2 font-medium">影响资源</th>
                <th className="px-4 py-2 font-medium">影响范围</th>
                <th className="px-4 py-2 font-medium">首次</th>
                <th className="px-4 py-2 font-medium">最后</th>
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {data.map((inc) => (
                <tr
                  key={inc.incident_id}
                  className="border-b border-surface-800 last:border-0 hover:bg-surface-800/60"
                >
                  <td className="px-4 py-3">
                    <SeverityBadge severity={inc.severity} />
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge status={inc.status} />
                  </td>
                  <td className="px-4 py-3">
                    <span className="rounded bg-surface-800 px-1.5 py-0.5 font-mono text-xs text-slate-300">
                      {inc.fault_category || '—'}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-slate-300">
                    {inc.affected_resources?.length ? (
                      <span className="font-mono text-xs">
                        {inc.affected_resources[0].kind}/
                        {inc.affected_resources[0].name}
                        {inc.affected_resources.length > 1
                          ? ` +${inc.affected_resources.length - 1}`
                          : ''}
                      </span>
                    ) : (
                      '—'
                    )}
                  </td>
                  <td className="px-4 py-3 text-xs text-slate-400">
                    {formatBlastRadius(inc.blast_radius)}
                  </td>
                  <td
                    className="px-4 py-3 text-xs text-slate-400"
                    title={formatTime(inc.first_seen)}
                  >
                    {relativeTime(inc.first_seen)}
                  </td>
                  <td
                    className="px-4 py-3 text-xs text-slate-400"
                    title={formatTime(inc.last_seen)}
                  >
                    {relativeTime(inc.last_seen)}
                  </td>
                  <td className="px-4 py-3 text-right">
                    <Link
                      to={`/incidents/${inc.incident_id}`}
                      className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
                    >
                      详情
                      <ChevronRight className="h-3.5 w-3.5" />
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      {showInjector && (
        <SignalInjector onClose={() => setShowInjector(false)} />
      )}
    </div>
  )
}
