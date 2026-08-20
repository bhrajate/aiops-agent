import { useState } from 'react'
import type { FeedbackAction, InvestigationPhase } from '@/api/types'
import { Button, Field, inputCls, Callout } from './ui'
import { useSendFeedback, useCancelInvestigation } from '@/hooks/queries'
import { useAuth } from '@/auth/context'
import { pushToast } from './Toast'
import { HttpError } from '@/api/client'
import { isActivePhase } from './Badges'
import { cn } from '@/lib/format'
import { CheckCircle2, Edit3, XCircle, Ban, ThumbsDown } from 'lucide-react'

export function FeedbackControls({
  investigationId,
  phase,
}: {
  investigationId: string
  phase: InvestigationPhase
}) {
  // 需要展开输入框的动作。correct 必须填根因(它是标注真值),
  // reject 建议填备注说明为什么错。
  const [expanded, setExpanded] = useState<'correct' | 'reject' | null>(null)
  const [rootCause, setRootCause] = useState('')
  const [comment, setComment] = useState('')

  const feedback = useSendFeedback(investigationId)
  const cancel = useCancelInvestigation(investigationId)
  const { canWrite, user } = useAuth()

  const active = isActivePhase(phase)
  const closed = phase === 'closed' || phase === 'cancelled'
  const disabledWrite = closed || !canWrite

  async function submit(action: FeedbackAction) {
    try {
      await feedback.mutateAsync({
        // author 由后端以认证身份覆盖(不信任 body),这里带上只为本地回显。
        author: user?.sub ?? 'unknown',
        action,
        confirmed_root_cause:
          action === 'correct' ? rootCause || undefined : undefined,
        comment: comment || undefined,
      })
      pushToast(
        action === 'confirm'
          ? '已确认,该调查将提升为待审评测用例'
          : action === 'correct'
            ? '已提交纠正,标注根因将进入待审评测集'
            : action === 'close'
              ? '已关闭调查与 Incident'
              : '已记录否决',
        'success',
      )
      setExpanded(null)
      setRootCause('')
      setComment('')
    } catch (e) {
      pushToast(
        e instanceof HttpError ? `提交失败:${e.message}` : '提交失败',
        'error',
      )
    }
  }

  async function doCancel() {
    try {
      await cancel.mutateAsync()
      pushToast('已请求取消调查', 'success')
    } catch (e) {
      pushToast(
        e instanceof HttpError ? `取消失败:${e.message}` : '取消失败',
        'error',
      )
    }
  }

  if (!canWrite) {
    return (
      <div className="p-4">
        <Callout tone="info">
          当前角色为只读(viewer),无法提交反馈或取消调查。
        </Callout>
      </div>
    )
  }

  return (
    <div className="space-y-3 p-4">
      {closed && (
        <Callout tone="info">
          该调查已是终态,不再接受反馈。
        </Callout>
      )}

      <Field label="备注(可选)">
        <input
          value={comment}
          onChange={(e) => setComment(e.target.value)}
          placeholder="补充说明…"
          disabled={disabledWrite}
          className={inputCls}
        />
      </Field>

      {expanded === 'correct' && (
        <Field
          label={<span className="text-warn">你判断的真实根因</span>}
          hint="这句话会作为标注真值进入待审评测集,请写具体结论而非现象"
        >
          <textarea
            value={rootCause}
            onChange={(e) => setRootCause(e.target.value)}
            rows={3}
            autoFocus
            placeholder="例:v2.3.1 引入的连接池配置把上限从 100 降到 10,高峰期请求排队超时"
            className={cn(inputCls, 'border-warn/40 focus:border-warn')}
          />
        </Field>
      )}

      <div className="flex flex-wrap gap-2">
        <Button
          variant="primary"
          size="sm"
          loading={feedback.isPending}
          disabled={disabledWrite}
          onClick={() => submit('confirm')}
          title="认可当前结论。排名第一的假设将作为标注真值"
        >
          <CheckCircle2 className="h-3.5 w-3.5" />
          确认
        </Button>

        {expanded === 'correct' ? (
          <Button
            variant="secondary"
            size="sm"
            loading={feedback.isPending}
            disabled={disabledWrite || !rootCause.trim()}
            onClick={() => submit('correct')}
          >
            <Edit3 className="h-3.5 w-3.5" />
            提交纠正
          </Button>
        ) : (
          <Button
            variant="secondary"
            size="sm"
            disabled={disabledWrite}
            onClick={() => setExpanded('correct')}
            title="结论不对,我知道正确答案"
          >
            <Edit3 className="h-3.5 w-3.5" />
            纠正
          </Button>
        )}

        <Button
          variant="secondary"
          size="sm"
          loading={feedback.isPending}
          disabled={disabledWrite}
          onClick={() => submit('reject')}
          title="结论不对,但我也还不知道正确答案"
        >
          <ThumbsDown className="h-3.5 w-3.5" />
          否决
        </Button>

        <Button
          variant="secondary"
          size="sm"
          loading={feedback.isPending}
          disabled={disabledWrite}
          onClick={() => submit('close')}
          title="关闭调查与对应 Incident"
        >
          <XCircle className="h-3.5 w-3.5" />
          关闭
        </Button>

        {active && (
          <Button
            variant="danger"
            size="sm"
            loading={cancel.isPending}
            onClick={doCancel}
            title="中止正在运行的调查"
          >
            <Ban className="h-3.5 w-3.5" />
            取消调查
          </Button>
        )}
      </div>

      {/* 说明三个动作的语义差别。这不是废话:confirm 与 reject 都表示
          "我看过了",但只有前者给出了标注真值,评测集的质量取决于人是否
          理解这个区别。 */}
      <p className="text-2xs leading-relaxed text-faint">
        确认与纠正会把这次调查提升为
        <span className="text-muted">待审</span>
        评测用例(需 SRE 复核后才进入评测集);否决只记录"结论错误",不产生用例 ——
        因为它没有给出正确答案。
      </p>
    </div>
  )
}
