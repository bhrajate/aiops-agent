import type { Feedback, FeedbackAction } from '@/api/types'
import { Card, CardHeader } from './ui'
import { ReviewStatusBadge } from './Badges'
import { formatTime, relativeTime, cn, feedbackLabel } from '@/lib/format'
import { History, CheckCircle2, Edit3, XCircle, Ban } from 'lucide-react'
import type { ReviewStatus } from '@/api/types'

const ACTION_STYLE: Record<string, string> = {
  confirm: 'text-ok',
  correct: 'text-warn',
  reject: 'text-danger',
  close: 'text-muted',
}

function ActionIcon({ action }: { action: FeedbackAction | string }) {
  const cls = cn('h-3.5 w-3.5', ACTION_STYLE[action] ?? 'text-muted')
  if (action === 'confirm') return <CheckCircle2 className={cls} />
  if (action === 'correct') return <Edit3 className={cls} />
  if (action === 'reject') return <Ban className={cls} />
  return <XCircle className={cls} />
}

export function FeedbackHistory({ feedback }: { feedback: Feedback[] }) {
  return (
    <Card>
      <CardHeader
        icon={<History className="h-4 w-4 text-accent" />}
        title="反馈历史"
        right={
          <span className="text-2xs text-faint">{feedback.length} 条</span>
        }
      />
      <div className="p-2">
        {feedback.length === 0 ? (
          <p className="py-4 text-center text-xs text-faint">
            暂无人工反馈
          </p>
        ) : (
          <ul className="divide-y divide-line-soft">
            {feedback.map((f, i) => (
              <li key={f.feedback_id ?? i} className="px-2 py-2.5">
                <div className="flex items-center justify-between gap-2">
                  <span className="flex min-w-0 items-center gap-1.5 text-xs">
                    <ActionIcon action={f.action} />
                    <span
                      className={cn(
                        'font-medium',
                        ACTION_STYLE[f.action] ?? 'text-muted',
                      )}
                    >
                      {feedbackLabel(f.action)}
                    </span>
                    <span className="truncate text-muted">· {f.author}</span>
                  </span>
                  <span
                    className="shrink-0 font-mono text-2xs text-faint"
                    title={formatTime(f.created_at)}
                  >
                    {relativeTime(f.created_at)}
                  </span>
                </div>
                {f.confirmed_root_cause && (
                  <p className="mt-1.5 rounded-lg border border-warn/25 bg-warn/10 px-2 py-1.5 text-2xs text-warn">
                    标注根因:{f.confirmed_root_cause}
                  </p>
                )}
                {f.comment && (
                  <p className="mt-1 text-2xs text-muted">{f.comment}</p>
                )}
                {/* review_status 是这条反馈提升成的评测用例的审核状态。
                    confirm/correct 会自动提升为待审用例(见后端 promoteGoldenCase)。 */}
                {f.review_status && (
                  <div className="mt-1.5">
                    <ReviewStatusBadge
                      status={f.review_status as ReviewStatus}
                    />
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  )
}
