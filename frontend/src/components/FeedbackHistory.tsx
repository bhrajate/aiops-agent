import type { Feedback, FeedbackAction } from '@/api/types'
import { Card, CardHeader } from './ui'
import { formatTime, cn } from '@/lib/format'
import { History, CheckCircle2, Edit3, XCircle } from 'lucide-react'

const ACTION_LABEL: Record<FeedbackAction, string> = {
  confirm: '确认',
  correct: '纠错',
  close: '关闭',
}

const ACTION_STYLE: Record<FeedbackAction, string> = {
  confirm: 'text-emerald-300',
  correct: 'text-amber-300',
  close: 'text-slate-300',
}

function ActionIcon({ action }: { action: FeedbackAction }) {
  const cls = cn('h-3.5 w-3.5', ACTION_STYLE[action])
  if (action === 'confirm') return <CheckCircle2 className={cls} />
  if (action === 'correct') return <Edit3 className={cls} />
  return <XCircle className={cls} />
}

export function FeedbackHistory({ feedback }: { feedback: Feedback[] }) {
  return (
    <Card>
      <CardHeader
        icon={<History className="h-4 w-4 text-accent" />}
        title="反馈历史"
        right={<span className="text-xs text-slate-500">{feedback.length} 条</span>}
      />
      <div className="p-2">
        {feedback.length === 0 ? (
          <p className="py-4 text-center text-xs text-slate-500">暂无人工反馈</p>
        ) : (
          <ul className="divide-y divide-surface-800">
            {feedback.map((f, i) => (
              <li key={f.feedback_id ?? i} className="px-2 py-2.5">
                <div className="flex items-center justify-between gap-2">
                  <span className="flex items-center gap-1.5 text-sm">
                    <ActionIcon action={f.action} />
                    <span className={cn('font-medium', ACTION_STYLE[f.action])}>
                      {ACTION_LABEL[f.action] ?? f.action}
                    </span>
                    <span className="text-slate-400">· {f.author}</span>
                  </span>
                  <span className="shrink-0 font-mono text-[11px] text-slate-500">
                    {formatTime(f.created_at)}
                  </span>
                </div>
                {f.confirmed_root_cause && (
                  <p className="mt-1 rounded bg-surface-800 px-2 py-1 text-xs text-amber-200">
                    根因:{f.confirmed_root_cause}
                  </p>
                )}
                {f.comment && (
                  <p className="mt-1 text-xs text-slate-400">{f.comment}</p>
                )}
                {f.review_status && (
                  <span className="mt-1 inline-block rounded bg-surface-800 px-1.5 py-0.5 font-mono text-[10px] text-slate-500">
                    {f.review_status}
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  )
}
