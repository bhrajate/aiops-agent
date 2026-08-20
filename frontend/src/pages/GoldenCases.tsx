import { useState } from 'react'
import { Link } from 'react-router-dom'
import {
  FlaskConical,
  RefreshCw,
  Check,
  X,
  ExternalLink,
  ShieldQuestion,
} from 'lucide-react'
import { useGoldenCases, useReviewGoldenCase } from '@/hooks/queries'
import { useAuth } from '@/auth/context'
import {
  Card,
  PageHeader,
  Spinner,
  ErrorState,
  EmptyState,
  Button,
  SegmentedControl,
  Callout,
  Mono,
  inputCls,
} from '@/components/ui'
import { ReviewStatusBadge } from '@/components/Badges'
import { pushToast } from '@/components/Toast'
import {
  cn,
  formatTime,
  relativeTime,
  faultCategoryLabel,
} from '@/lib/format'
import { HttpError } from '@/api/client'
import type { GoldenCase, ReviewStatus } from '@/api/types'

const STATUS_OPTS: Array<{ value: ReviewStatus | 'all'; label: string }> = [
  { value: 'pending', label: '待审' },
  { value: 'approved', label: '已批准' },
  { value: 'rejected', label: '已驳回' },
  { value: 'all', label: '全部' },
]

export function GoldenCasesPage() {
  const [status, setStatus] = useState<ReviewStatus | 'all'>('pending')
  const { canReviewGolden } = useAuth()
  const { data, isLoading, error, refetch, isFetching } = useGoldenCases(status)

  if (!canReviewGolden) {
    return (
      <>
        <PageHeader title="评测集" />
        <div className="px-6 py-5">
          <Card>
            <EmptyState
              icon={<ShieldQuestion className="h-7 w-7" />}
              title="没有审核权限"
              hint="评测用例的审核仅开放给 sre / admin —— 批准一条用例意味着它进入评测集,而评测集决定发布质量门槛。"
            />
          </Card>
        </div>
      </>
    )
  }

  return (
    <>
      <PageHeader
        title="评测集"
        subtitle="人工反馈提升而来的用例,审核后进入离线回放的评测集"
        actions={
          <>
            <SegmentedControl
              value={status}
              options={STATUS_OPTS}
              onChange={setStatus}
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
      />

      <div className="anim-rise space-y-4 px-6 py-5">
        {status === 'pending' && (
          <Callout tone="info">
            这些用例来自值班人员的 confirm / correct 反馈 —— 那一刻系统第一次拥有了
            标注真值。审核这一步不能省:一条错误标注会让质量门槛失真,而这种失真极难
            发现(门槛照常通过或照常失败,只是标准错了)。
          </Callout>
        )}

        {isLoading ? (
          <Card>
            <Spinner label="加载评测用例…" />
          </Card>
        ) : error ? (
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
        ) : !data || data.length === 0 ? (
          <Card>
            <EmptyState
              icon={<FlaskConical className="h-7 w-7" />}
              title={
                status === 'pending' ? '没有待审用例' : '该状态下没有用例'
              }
              hint={
                status === 'pending'
                  ? '值班人员在调查页提交确认或纠正后,用例会自动出现在这里'
                  : undefined
              }
            />
          </Card>
        ) : (
          <div className="grid grid-cols-1 gap-3 xl:grid-cols-2">
            {data.map((c) => (
              <GoldenCaseCard key={c.case_id} item={c} />
            ))}
          </div>
        )}
      </div>
    </>
  )
}

function GoldenCaseCard({ item }: { item: GoldenCase }) {
  const review = useReviewGoldenCase()
  const [note, setNote] = useState('')
  const [showNote, setShowNote] = useState(false)
  const pending = item.review_status === 'pending'

  async function act(status: 'approved' | 'rejected') {
    try {
      await review.mutateAsync({ caseId: item.case_id, status, note })
      pushToast(
        status === 'approved' ? '已批准,该用例进入评测集' : '已驳回',
        'success',
      )
      setNote('')
      setShowNote(false)
    } catch (e) {
      pushToast(
        e instanceof HttpError ? `操作失败:${e.message}` : '操作失败',
        'error',
      )
    }
  }

  return (
    <Card className="flex flex-col p-4">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-1.5">
            <ReviewStatusBadge status={item.review_status} />
            {item.fault_category && (
              <Mono>{faultCategoryLabel(item.fault_category)}</Mono>
            )}
            {item.source === 'human_feedback' && (
              <span className="rounded bg-accent/15 px-1.5 py-0.5 text-2xs text-accent">
                人工反馈提升
              </span>
            )}
          </div>
          <p className="mt-2 text-sm text-content">{item.root_cause}</p>
          {item.affected_component && (
            <p className="mt-1 font-mono text-2xs text-faint">
              组件:{item.affected_component}
            </p>
          )}
        </div>
      </div>

      {item.expected_top_causes?.length > 0 && (
        <div className="mt-3">
          <div className="mb-1 text-2xs text-faint">
            期望命中的根因(Top-N 评测口径)
          </div>
          <ul className="space-y-1">
            {item.expected_top_causes.map((c, i) => (
              <li
                key={i}
                className="flex gap-2 rounded-lg border border-line-soft bg-bg-soft px-2 py-1 text-2xs text-muted"
              >
                <span className="tabular shrink-0 text-faint">{i + 1}.</span>
                <span className="min-w-0">{c}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-faint">
        {item.incident_id && (
          <Link
            to={`/incidents/${item.incident_id}`}
            className="inline-flex items-center gap-1 font-mono text-accent hover:underline"
          >
            {item.incident_id}
            <ExternalLink className="h-3 w-3" />
          </Link>
        )}
        {item.investigation_id && (
          <Link
            to={`/investigations/${item.investigation_id}`}
            className="inline-flex items-center gap-1 font-mono text-accent hover:underline"
          >
            {item.investigation_id}
            <ExternalLink className="h-3 w-3" />
          </Link>
        )}
        {item.promoted_by && <span>提升者 {item.promoted_by}</span>}
        <span title={formatTime(item.created_at)}>
          {relativeTime(item.created_at)}
        </span>
      </div>

      {/* 已审核的用例显示审核结论,让"谁在什么时候批的"可追溯 ——
          这是问责依据,不能只留下最终状态。 */}
      {!pending && (item.reviewed_by || item.review_note) && (
        <div className="mt-3 rounded-lg border border-line-soft bg-bg-soft px-2.5 py-2 text-2xs text-muted">
          {item.reviewed_by && (
            <div>
              审核者 {item.reviewed_by}
              {item.reviewed_at && ` · ${formatTime(item.reviewed_at)}`}
            </div>
          )}
          {item.review_note && (
            <div className="mt-0.5 text-faint">{item.review_note}</div>
          )}
        </div>
      )}

      {pending && (
        <div className="mt-4 border-t border-line-soft pt-3">
          {showNote && (
            <input
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder="审核备注(可选)"
              autoFocus
              className={cn(inputCls, 'mb-2')}
            />
          )}
          <div className="flex items-center gap-2">
            <Button
              variant="primary"
              size="sm"
              loading={review.isPending}
              onClick={() => act('approved')}
            >
              <Check className="h-3.5 w-3.5" />
              批准入集
            </Button>
            <Button
              variant="secondary"
              size="sm"
              loading={review.isPending}
              onClick={() => act('rejected')}
            >
              <X className="h-3.5 w-3.5" />
              驳回
            </Button>
            {!showNote && (
              <button
                onClick={() => setShowNote(true)}
                className="text-2xs text-faint hover:text-muted"
              >
                加备注
              </button>
            )}
          </div>
        </div>
      )}
    </Card>
  )
}
