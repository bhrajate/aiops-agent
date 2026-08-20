import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import {
  ScrollText,
  RefreshCw,
  ShieldQuestion,
  ShieldAlert,
  ChevronDown,
  ChevronRight,
} from 'lucide-react'
import { useAudit } from '@/hooks/queries'
import { useAuth } from '@/auth/context'
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
  Callout,
} from '@/components/ui'
import { AuditResultBadge } from '@/components/Badges'
import {
  cn,
  formatTime,
  relativeTime,
  actionLabel,
  formatCount,
} from '@/lib/format'
import { HttpError } from '@/api/client'
import type { AuditEntry, AuditResult } from '@/api/types'

const RESULT_OPTS: Array<{ value: AuditResult | ''; label: string }> = [
  { value: '', label: '全部' },
  { value: 'ok', label: '成功' },
  { value: 'denied', label: '已拒绝' },
  { value: 'error', label: '错误' },
]

const WINDOWS = [
  { value: 24, label: '24h' },
  { value: 168, label: '7d' },
  { value: 720, label: '30d' },
]

export function AuditPage() {
  const [params, setParams] = useSearchParams()
  const result = (params.get('result') ?? '') as AuditResult | ''
  const [hours, setHours] = useState(168)
  const [actor, setActor] = useState('')
  // 游标翻页:审计表持续写入,OFFSET 会在新记录插入时漏行,
  // 而这是问责依据,不能有"翻页时少了一条"。
  const [cursor, setCursor] = useState(0)

  const { canReadAudit } = useAuth()
  const { data, isLoading, error, refetch, isFetching } = useAudit({
    result: result || undefined,
    actor: actor.trim() || undefined,
    hours,
    limit: 100,
    before_id: cursor || undefined,
  })

  const deniedCount = useMemo(() => {
    return (data?.action_counts ?? [])
      .filter((c) => c.result === 'denied')
      .reduce((a, b) => a + b.count, 0)
  }, [data?.action_counts])

  if (!canReadAudit) {
    return (
      <>
        <PageHeader title="审计日志" />
        <div className="px-6 py-5">
          <Card>
            <EmptyState
              icon={<ShieldQuestion className="h-7 w-7" />}
              title="没有查看权限"
              hint="审计日志仅开放给 sre / admin —— 它跨命名空间,且包含被拒绝访问的目标 ID,本身就是敏感信息。"
            />
          </Card>
        </div>
      </>
    )
  }

  function setResult(v: string) {
    const next = new URLSearchParams(params)
    if (v) next.set('result', v)
    else next.delete('result')
    setParams(next, { replace: true })
    setCursor(0)
  }

  return (
    <>
      <PageHeader
        title="审计日志"
        subtitle={
          data
            ? `本页 ${data.count} 条${
                deniedCount > 0 ? ` · 窗口内 ${deniedCount} 次访问被拒绝` : ''
              }`
            : '加载中…'
        }
        actions={
          <>
            <SegmentedControl
              value={hours}
              options={WINDOWS}
              onChange={(v) => {
                setHours(v)
                setCursor(0)
              }}
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
          <div className="flex flex-wrap items-center gap-3">
            <input
              value={actor}
              onChange={(e) => {
                setActor(e.target.value)
                setCursor(0)
              }}
              placeholder="按操作者精确过滤(如 alice)"
              className={cn(inputCls, 'max-w-[240px]')}
            />
            <div className="flex items-center gap-1.5">
              <span className="text-2xs text-faint">结果</span>
              <SegmentedControl
                value={result}
                options={RESULT_OPTS}
                onChange={setResult}
                size="sm"
              />
            </div>
            {cursor > 0 && (
              <button
                onClick={() => setCursor(0)}
                className="text-2xs text-accent hover:underline"
              >
                回到最新
              </button>
            )}
          </div>
        }
      />

      <div className="anim-rise space-y-4 px-6 py-5">
        {deniedCount > 0 && result !== 'denied' && (
          <Callout tone="warn" icon={<ShieldAlert className="h-4 w-4" />}>
            窗口内有 {deniedCount} 次访问被拒绝。这类记录通常来自越权尝试或
            ABAC 范围外的访问 —— 值得逐条看过。
            <button
              onClick={() => setResult('denied')}
              className="ml-2 underline hover:no-underline"
            >
              只看被拒绝的
            </button>
          </Callout>
        )}

        {/* 动作聚合条:先看"发生了什么类型的操作",再决定要不要翻明细。 */}
        {data?.action_counts && data.action_counts.length > 0 && (
          <Card>
            <div className="flex flex-wrap gap-1.5 p-3">
              {data.action_counts.slice(0, 16).map((c) => (
                <span
                  key={`${c.action}-${c.result}`}
                  className={cn(
                    'inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-2xs',
                    c.result === 'denied'
                      ? 'border-danger/30 bg-danger/10 text-danger'
                      : 'border-line-soft bg-bg-soft text-muted',
                  )}
                >
                  {actionLabel(c.action)}
                  <span className="tabular font-mono">
                    {formatCount(c.count)}
                  </span>
                </span>
              ))}
            </div>
          </Card>
        )}

        <Card className="overflow-hidden">
          {isLoading ? (
            <Spinner label="加载审计日志…" />
          ) : error ? (
            <ErrorState
              message={
                error instanceof HttpError
                  ? `${error.message}(${error.code})`
                  : '加载失败'
              }
              onRetry={() => refetch()}
            />
          ) : !data || data.entries.length === 0 ? (
            <EmptyState
              icon={<ScrollText className="h-7 w-7" />}
              title="该条件下没有审计记录"
              hint="可放宽时间窗或清空过滤条件"
            />
          ) : (
            <ul className="divide-y divide-line-soft">
              {data.entries.map((e) => (
                <AuditRow key={e.id} entry={e} />
              ))}
            </ul>
          )}
        </Card>

        {data && data.next_cursor > 0 && data.entries.length >= 100 && (
          <div className="flex justify-center">
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setCursor(data.next_cursor)}
            >
              加载更早的记录
            </Button>
          </div>
        )}
      </div>
    </>
  )
}

function AuditRow({ entry }: { entry: AuditEntry }) {
  const [expanded, setExpanded] = useState(false)
  const hasDetail =
    (entry.detail && Object.keys(entry.detail).length > 0) ||
    (entry.scope && Object.keys(entry.scope).length > 0)

  // target 能链接到实际对象时给出跳转 —— 审计的价值在于能顺着记录
  // 走到出问题的那个对象上,而不是停在一串 ID。
  const targetLink =
    entry.target_type === 'incident' && entry.target_id
      ? `/incidents/${entry.target_id}`
      : entry.target_type === 'investigation' && entry.target_id
        ? `/investigations/${entry.target_id}`
        : null

  return (
    <li className="px-4 py-2.5">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <AuditResultBadge result={entry.result} />
            <span className="text-xs font-medium text-content">
              {actionLabel(entry.action)}
            </span>
            <span className="text-2xs text-muted">
              操作者 <span className="font-mono">{entry.actor}</span>
            </span>
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-faint">
            {entry.target_type && (
              <span>
                目标 {entry.target_type}
                {entry.target_id && (
                  <>
                    {' '}
                    {targetLink ? (
                      <Link
                        to={targetLink}
                        className="font-mono text-accent hover:underline"
                      >
                        {entry.target_id}
                      </Link>
                    ) : (
                      <span className="font-mono">{entry.target_id}</span>
                    )}
                  </>
                )}
              </span>
            )}
            {entry.scope?.['cluster'] != null && (
              <span>集群 {String(entry.scope['cluster'])}</span>
            )}
            {entry.scope?.['namespace'] != null && (
              <span>命名空间 {String(entry.scope['namespace'])}</span>
            )}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span
            className="tabular font-mono text-2xs text-faint"
            title={formatTime(entry.created_at)}
          >
            {relativeTime(entry.created_at)}
          </span>
          {hasDetail && (
            <button
              onClick={() => setExpanded((v) => !v)}
              aria-label="展开详情"
              className="rounded p-0.5 text-faint hover:bg-card-soft hover:text-content"
            >
              {expanded ? (
                <ChevronDown className="h-3.5 w-3.5" />
              ) : (
                <ChevronRight className="h-3.5 w-3.5" />
              )}
            </button>
          )}
        </div>
      </div>

      {expanded && hasDetail && (
        <div className="mt-2 space-y-1.5">
          {entry.scope && Object.keys(entry.scope).length > 0 && (
            <div>
              <div className="mb-0.5 text-2xs text-faint">scope</div>
              <Mono className="block overflow-x-auto whitespace-pre">
                {JSON.stringify(entry.scope, null, 2)}
              </Mono>
            </div>
          )}
          {entry.detail && Object.keys(entry.detail).length > 0 && (
            <div>
              <div className="mb-0.5 text-2xs text-faint">detail</div>
              <Mono className="block overflow-x-auto whitespace-pre">
                {JSON.stringify(entry.detail, null, 2)}
              </Mono>
            </div>
          )}
        </div>
      )}
    </li>
  )
}
