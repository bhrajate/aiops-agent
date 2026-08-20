import { useState, useMemo } from 'react'
import { useParams, Link } from 'react-router-dom'
import {
  ArrowLeft,
  Gauge,
  Lightbulb,
  MessageSquare,
  Printer,
  Timer,
} from 'lucide-react'
import { useInvestigation } from '@/hooks/queries'
import { useSSE } from '@/hooks/useSSE'
import { PhaseBadge, phaseLabel, isActivePhase } from '@/components/Badges'
import {
  Card,
  CardHeader,
  PageHeader,
  Spinner,
  ErrorState,
  EmptyState,
  Button,
  Callout,
  Mono,
} from '@/components/ui'
import { BudgetPanel } from '@/components/BudgetPanel'
import { HypothesisCard } from '@/components/HypothesisCard'
import { DiagnosisPanel } from '@/components/DiagnosisPanel'
import { Timeline } from '@/components/Timeline'
import { FeedbackControls } from '@/components/FeedbackControls'
import { FeedbackHistory } from '@/components/FeedbackHistory'
import { ToolCallsPanel } from '@/components/ToolCallsPanel'
import { EvidenceModal } from '@/components/EvidenceModal'
import { HttpError } from '@/api/client'
import { formatDuration, formatTime, relativeTime } from '@/lib/format'

// 卡住判定与后端 overview / 调查队列一致(10 分钟)。
const STALL_MS = 10 * 60 * 1000

export function InvestigationViewPage() {
  const { investigationId } = useParams<{ investigationId: string }>()
  const [evidenceId, setEvidenceId] = useState<string | null>(null)

  const {
    data: inv,
    isLoading,
    error,
    refetch,
  } = useInvestigation(investigationId, true)

  const live = inv ? isActivePhase(inv.phase) : true
  // 仅在非终态订阅 SSE:终态后服务端会发 done 并关流,
  // 继续订阅只会拿到一次立即断开的连接。
  const { events, status: sseStatus } = useSSE(investigationId, live)

  const hypotheses = useMemo(
    () =>
      [...(inv?.hypotheses ?? [])].sort(
        (a, b) => (a.rank ?? 99) - (b.rank ?? 99),
      ),
    [inv?.hypotheses],
  )

  // 后端不返回独立 tool_calls;从 evidence 派生(每条 evidence 记录一次工具调用)。
  const toolCalls = useMemo(() => {
    if (inv?.tool_calls?.length) return inv.tool_calls
    return (inv?.evidence ?? []).map((ev) => ({
      tool_name: ev.tool_name ?? '(未知工具)',
      status: ev.redaction_status,
      scope: ev.query?.scope as Record<string, unknown> | undefined,
      evidence_id: ev.evidence_id,
      started_at: ev.created_at,
      finished_at: ev.created_at,
    }))
  }, [inv?.tool_calls, inv?.evidence])

  if (isLoading) {
    return (
      <>
        <PageHeader title="调查" subtitle="加载中…" />
        <Spinner label="加载调查…" />
      </>
    )
  }

  if (error || !inv) {
    return (
      <>
        <PageHeader
          title="调查"
          leading={
            <Link
              to="/investigations"
              className="inline-flex items-center gap-1 text-muted hover:text-content"
            >
              <ArrowLeft className="h-3.5 w-3.5" />
              返回调查队列
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

  const startedMs = new Date(inv.started_at).getTime()
  // 进行中的耗时用挂钟时间,不用 usage.elapsed_sec:后者由 worker 上报,
  // worker 挂了就不再更新 —— 而那正是要看见的情况。
  const elapsed = Number.isFinite(startedMs)
    ? inv.ended_at
      ? (new Date(inv.ended_at).getTime() - startedMs) / 1000
      : (Date.now() - startedMs) / 1000
    : (inv.usage?.elapsed_sec ?? 0)
  const stalled =
    isActivePhase(inv.phase) &&
    Number.isFinite(startedMs) &&
    Date.now() - startedMs > STALL_MS

  return (
    <>
      <PageHeader
        leading={
          <span className="flex items-center gap-3">
            <Link
              to="/investigations"
              className="inline-flex items-center gap-1 text-muted hover:text-content"
            >
              <ArrowLeft className="h-3.5 w-3.5" />
              调查队列
            </Link>
            <Link
              to={`/incidents/${inv.incident_id}`}
              className="truncate font-mono text-accent hover:underline"
            >
              {inv.incident_id}
            </Link>
          </span>
        }
        title={
          <span className="flex items-center gap-2">
            <PhaseBadge phase={inv.phase} />
            <span className="truncate font-mono text-sm">
              {inv.investigation_id}
            </span>
          </span>
        }
        subtitle={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span>当前阶段:{phaseLabel(inv.phase)}</span>
            {inv.trigger_reason && <span>触发:{inv.trigger_reason}</span>}
            {inv.started_at && (
              <span title={formatTime(inv.started_at)}>
                开始于 {relativeTime(inv.started_at)}
              </span>
            )}
            <span>
              {inv.ended_at ? '耗时' : '已耗时'} {formatDuration(elapsed)}
            </span>
          </span>
        }
        actions={
          <Button
            variant="ghost"
            size="sm"
            onClick={() => window.print()}
            title="打印 / 导出为 PDF"
          >
            <Printer className="h-4 w-4" />
          </Button>
        }
      />

      <div className="anim-rise space-y-4 px-6 py-5">
        {stalled && (
          <Callout tone="warn" icon={<Timer className="h-4 w-4" />}>
            这次调查已运行超过 10 分钟仍未结束(预算上限{' '}
            {formatDuration(inv.budget?.max_duration_sec)})。
            若时间线也停止追加,通常是 worker 卡住或工具调用挂起 ——
            这类故障不会自行恢复,可以直接取消后重新发起。
          </Callout>
        )}

        {inv.phase === 'needs_human' && (
          <Callout tone="warn">
            Agent 判定需要人工介入:预算耗尽或证据不足以得出结论。
            下方假设与证据仍然有效,可据此继续人工排查。
          </Callout>
        )}

        <div className="print-area grid grid-cols-1 gap-4 lg:grid-cols-3">
          {/* 左:假设 + 诊断 + 工具 */}
          <div className="space-y-4 lg:col-span-2">
            <Card>
              <CardHeader
                icon={<Lightbulb className="h-4 w-4 text-accent" />}
                title="根因假设"
                subtitle="按置信度排序,每条都可展开看支撑证据"
                right={
                  <span className="text-2xs text-faint">
                    {hypotheses.length} 条
                  </span>
                }
              />
              <div className="space-y-2 p-4">
                {hypotheses.length === 0 ? (
                  <EmptyState
                    icon={<Lightbulb className="h-7 w-7" />}
                    title="暂无假设"
                    hint="Agent 进入综合分析阶段后会产出排序假设"
                  />
                ) : (
                  hypotheses.map((h) => (
                    <HypothesisCard
                      key={h.hypothesis_id}
                      hypothesis={h}
                      onOpenEvidence={setEvidenceId}
                    />
                  ))
                )}
              </div>
            </Card>

            {inv.diagnosis && <DiagnosisPanel diagnosis={inv.diagnosis} />}

            <ToolCallsPanel
              toolCalls={toolCalls}
              onOpenEvidence={setEvidenceId}
            />

            {/* 模型与策略版本:结论可复现的前提。
                出了错误诊断要能回答"当时用的哪个模型、哪版 prompt"。 */}
            {(inv.model_version ||
              inv.prompt_version ||
              inv.policy_version) && (
              <div className="flex flex-wrap items-center gap-2 text-2xs text-faint">
                <span>可复现信息:</span>
                {inv.model_version && <Mono>模型 {inv.model_version}</Mono>}
                {inv.prompt_version && <Mono>prompt {inv.prompt_version}</Mono>}
                {inv.policy_version && <Mono>策略 {inv.policy_version}</Mono>}
              </div>
            )}
          </div>

          {/* 右:预算 + 时间线 + 人工控制 */}
          <div className="space-y-4">
            <Card>
              <CardHeader
                icon={<Gauge className="h-4 w-4 text-accent" />}
                title="预算 / 用量"
                subtitle="确定性护栏,由代码强制"
              />
              <BudgetPanel budget={inv.budget} usage={inv.usage} />
            </Card>

            <div className="h-80">
              <Timeline events={events} sseStatus={sseStatus} />
            </div>

            <Card>
              <CardHeader
                icon={<MessageSquare className="h-4 w-4 text-accent" />}
                title="人工判定"
                subtitle="P1/P2 由值班人员最终负责"
              />
              <FeedbackControls
                investigationId={inv.investigation_id}
                phase={inv.phase}
              />
            </Card>

            {inv.feedback && inv.feedback.length > 0 && (
              <FeedbackHistory feedback={inv.feedback} />
            )}
          </div>
        </div>
      </div>

      {evidenceId && (
        <EvidenceModal
          evidenceId={evidenceId}
          onClose={() => setEvidenceId(null)}
        />
      )}
    </>
  )
}
