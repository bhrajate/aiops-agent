import { useState } from 'react'
import type { FeedbackAction, InvestigationPhase } from '@/api/types'
import { Button } from './ui'
import { useSendFeedback, useCancelInvestigation } from '@/hooks/queries'
import { useAuth } from '@/auth/context'
import { HttpError } from '@/api/client'
import { CheckCircle2, Edit3, XCircle, Ban } from 'lucide-react'

const ACTIVE_PHASES: InvestigationPhase[] = [
  'queued',
  'triaging',
  'triage_published',
  'planning',
  'collecting',
  'synthesizing',
]

export function FeedbackControls({
  investigationId,
  phase,
}: {
  investigationId: string
  phase: InvestigationPhase
}) {
  const [author, setAuthor] = useState('sre-oncall')
  const [correcting, setCorrecting] = useState(false)
  const [rootCause, setRootCause] = useState('')
  const [comment, setComment] = useState('')
  const [msg, setMsg] = useState<string | null>(null)

  const feedback = useSendFeedback(investigationId)
  const cancel = useCancelInvestigation(investigationId)
  const { canWrite } = useAuth()

  const isActive = ACTIVE_PHASES.includes(phase)
  const isClosed = phase === 'closed' || phase === 'cancelled'
  // 只读角色(viewer)禁用所有写操作(后端已强制,前端体验优化)
  const disabledWrite = isClosed || !canWrite

  async function submit(action: FeedbackAction) {
    setMsg(null)
    try {
      await feedback.mutateAsync({
        author: author || 'unknown',
        action,
        confirmed_root_cause:
          action === 'correct' ? rootCause || undefined : undefined,
        comment: comment || undefined,
      })
      setMsg(`已提交:${action}`)
      setCorrecting(false)
      setRootCause('')
      setComment('')
    } catch (e) {
      setMsg(e instanceof HttpError ? `失败:${e.message}` : '提交失败')
    }
  }

  async function doCancel() {
    setMsg(null)
    try {
      await cancel.mutateAsync()
      setMsg('已请求取消调查')
    } catch (e) {
      setMsg(e instanceof HttpError ? `失败:${e.message}` : '取消失败')
    }
  }

  return (
    <div className="space-y-3 p-4">
      <div>
        <label className="mb-1 block text-xs text-slate-400">操作人</label>
        <input
          value={author}
          onChange={(e) => setAuthor(e.target.value)}
          className="w-full rounded-md border border-surface-600 bg-surface-900 px-2.5 py-1.5 text-sm text-slate-200 outline-none focus:border-accent"
        />
      </div>

      <div>
        <label className="mb-1 block text-xs text-slate-400">
          备注(可选)
        </label>
        <input
          value={comment}
          onChange={(e) => setComment(e.target.value)}
          placeholder="补充说明…"
          className="w-full rounded-md border border-surface-600 bg-surface-900 px-2.5 py-1.5 text-sm text-slate-200 outline-none placeholder:text-slate-600 focus:border-accent"
        />
      </div>

      {correcting && (
        <div>
          <label className="mb-1 block text-xs text-amber-300">
            确认的根因(confirmed_root_cause)
          </label>
          <textarea
            value={rootCause}
            onChange={(e) => setRootCause(e.target.value)}
            rows={2}
            placeholder="填写你判断的真实根因…"
            className="w-full rounded-md border border-amber-500/40 bg-surface-900 px-2.5 py-1.5 text-sm text-slate-200 outline-none placeholder:text-slate-600 focus:border-amber-400"
          />
        </div>
      )}

      <div className="flex flex-wrap gap-2">
        <Button
          variant="primary"
          loading={feedback.isPending}
          disabled={disabledWrite}
          onClick={() => submit('confirm')}
        >
          <CheckCircle2 className="h-3.5 w-3.5" />
          确认
        </Button>

        {correcting ? (
          <Button
            variant="secondary"
            loading={feedback.isPending}
            disabled={disabledWrite || !rootCause}
            onClick={() => submit('correct')}
          >
            <Edit3 className="h-3.5 w-3.5" />
            提交纠错
          </Button>
        ) : (
          <Button
            variant="secondary"
            disabled={disabledWrite}
            onClick={() => setCorrecting(true)}
          >
            <Edit3 className="h-3.5 w-3.5" />
            纠错
          </Button>
        )}

        <Button
          variant="secondary"
          loading={feedback.isPending}
          disabled={disabledWrite}
          onClick={() => submit('close')}
        >
          <XCircle className="h-3.5 w-3.5" />
          关闭
        </Button>

        {isActive && canWrite && (
          <Button
            variant="danger"
            loading={cancel.isPending}
            onClick={doCancel}
          >
            <Ban className="h-3.5 w-3.5" />
            取消调查
          </Button>
        )}
      </div>

      {!canWrite && (
        <p className="text-xs text-amber-300/80">
          当前角色为只读(viewer),写操作已禁用。
        </p>
      )}

      {msg && <p className="text-xs text-slate-400">{msg}</p>}
    </div>
  )
}
