import { useMemo, useState } from 'react'
import { Link, useSearchParams, useNavigate } from 'react-router-dom'
import {
  FlaskConical,
  RefreshCw,
  ChevronRight,
  Search,
  Hand,
  CheckCheck,
  Siren,
} from 'lucide-react'
import type { IncidentStatus, Severity, Incident } from '@/api/types'
import {
  useIncidents,
  useStartInvestigation,
  useUpdateIncidentStatus,
} from '@/hooks/queries'
import { useAuth } from '@/auth/context'
import { SeverityBadge, StatusBadge } from '@/components/Badges'
import {
  Card,
  PageHeader,
  Spinner,
  ErrorState,
  EmptyState,
  Button,
  SegmentedControl,
  Mono,
  inputCls,
} from '@/components/ui'
import { SignalInjector } from '@/components/SignalInjector'
import { pushToast } from '@/components/Toast'
import {
  formatTime,
  relativeTime,
  formatBlastRadius,
  formatResource,
  faultCategoryLabel,
  cn,
} from '@/lib/format'
import { HttpError } from '@/api/client'

const STATUS_OPTS: Array<{ value: IncidentStatus | ''; label: string }> = [
  { value: '', label: '全部' },
  { value: 'open', label: '未认领' },
  { value: 'acknowledged', label: '处置中' },
  { value: 'resolved', label: '已解决' },
  { value: 'closed', label: '已关闭' },
]

const SEV_OPTS: Array<{ value: Severity | ''; label: string }> = [
  { value: '', label: '全部' },
  { value: 'P1', label: 'P1' },
  { value: 'P2', label: 'P2' },
  { value: 'P3', label: 'P3' },
  { value: 'P4', label: 'P4' },
]

export function IncidentListPage() {
  // 筛选走 URL:命令面板的"仅看 P1"要能直接落到这个页面,
  // 且值班人员分享链接时对方看到的是同一个视图。
  const [params, setParams] = useSearchParams()
  const status = (params.get('status') ?? '') as IncidentStatus | ''
  const severity = (params.get('severity') ?? '') as Severity | ''
  const [keyword, setKeyword] = useState('')
  const [showInjector, setShowInjector] = useState(false)

  const { canWrite } = useAuth()
  const { data, isLoading, error, refetch, isFetching } = useIncidents({
    status: status || undefined,
    severity: severity || undefined,
    limit: 200,
  })

  function setFilter(key: string, value: string) {
    const next = new URLSearchParams(params)
    if (value) next.set(key, value)
    else next.delete(key)
    setParams(next, { replace: true })
  }

  // 关键词在前端过滤:后端没有全文检索端点,而值班时最常见的操作是
  // "在当前这屏里找 payment"。不假装是服务端搜索 —— 提示里写明范围。
  const rows = useMemo(() => {
    const list = data ?? []
    const q = keyword.trim().toLowerCase()
    if (!q) return list
    return list.filter((inc) => {
      const hay = [
        inc.incident_id,
        inc.title,
        inc.fault_category,
        inc.grouping_key,
        ...(inc.affected_resources ?? []).flatMap((r) => [
          r.name,
          r.namespace,
          r.kind,
        ]),
      ]
        .filter(Boolean)
        .join(' ')
        .toLowerCase()
      return hay.includes(q)
    })
  }, [data, keyword])

  const openCount = useMemo(
    () => (data ?? []).filter((i) => i.status === 'open').length,
    [data],
  )

  return (
    <>
      <PageHeader
        title="告警"
        subtitle={
          data
            ? `${rows.length} 条${keyword ? ` / 共 ${data.length}` : ''}${
                openCount > 0 ? ` · ${openCount} 个未认领` : ''
              }`
            : '加载中…'
        }
        actions={
          <>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => refetch()}
              aria-label="刷新"
            >
              <RefreshCw
                className={cn('h-4 w-4', isFetching && 'animate-spin')}
              />
            </Button>
            {canWrite && (
              <Button
                variant="primary"
                size="sm"
                onClick={() => setShowInjector(true)}
              >
                <FlaskConical className="h-3.5 w-3.5" />
                注入 Signal
              </Button>
            )}
          </>
        }
        extra={
          <div className="flex flex-wrap items-center gap-3">
            <div className="relative min-w-[220px] flex-1 md:max-w-xs">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-faint" />
              <input
                value={keyword}
                onChange={(e) => setKeyword(e.target.value)}
                placeholder="过滤当前结果:资源 / 类别 / ID"
                className={cn(inputCls, 'pl-8')}
              />
            </div>
            <div className="flex items-center gap-1.5">
              <span className="text-2xs text-faint">状态</span>
              <SegmentedControl
                value={status}
                options={STATUS_OPTS}
                onChange={(v) => setFilter('status', v)}
                size="sm"
              />
            </div>
            <div className="flex items-center gap-1.5">
              <span className="text-2xs text-faint">级别</span>
              <SegmentedControl
                value={severity}
                options={SEV_OPTS}
                onChange={(v) => setFilter('severity', v)}
                size="sm"
              />
            </div>
          </div>
        }
      />

      <div className="anim-rise px-6 py-5">
        <Card className="overflow-hidden">
          {isLoading ? (
            <Spinner label="加载 Incident…" />
          ) : error ? (
            <ErrorState
              message={
                error instanceof HttpError
                  ? `${error.message}(${error.code})`
                  : '加载失败,请确认后端 :8088 已启动'
              }
              onRetry={() => refetch()}
            />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<Siren className="h-7 w-7" />}
              title={
                keyword || status || severity
                  ? '当前筛选下没有 Incident'
                  : '暂无 Incident'
              }
              hint={
                keyword || status || severity
                  ? '可清空筛选条件后再看'
                  : '可通过右上角"注入 Signal"模拟一次端到端流程'
              }
              action={
                (keyword || status || severity) && (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => {
                      setKeyword('')
                      setParams(new URLSearchParams(), { replace: true })
                    }}
                  >
                    清空筛选
                  </Button>
                )
              }
            />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[900px] text-sm">
                <thead>
                  <tr className="border-b border-line-soft bg-bg-soft text-left text-2xs uppercase tracking-wide text-faint">
                    <th className="px-4 py-2.5 font-medium">级别</th>
                    <th className="px-4 py-2.5 font-medium">状态</th>
                    <th className="px-4 py-2.5 font-medium">标题 / 资源</th>
                    <th className="px-4 py-2.5 font-medium">类别</th>
                    <th className="px-4 py-2.5 font-medium">影响范围</th>
                    <th className="px-4 py-2.5 text-right font-medium">
                      信号
                    </th>
                    <th className="px-4 py-2.5 font-medium">最后出现</th>
                    <th className="px-4 py-2.5" />
                  </tr>
                </thead>
                <tbody>
                  {rows.map((inc) => (
                    <IncidentRow key={inc.incident_id} inc={inc} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>

        {data && data.length >= 200 && (
          <p className="mt-2 text-2xs text-faint">
            仅显示最近 200 条。更早的记录请用总览的时间窗或按状态筛选收窄。
          </p>
        )}
      </div>

      {showInjector && (
        <SignalInjector onClose={() => setShowInjector(false)} />
      )}
    </>
  )
}

function IncidentRow({ inc }: { inc: Incident }) {
  const navigate = useNavigate()
  const { canWrite } = useAuth()
  const start = useStartInvestigation()
  const updateStatus = useUpdateIncidentStatus()

  const primary = inc.affected_resources?.[0]
  const extraResources = (inc.affected_resources?.length ?? 0) - 1

  async function handleAck(e: React.MouseEvent) {
    e.stopPropagation()
    try {
      const res = await updateStatus.mutateAsync({
        incidentId: inc.incident_id,
        status: 'acknowledged',
      })
      pushToast(
        res.changed ? '已认领' : '该 Incident 已是处置中状态',
        res.changed ? 'success' : 'info',
      )
    } catch (err) {
      pushToast(
        err instanceof HttpError ? `认领失败:${err.message}` : '认领失败',
        'error',
      )
    }
  }

  async function handleStart(e: React.MouseEvent) {
    e.stopPropagation()
    try {
      const inv = await start.mutateAsync(inc.incident_id)
      if (inv?.investigation_id) {
        navigate(`/investigations/${inv.investigation_id}`)
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

  return (
    <tr
      onClick={() => navigate(`/incidents/${inc.incident_id}`)}
      className="cursor-pointer border-b border-line-soft transition-colors last:border-0 hover:bg-card-soft"
    >
      <td className="px-4 py-3">
        <SeverityBadge severity={inc.severity} showDot={inc.severity === 'P1'} />
      </td>
      <td className="px-4 py-3">
        <StatusBadge status={inc.status} />
      </td>
      <td className="max-w-[280px] px-4 py-3">
        <div className="truncate text-content" title={inc.title}>
          {inc.title || inc.incident_id}
        </div>
        <div className="mt-0.5 truncate font-mono text-2xs text-faint">
          {primary ? (
            <>
              {formatResource(primary)}
              {primary.namespace && (
                <span className="text-faint"> · {primary.namespace}</span>
              )}
              {extraResources > 0 && (
                <span className="text-faint"> +{extraResources}</span>
              )}
            </>
          ) : (
            inc.incident_id
          )}
        </div>
      </td>
      <td className="px-4 py-3">
        {inc.fault_category ? (
          <Mono>{faultCategoryLabel(inc.fault_category)}</Mono>
        ) : (
          <span className="text-faint">—</span>
        )}
      </td>
      <td className="px-4 py-3 text-xs text-muted">
        {formatBlastRadius(inc.blast_radius)}
      </td>
      <td className="tabular px-4 py-3 text-right text-xs text-muted">
        {inc.signal_count ?? '—'}
      </td>
      <td
        className="px-4 py-3 text-xs text-muted"
        title={`首次 ${formatTime(inc.first_seen)}\n最后 ${formatTime(inc.last_seen)}`}
      >
        {relativeTime(inc.last_seen)}
      </td>
      <td className="px-4 py-3">
        <div className="flex items-center justify-end gap-1">
          {/* 行内动作只放"认领"与"发起调查":值班时这两个是高频操作,
              点进详情再做会多两次跳转。解决/关闭需要看过结论才做,
              刻意只在详情页给。 */}
          {canWrite && inc.status === 'open' && (
            <Button
              size="sm"
              variant="subtle"
              loading={updateStatus.isPending}
              onClick={handleAck}
              title="认领:让其他值班人员知道你在看了"
            >
              <Hand className="h-3 w-3" />
              认领
            </Button>
          )}
          {canWrite &&
            inc.status !== 'closed' &&
            inc.status !== 'resolved' && (
              <Button
                size="sm"
                variant="subtle"
                loading={start.isPending}
                onClick={handleStart}
                title="发起一次有界的自动调查"
              >
                <Search className="h-3 w-3" />
                调查
              </Button>
            )}
          {inc.status === 'resolved' && (
            <span
              className="inline-flex items-center gap-1 text-2xs text-ok"
              title="已标记解决"
            >
              <CheckCheck className="h-3 w-3" />
            </span>
          )}
          <Link
            to={`/incidents/${inc.incident_id}`}
            onClick={(e) => e.stopPropagation()}
            className="rounded p-1 text-faint hover:bg-card-soft hover:text-content"
            aria-label="查看详情"
          >
            <ChevronRight className="h-4 w-4" />
          </Link>
        </div>
      </td>
    </tr>
  )
}
