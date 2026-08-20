import { useMemo } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import {
  ArrowLeft,
  Play,
  Boxes,
  GitCommitHorizontal,
  Network,
  Search,
  Hand,
  CheckCheck,
  Share2,
  ArrowUpRight,
  ArrowDownRight,
} from 'lucide-react'
import {
  useIncident,
  useStartInvestigation,
  useUpdateIncidentStatus,
} from '@/hooks/queries'
import { useAuth } from '@/auth/context'
import {
  SeverityBadge,
  StatusBadge,
  PhaseBadge,
} from '@/components/Badges'
import {
  Card,
  CardHeader,
  PageHeader,
  Spinner,
  ErrorState,
  Button,
  InfoItem,
  Mono,
  Callout,
} from '@/components/ui'
import { AlertGroupsPanel } from '@/components/AlertGroupsPanel'
import { pushToast } from '@/components/Toast'
import {
  formatTime,
  formatBlastRadius,
  formatResource,
  faultCategoryLabel,
  relativeTime,
  pct,
} from '@/lib/format'
import { HttpError } from '@/api/client'
import type { Incident, InvestigationPhase } from '@/api/types'

interface InvestigationRef {
  id: string
  phase?: InvestigationPhase
  reason?: string
}

// 详情响应里调查引用有三个可能来源(当前调查 / ID 列表 / 对象列表),
// 后端在不同版本填不同字段。合并去重后按"有 phase 的优先"排,
// 让带状态的那条排在前面。
function resolveInvestigations(inc: Incident): InvestigationRef[] {
  const map = new Map<string, InvestigationRef>()
  const put = (ref: InvestigationRef) => {
    const prev = map.get(ref.id)
    if (!prev || (!prev.phase && ref.phase)) map.set(ref.id, ref)
  }
  if (inc.current_investigation_id) put({ id: inc.current_investigation_id })
  inc.investigation_ids?.forEach((id) => put({ id }))
  inc.investigations?.forEach((i) =>
    put({
      id: i.investigation_id,
      phase: i.phase,
      reason: i.trigger_reason,
    }),
  )
  return [...map.values()]
}

export function IncidentDetailPage() {
  const { incidentId } = useParams<{ incidentId: string }>()
  const navigate = useNavigate()
  const { data: inc, isLoading, error, refetch } = useIncident(incidentId)
  const start = useStartInvestigation()
  const updateStatus = useUpdateIncidentStatus()
  const { canWrite } = useAuth()

  const investigations = useMemo(
    () => (inc ? resolveInvestigations(inc) : []),
    [inc],
  )

  async function handleStart() {
    if (!incidentId) return
    try {
      const inv = await start.mutateAsync(incidentId)
      if (inv?.investigation_id) {
        navigate(`/investigations/${inv.investigation_id}`)
      } else {
        refetch()
      }
    } catch (err) {
      pushToast(
        err instanceof HttpError
          ? `发起调查失败:${err.message}`
          : '发起调查失败',
        'error',
      )
    }
  }

  async function handleStatus(status: 'acknowledged' | 'resolved') {
    if (!incidentId) return
    try {
      const res = await updateStatus.mutateAsync({ incidentId, status })
      if (res.changed) {
        pushToast(status === 'acknowledged' ? '已认领' : '已标记解决', 'success')
      } else {
        pushToast('状态未变化', 'info')
      }
    } catch (err) {
      pushToast(
        err instanceof HttpError ? `操作失败:${err.message}` : '操作失败',
        'error',
      )
    }
  }

  if (isLoading) {
    return (
      <>
        <PageHeader title="Incident" subtitle="加载中…" />
        <Spinner label="加载 Incident…" />
      </>
    )
  }

  if (error || !inc) {
    return (
      <>
        <PageHeader
          title="Incident"
          leading={
            <Link
              to="/incidents"
              className="inline-flex items-center gap-1 text-muted hover:text-content"
            >
              <ArrowLeft className="h-3.5 w-3.5" />
              返回告警列表
            </Link>
          }
        />
        <div className="px-6 py-5">
          <Card>
            <ErrorState
              message={
                error instanceof HttpError
                  ? `${error.message}(${error.code})`
                  : '加载失败'
              }
              onRetry={() => refetch()}
            />
          </Card>
        </div>
      </>
    )
  }

  const closed = inc.status === 'closed'

  return (
    <>
      <PageHeader
        leading={
          <Link
            to="/incidents"
            className="inline-flex items-center gap-1 text-muted hover:text-content"
          >
            <ArrowLeft className="h-3.5 w-3.5" />
            返回告警列表
          </Link>
        }
        title={
          <span className="flex items-center gap-2">
            <SeverityBadge severity={inc.severity} />
            <StatusBadge status={inc.status} />
            <span className="truncate">{inc.title || inc.incident_id}</span>
          </span>
        }
        subtitle={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="font-mono">{inc.incident_id}</span>
            <span>版本 v{inc.version}</span>
            {inc.cluster_id && <span>集群 {inc.cluster_id}</span>}
            <span>首次 {relativeTime(inc.first_seen)}</span>
          </span>
        }
        actions={
          canWrite ? (
            <>
              {inc.status === 'open' && (
                <Button
                  variant="secondary"
                  size="sm"
                  loading={updateStatus.isPending}
                  onClick={() => handleStatus('acknowledged')}
                >
                  <Hand className="h-3.5 w-3.5" />
                  认领
                </Button>
              )}
              {!closed && inc.status !== 'resolved' && (
                <Button
                  variant="secondary"
                  size="sm"
                  loading={updateStatus.isPending}
                  onClick={() => handleStatus('resolved')}
                  title="标记为已解决。彻底关闭请在调查页提交 close 反馈。"
                >
                  <CheckCheck className="h-3.5 w-3.5" />
                  标记解决
                </Button>
              )}
              {!closed && (
                <Button
                  variant="primary"
                  size="sm"
                  loading={start.isPending}
                  onClick={handleStart}
                >
                  <Play className="h-3.5 w-3.5" />
                  发起调查
                </Button>
              )}
            </>
          ) : (
            <span className="text-2xs text-faint">
              当前角色只读,无法发起调查
            </span>
          )
        }
      />

      <div className="anim-rise space-y-4 px-6 py-5">
        {closed && (
          <Callout tone="info">
            该 Incident 已关闭。重新打开需要新的 Signal 聚合 ——
            关闭是终态,把旧记录改回去会让 first_seen 与状态历史矛盾。
          </Callout>
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
              <InfoItem
                label="故障类别"
                value={
                  inc.fault_category
                    ? faultCategoryLabel(inc.fault_category)
                    : '未分类'
                }
              />
              <InfoItem
                label="聚合信号数"
                value={inc.signal_count ?? '—'}
              />
              <InfoItem label="首次出现" value={formatTime(inc.first_seen)} />
              <InfoItem label="最后出现" value={formatTime(inc.last_seen)} />
              <InfoItem
                label={inc.resolved_at ? '解决时间' : '关闭时间'}
                value={
                  formatTime(inc.resolved_at ?? inc.closed_at ?? undefined)
                }
              />
              <InfoItem
                label="拓扑引用"
                value={
                  <span className="inline-flex items-center gap-1">
                    <Network className="h-3.5 w-3.5 text-faint" />
                    {inc.topology_refs?.length ?? 0}
                  </span>
                }
              />
              <InfoItem
                label="变更引用"
                value={
                  <span className="inline-flex items-center gap-1">
                    <GitCommitHorizontal className="h-3.5 w-3.5 text-faint" />
                    {inc.change_refs?.length ?? 0}
                  </span>
                }
              />
              <InfoItem
                label="聚合键"
                value={
                  <span
                    className="block truncate font-mono text-2xs text-muted"
                    title={inc.grouping_key}
                  >
                    {inc.grouping_key || '—'}
                  </span>
                }
              />
            </div>

            {inc.affected_resources?.length > 0 && (
              <div className="border-t border-line-soft p-4">
                <div className="mb-2 text-xs text-muted">受影响资源</div>
                <div className="flex flex-wrap gap-1.5">
                  {inc.affected_resources.map((r, i) => (
                    <Mono key={i}>
                      {formatResource(r)}
                      {r.namespace && (
                        <span className="text-faint"> · {r.namespace}</span>
                      )}
                    </Mono>
                  ))}
                </div>
              </div>
            )}
          </Card>

          <div className="space-y-4">
            <Card>
              <CardHeader
                icon={<Search className="h-4 w-4 text-accent" />}
                title="调查"
                right={
                  <span className="text-2xs text-faint">
                    {investigations.length} 次
                  </span>
                }
              />
              <div className="p-3">
                {investigations.length === 0 ? (
                  <p className="px-1 py-4 text-xs text-faint">
                    暂无调查。发起一次有界诊断后,证据与假设会出现在这里。
                  </p>
                ) : (
                  <ul className="space-y-1.5">
                    {investigations.map((iv) => (
                      <li key={iv.id}>
                        <Link
                          to={`/investigations/${iv.id}`}
                          className="flex items-center justify-between gap-2 rounded-lg border border-line-soft bg-bg-soft px-3 py-2 transition-colors hover:border-line hover:bg-card-soft"
                        >
                          <span className="min-w-0">
                            <span className="block truncate font-mono text-2xs text-accent">
                              {iv.id}
                            </span>
                            {iv.reason && (
                              <span className="mt-0.5 block truncate text-2xs text-faint">
                                触发:{iv.reason}
                              </span>
                            )}
                          </span>
                          {iv.phase && <PhaseBadge phase={iv.phase} />}
                        </Link>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </Card>

            {/* 拓扑关联:疑似同源的其他 incident。
                后端刻意不合并而是链接 —— 一条误判的拓扑边会把两次无关故障
                焊死成一个 incident,而拆分比合并难得多。 */}
            <Card>
              <CardHeader
                icon={<Share2 className="h-4 w-4 text-accent" />}
                title="疑似同源"
                subtitle="拓扑相邻,未合并"
              />
              <div className="p-3">
                {!inc.relations || inc.relations.length === 0 ? (
                  <p className="px-1 py-4 text-xs text-faint">
                    没有发现拓扑上相关的其他 Incident。
                  </p>
                ) : (
                  <ul className="space-y-1.5">
                    {inc.relations.map((rel) => (
                      <li key={`${rel.related_incident_id}-${rel.relation}`}>
                        <Link
                          to={`/incidents/${rel.related_incident_id}`}
                          className="flex items-center gap-2 rounded-lg border border-line-soft bg-bg-soft px-3 py-2 transition-colors hover:border-line hover:bg-card-soft"
                        >
                          {rel.relation === 'upstream' ? (
                            <ArrowUpRight className="h-3.5 w-3.5 shrink-0 text-warn" />
                          ) : (
                            <ArrowDownRight className="h-3.5 w-3.5 shrink-0 text-info" />
                          )}
                          <span className="min-w-0 flex-1">
                            <span className="block truncate font-mono text-2xs text-accent">
                              {rel.related_incident_id}
                            </span>
                            <span className="text-2xs text-faint">
                              {rel.relation === 'upstream'
                                ? '调用链上游'
                                : '调用链下游'}
                              {' · 置信度 '}
                              {pct(rel.confidence)}
                            </span>
                          </span>
                        </Link>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </Card>
          </div>
        </div>

        {/* 两层聚合模型:该 Incident 下的告警去重单元明细 */}
        <AlertGroupsPanel groups={inc.alert_groups ?? []} />
      </div>
    </>
  )
}
