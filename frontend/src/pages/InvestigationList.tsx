import { useMemo } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import {
  RefreshCw,
  Search,
  ChevronRight,
  Timer,
  AlertTriangle,
  FileSearch,
  Lightbulb,
} from 'lucide-react'
import { useInvestigationList } from '@/hooks/queries'
import {
  PhaseBadge,
  SeverityBadge,
  DiagnosisStatusBadge,
  isActivePhase,
} from '@/components/Badges'
import {
  Card,
  PageHeader,
  Spinner,
  ErrorState,
  EmptyState,
  Button,
  SegmentedControl,
  ProgressBar,
  Callout,
} from '@/components/ui'
import {
  cn,
  formatCost,
  formatDuration,
  relativeTime,
  formatTime,
} from '@/lib/format'
import { HttpError } from '@/api/client'
import type { InvestigationListItem, Severity } from '@/api/types'

// 卡住判定与后端 overview 的 stallThreshold 保持一致(10 分钟)。
// 两处漂移会让总览说"3 个卡住"而列表里标出 5 个。
const STALL_MS = 10 * 60 * 1000

const SCOPE_OPTS = [
  { value: 'active', label: '进行中' },
  { value: 'all', label: '全部' },
]

export function InvestigationListPage() {
  const [params, setParams] = useSearchParams()
  // 默认只看进行中:这个页面回答的是"现在有什么在跑、什么卡住了"。
  const scope = params.get('active') === 'false' ? 'all' : 'active'
  const phase = params.get('phase') ?? ''

  const { data, isLoading, error, refetch, isFetching } = useInvestigationList({
    active: scope === 'active',
    phase: phase || undefined,
    limit: 200,
  })

  const stalled = useMemo(
    () =>
      (data ?? []).filter(
        (iv) =>
          isActivePhase(iv.phase) &&
          Date.now() - new Date(iv.started_at).getTime() > STALL_MS,
      ),
    [data],
  )

  function setScope(v: string) {
    const next = new URLSearchParams(params)
    if (v === 'all') next.set('active', 'false')
    else next.delete('active')
    setParams(next, { replace: true })
  }

  return (
    <>
      <PageHeader
        title="调查队列"
        subtitle={
          data
            ? `${data.length} 次调查${
                phase ? ` · 阶段 ${phase}` : ''
              }${stalled.length > 0 ? ` · ${stalled.length} 个疑似卡住` : ''}`
            : '加载中…'
        }
        actions={
          <>
            <SegmentedControl
              value={scope}
              options={SCOPE_OPTS}
              onChange={setScope}
            />
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
          </>
        }
        extra={
          phase ? (
            <button
              onClick={() => {
                const next = new URLSearchParams(params)
                next.delete('phase')
                setParams(next, { replace: true })
              }}
              className="text-2xs text-accent hover:underline"
            >
              清除阶段筛选({phase})
            </button>
          ) : undefined
        }
      />

      <div className="anim-rise space-y-4 px-6 py-5">
        {stalled.length > 0 && (
          <Callout tone="warn" icon={<Timer className="h-4 w-4" />}>
            <span className="font-medium">
              {stalled.length} 次调查已运行超过 10 分钟仍未结束
            </span>
            。默认预算是 300 秒,超出这么多通常意味着 worker 卡住或工具调用挂起 ——
            这类故障不会自行恢复,进度条也不会再动。
          </Callout>
        )}

        <Card className="overflow-hidden">
          {isLoading ? (
            <Spinner label="加载调查队列…" />
          ) : error ? (
            <ErrorState
              message={
                error instanceof HttpError
                  ? `${error.message}(${error.code})`
                  : '加载失败'
              }
              onRetry={() => refetch()}
            />
          ) : !data || data.length === 0 ? (
            <EmptyState
              icon={<Search className="h-7 w-7" />}
              title={
                scope === 'active' ? '当前没有进行中的调查' : '暂无调查记录'
              }
              hint={
                scope === 'active'
                  ? '切到"全部"可以看历史调查'
                  : '在告警详情页发起一次调查后会出现在这里'
              }
              action={
                scope === 'active' && (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => setScope('all')}
                  >
                    查看全部
                  </Button>
                )
              }
            />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[1000px] text-sm">
                <thead>
                  <tr className="border-b border-line-soft bg-bg-soft text-left text-2xs uppercase tracking-wide text-faint">
                    <th className="px-4 py-2.5 font-medium">阶段</th>
                    <th className="px-4 py-2.5 font-medium">Incident</th>
                    <th className="px-4 py-2.5 font-medium">触发</th>
                    <th className="px-4 py-2.5 font-medium">进度</th>
                    <th className="px-4 py-2.5 text-right font-medium">
                      证据
                    </th>
                    <th className="px-4 py-2.5 text-right font-medium">
                      假设
                    </th>
                    <th className="px-4 py-2.5 text-right font-medium">
                      成本
                    </th>
                    <th className="px-4 py-2.5 font-medium">诊断</th>
                    <th className="px-4 py-2.5 font-medium">开始</th>
                    <th className="px-4 py-2.5" />
                  </tr>
                </thead>
                <tbody>
                  {data.map((iv) => (
                    <InvestigationRow key={iv.investigation_id} iv={iv} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      </div>
    </>
  )
}

function InvestigationRow({ iv }: { iv: InvestigationListItem }) {
  const navigate = useNavigate()
  const startedMs = new Date(iv.started_at).getTime()
  const active = isActivePhase(iv.phase)
  const isStalled = active && Date.now() - startedMs > STALL_MS

  // 已结束用实际耗时,进行中用挂钟时间。
  // 不用 usage.elapsed_sec 判进行中的耗时:那是 worker 上报的,
  // worker 挂了它就不再更新 —— 而那正是要看见的情况。
  const elapsedSec = iv.ended_at
    ? (new Date(iv.ended_at).getTime() - startedMs) / 1000
    : (Date.now() - startedMs) / 1000

  const budgetSec = iv.budget?.max_duration_sec || 300
  const rounds = iv.usage?.rounds ?? 0
  const maxRounds = iv.budget?.max_rounds || 3

  return (
    <tr
      onClick={() => navigate(`/investigations/${iv.investigation_id}`)}
      className={cn(
        'cursor-pointer border-b border-line-soft transition-colors last:border-0 hover:bg-card-soft',
        isStalled && 'bg-warn/[0.04]',
      )}
    >
      <td className="px-4 py-3">
        <div className="flex items-center gap-1.5">
          <PhaseBadge phase={iv.phase} />
          {isStalled && (
            <span title="运行超过 10 分钟仍未结束,可能是 worker 卡住">
              <AlertTriangle className="h-3.5 w-3.5 text-warn" />
            </span>
          )}
        </div>
      </td>
      <td className="max-w-[240px] px-4 py-3">
        <div className="flex items-center gap-1.5">
          {iv.incident_severity && (
            <SeverityBadge severity={iv.incident_severity as Severity} />
          )}
          <span
            className="truncate text-content"
            title={iv.incident_title || iv.incident_id}
          >
            {iv.incident_title || iv.incident_id}
          </span>
        </div>
        <div className="mt-0.5 truncate font-mono text-2xs text-faint">
          {iv.namespace ? `${iv.namespace} · ` : ''}
          {iv.cluster_id}
        </div>
      </td>
      <td className="px-4 py-3 text-xs text-muted">
        <div className="truncate" title={iv.trigger_reason}>
          {iv.trigger_reason || '—'}
        </div>
        {iv.triggered_by && (
          <div className="mt-0.5 truncate text-2xs text-faint">
            {iv.triggered_by}
          </div>
        )}
      </td>
      <td className="w-[140px] px-4 py-3">
        <div className="flex items-center justify-between text-2xs text-faint">
          <span className="tabular">{formatDuration(elapsedSec)}</span>
          <span className="tabular">
            {rounds}/{maxRounds} 轮
          </span>
        </div>
        <ProgressBar
          className="mt-1"
          value={elapsedSec}
          max={budgetSec}
          tone={
            elapsedSec > budgetSec
              ? 'danger'
              : elapsedSec > budgetSec * 0.7
                ? 'warn'
                : 'accent'
          }
        />
      </td>
      <td className="tabular px-4 py-3 text-right text-xs text-muted">
        <span className="inline-flex items-center gap-1">
          <FileSearch className="h-3 w-3 text-faint" />
          {iv.evidence_count}
        </span>
      </td>
      <td className="tabular px-4 py-3 text-right text-xs text-muted">
        <span className="inline-flex items-center gap-1">
          <Lightbulb className="h-3 w-3 text-faint" />
          {iv.hypothesis_count}
        </span>
      </td>
      <td className="tabular px-4 py-3 text-right text-xs text-muted">
        {formatCost(iv.usage?.cost_usd)}
      </td>
      <td className="px-4 py-3">
        {iv.diagnosis ? (
          <DiagnosisStatusBadge status={iv.diagnosis.status} />
        ) : (
          <span className="text-2xs text-faint">—</span>
        )}
      </td>
      <td
        className="px-4 py-3 text-xs text-muted"
        title={formatTime(iv.started_at)}
      >
        {relativeTime(iv.started_at)}
      </td>
      <td className="px-4 py-3 text-right">
        <Link
          to={`/investigations/${iv.investigation_id}`}
          onClick={(e) => e.stopPropagation()}
          className="rounded p-1 text-faint hover:bg-card-soft hover:text-content"
          aria-label="查看调查详情"
        >
          <ChevronRight className="h-4 w-4" />
        </Link>
      </td>
    </tr>
  )
}
