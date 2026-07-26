import { useMemo } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import { useIncident, useStartInvestigation } from '@/hooks/queries'
import { SeverityBadge, StatusBadge } from '@/components/Badges'
import { Card, CardHeader, Spinner, ErrorState, Button } from '@/components/ui'
import { formatTime, formatBlastRadius } from '@/lib/format'
import { HttpError } from '@/api/client'
import type { Incident } from '@/api/types'
import {
  ArrowLeft,
  Play,
  Boxes,
  GitCommitHorizontal,
  Network,
  Search,
} from 'lucide-react'

function resolveInvestigationIds(inc: Incident): string[] {
  const ids = new Set<string>()
  if (inc.current_investigation_id) ids.add(inc.current_investigation_id)
  inc.investigation_ids?.forEach((i) => ids.add(i))
  inc.investigations?.forEach((i) => ids.add(i.investigation_id))
  return [...ids]
}

function InfoItem({
  label,
  value,
}: {
  label: string
  value: React.ReactNode
}) {
  return (
    <div className="rounded-md bg-surface-800 px-3 py-2">
      <div className="text-xs text-slate-500">{label}</div>
      <div className="mt-0.5 text-sm text-slate-200">{value}</div>
    </div>
  )
}

export function IncidentDetailPage() {
  const { incidentId } = useParams<{ incidentId: string }>()
  const navigate = useNavigate()
  const { data: inc, isLoading, error, refetch } = useIncident(incidentId)
  const start = useStartInvestigation()

  const investigationIds = useMemo(
    () => (inc ? resolveInvestigationIds(inc) : []),
    [inc],
  )

  async function handleStart() {
    if (!incidentId) return
    try {
      const investigation = await start.mutateAsync(incidentId)
      if (investigation?.investigation_id) {
        navigate(`/investigations/${investigation.investigation_id}`)
      } else {
        refetch()
      }
    } catch {
      // 错误在按钮下方展示
    }
  }

  if (isLoading) return <Spinner label="加载 Incident…" />
  if (error || !inc)
    return (
      <div className="mx-auto max-w-5xl px-6 py-6">
        <ErrorState
          message={
            error instanceof HttpError
              ? `${error.message}(${error.code})`
              : '加载失败'
          }
          onRetry={() => refetch()}
        />
      </div>
    )

  return (
    <div className="mx-auto max-w-5xl px-6 py-6">
      <Link
        to="/"
        className="mb-4 inline-flex items-center gap-1 text-xs text-slate-400 hover:text-slate-200"
      >
        <ArrowLeft className="h-3.5 w-3.5" />
        返回列表
      </Link>

      {/* 概览 */}
      <div className="mb-4 flex items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-2">
            <SeverityBadge severity={inc.severity} />
            <StatusBadge status={inc.status} />
            <span className="rounded bg-surface-800 px-1.5 py-0.5 font-mono text-xs text-slate-300">
              {inc.fault_category || '未分类'}
            </span>
          </div>
          <h1 className="mt-2 text-lg text-slate-100">
            {inc.title || inc.incident_id}
          </h1>
          <p className="font-mono text-xs text-slate-500">
            {inc.incident_id} · 版本 v{inc.version}
          </p>
        </div>
        <Button variant="primary" loading={start.isPending} onClick={handleStart}>
          <Play className="h-3.5 w-3.5" />
          发起调查
        </Button>
      </div>

      {start.error && (
        <p className="mb-3 text-xs text-red-300">
          发起失败:
          {start.error instanceof HttpError
            ? start.error.message
            : '未知错误'}
        </p>
      )}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader
            icon={<Boxes className="h-4 w-4 text-accent" />}
            title="概览"
          />
          <div className="grid grid-cols-2 gap-2 p-4 md:grid-cols-3">
            <InfoItem
              label="影响范围"
              value={formatBlastRadius(inc.blast_radius)}
            />
            <InfoItem label="首次出现" value={formatTime(inc.first_seen)} />
            <InfoItem label="最后出现" value={formatTime(inc.last_seen)} />
            <InfoItem
              label="拓扑引用"
              value={
                <span className="inline-flex items-center gap-1">
                  <Network className="h-3.5 w-3.5 text-slate-500" />
                  {inc.topology_refs?.length ?? 0}
                </span>
              }
            />
            <InfoItem
              label="变更引用"
              value={
                <span className="inline-flex items-center gap-1">
                  <GitCommitHorizontal className="h-3.5 w-3.5 text-slate-500" />
                  {inc.change_refs?.length ?? 0}
                </span>
              }
            />
            <InfoItem label="聚合键" value={
              <span className="break-all font-mono text-xs text-slate-400">
                {inc.grouping_key || '—'}
              </span>
            } />
          </div>

          {inc.affected_resources?.length > 0 && (
            <div className="border-t border-surface-700 p-4">
              <div className="mb-2 text-xs text-slate-500">受影响资源</div>
              <div className="flex flex-wrap gap-2">
                {inc.affected_resources.map((r, i) => (
                  <span
                    key={i}
                    className="rounded bg-surface-800 px-2 py-1 font-mono text-xs text-slate-300"
                  >
                    {r.kind}/{r.name}
                    <span className="text-slate-500"> · {r.namespace}</span>
                  </span>
                ))}
              </div>
            </div>
          )}
        </Card>

        {/* 调查入口 */}
        <Card>
          <CardHeader
            icon={<Search className="h-4 w-4 text-accent" />}
            title="调查"
          />
          <div className="p-4">
            {investigationIds.length === 0 ? (
              <p className="text-xs text-slate-500">
                暂无调查。点击右上角“发起调查”开始一次有界诊断。
              </p>
            ) : (
              <ul className="space-y-2">
                {investigationIds.map((id) => (
                  <li key={id}>
                    <Link
                      to={`/investigations/${id}`}
                      className="flex items-center justify-between rounded-md bg-surface-800 px-3 py-2 text-sm hover:bg-surface-700"
                    >
                      <span className="font-mono text-xs text-accent">
                        {id}
                      </span>
                      <span className="text-xs text-slate-500">查看 →</span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </Card>
      </div>
    </div>
  )
}
