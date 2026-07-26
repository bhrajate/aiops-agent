import { useState, useMemo } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useInvestigation } from '@/hooks/queries'
import { useSSE } from '@/hooks/useSSE'
import { PhaseBadge, phaseLabel } from '@/components/Badges'
import { Card, CardHeader, Spinner, ErrorState, EmptyState } from '@/components/ui'
import { BudgetPanel } from '@/components/BudgetPanel'
import { HypothesisCard } from '@/components/HypothesisCard'
import { DiagnosisPanel } from '@/components/DiagnosisPanel'
import { Timeline } from '@/components/Timeline'
import { FeedbackControls } from '@/components/FeedbackControls'
import { FeedbackHistory } from '@/components/FeedbackHistory'
import { ToolCallsPanel } from '@/components/ToolCallsPanel'
import { EvidenceModal } from '@/components/EvidenceModal'
import { HttpError } from '@/api/client'
import type { InvestigationPhase } from '@/api/types'
import { ArrowLeft, Gauge, Lightbulb, MessageSquare } from 'lucide-react'

const TERMINAL: InvestigationPhase[] = ['closed', 'cancelled', 'concluded']

export function InvestigationViewPage() {
  const { investigationId } = useParams<{ investigationId: string }>()
  const [evidenceId, setEvidenceId] = useState<string | null>(null)

  const { data: inv, isLoading, error, refetch } =
    useInvestigation(investigationId, true)

  const live = inv ? !TERMINAL.includes(inv.phase) : true
  // 仅在非终态订阅 SSE
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
      tool_name: ev.tool_name,
      status: ev.redaction_status,
      scope: ev.query?.scope as Record<string, unknown> | undefined,
      evidence_id: ev.evidence_id,
      started_at: ev.created_at,
      finished_at: ev.created_at,
    }))
  }, [inv?.tool_calls, inv?.evidence])

  if (isLoading) return <Spinner label="加载调查…" />
  if (error || !inv)
    return (
      <div className="mx-auto max-w-6xl px-6 py-6">
        <ErrorState
          message={
            error instanceof HttpError
              ? `${error.message}(${error.code})`
              : '加载失败'
          }
          onRetry={() => refetch()}
        />
      </div>
    )

  return (
    <div className="mx-auto max-w-7xl px-6 py-6">
      <div className="mb-4 flex items-center justify-between">
        <div>
          <Link
            to={`/incidents/${inv.incident_id}`}
            className="inline-flex items-center gap-1 text-xs text-slate-400 hover:text-slate-200"
          >
            <ArrowLeft className="h-3.5 w-3.5" />
            返回 Incident {inv.incident_id}
          </Link>
          <h1 className="mt-1 font-mono text-lg text-slate-100">
            {inv.investigation_id}
          </h1>
        </div>
        <div className="text-right">
          <PhaseBadge phase={inv.phase} />
          <p className="mt-1 text-xs text-slate-500">
            当前阶段:{phaseLabel(inv.phase)}
          </p>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        {/* 左:假设 + 诊断 */}
        <div className="space-y-4 lg:col-span-2">
          <Card>
            <CardHeader
              icon={<Lightbulb className="h-4 w-4 text-accent" />}
              title="Top-N 假设"
              right={
                <span className="text-xs text-slate-500">
                  {hypotheses.length} 条
                </span>
              }
            />
            <div className="space-y-2 p-4">
              {hypotheses.length === 0 ? (
                <EmptyState
                  title="暂无假设"
                  hint="Agent 进入综合分析阶段后将产出假设"
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
          {inv.trigger_reason && (
            <p className="text-xs text-slate-500">
              触发原因:
              <span className="font-mono text-slate-400">
                {inv.trigger_reason}
              </span>
            </p>
          )}
        </div>

        {/* 右:预算 + 时间线 + 人工控制 */}
        <div className="space-y-4">
          <Card>
            <CardHeader
              icon={<Gauge className="h-4 w-4 text-accent" />}
              title="预算 / 用量"
            />
            <BudgetPanel budget={inv.budget} usage={inv.usage} />
          </Card>

          <div className="h-80">
            <Timeline events={events} sseStatus={sseStatus} />
          </div>

          <Card>
            <CardHeader
              icon={<MessageSquare className="h-4 w-4 text-accent" />}
              title="人工控制"
            />
            <FeedbackControls
              investigationId={inv.investigation_id}
              phase={inv.phase}
            />
          </Card>

          {inv.feedback && <FeedbackHistory feedback={inv.feedback} />}
        </div>
      </div>

      {evidenceId && (
        <EvidenceModal
          evidenceId={evidenceId}
          onClose={() => setEvidenceId(null)}
        />
      )}
    </div>
  )
}
