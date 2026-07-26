import type {
  IncidentStatus,
  Severity,
  InvestigationPhase,
  HypothesisStatus,
  DiagnosisStatus,
  EvidenceType,
} from '@/api/types'
import { cn } from '@/lib/format'

interface PillProps {
  children: React.ReactNode
  className?: string
  title?: string
}

function Pill({ children, className, title }: PillProps) {
  return (
    <span
      title={title}
      className={cn(
        'inline-flex items-center gap-1 rounded px-2 py-0.5 text-xs font-medium whitespace-nowrap',
        className,
      )}
    >
      {children}
    </span>
  )
}

const SEVERITY_STYLE: Record<Severity, string> = {
  P1: 'bg-red-500/15 text-red-300 ring-1 ring-red-500/40',
  P2: 'bg-orange-500/15 text-orange-300 ring-1 ring-orange-500/40',
  P3: 'bg-amber-500/15 text-amber-300 ring-1 ring-amber-500/40',
  P4: 'bg-sky-500/15 text-sky-300 ring-1 ring-sky-500/40',
}

export function SeverityBadge({ severity }: { severity: Severity }) {
  return (
    <Pill className={SEVERITY_STYLE[severity] ?? 'bg-surface-700 text-slate-300'}>
      {severity}
    </Pill>
  )
}

const STATUS_LABEL: Record<IncidentStatus, string> = {
  open: '进行中',
  acknowledged: '已认领',
  resolved: '已解决',
  closed: '已关闭',
}
const STATUS_STYLE: Record<IncidentStatus, string> = {
  open: 'bg-red-500/15 text-red-300',
  acknowledged: 'bg-yellow-500/15 text-yellow-300',
  resolved: 'bg-emerald-500/15 text-emerald-300',
  closed: 'bg-slate-500/15 text-slate-400',
}

export function StatusBadge({ status }: { status: IncidentStatus }) {
  return (
    <Pill className={STATUS_STYLE[status] ?? 'bg-surface-700 text-slate-300'}>
      {STATUS_LABEL[status] ?? status}
    </Pill>
  )
}

const PHASE_LABEL: Record<InvestigationPhase, string> = {
  queued: '排队中',
  triaging: '分诊中',
  triage_published: '分诊已发布',
  planning: '规划中',
  collecting: '证据采集中',
  synthesizing: '综合分析中',
  concluded: '已得出结论',
  needs_human: '需人工介入',
  waiting_feedback: '等待反馈',
  closed: '已关闭',
  cancelled: '已取消',
}

const PHASE_STYLE: Record<InvestigationPhase, string> = {
  queued: 'bg-slate-500/15 text-slate-300',
  triaging: 'bg-sky-500/15 text-sky-300',
  triage_published: 'bg-sky-500/15 text-sky-300',
  planning: 'bg-indigo-500/15 text-indigo-300',
  collecting: 'bg-blue-500/15 text-blue-300',
  synthesizing: 'bg-violet-500/15 text-violet-300',
  concluded: 'bg-emerald-500/15 text-emerald-300',
  needs_human: 'bg-amber-500/15 text-amber-300',
  waiting_feedback: 'bg-amber-500/15 text-amber-300',
  closed: 'bg-slate-500/15 text-slate-400',
  cancelled: 'bg-slate-600/20 text-slate-400',
}

export function phaseLabel(phase: InvestigationPhase): string {
  return PHASE_LABEL[phase] ?? phase
}

export function PhaseBadge({ phase }: { phase: InvestigationPhase }) {
  return (
    <Pill className={PHASE_STYLE[phase] ?? 'bg-surface-700 text-slate-300'}>
      {phaseLabel(phase)}
    </Pill>
  )
}

const HYP_LABEL: Record<HypothesisStatus, string> = {
  proposed: '待验证',
  supported: '有支持',
  rejected: '已排除',
  unresolved: '未决',
}
const HYP_STYLE: Record<HypothesisStatus, string> = {
  proposed: 'bg-slate-500/15 text-slate-300',
  supported: 'bg-emerald-500/15 text-emerald-300',
  rejected: 'bg-red-500/15 text-red-300',
  unresolved: 'bg-amber-500/15 text-amber-300',
}

export function HypothesisStatusBadge({
  status,
}: {
  status: HypothesisStatus
}) {
  return (
    <Pill className={HYP_STYLE[status] ?? 'bg-surface-700 text-slate-300'}>
      {HYP_LABEL[status] ?? status}
    </Pill>
  )
}

const DIAG_LABEL: Record<DiagnosisStatus, string> = {
  resolved: '已定位',
  unresolved: '未定位',
  inconclusive: '不确定',
}
const DIAG_STYLE: Record<DiagnosisStatus, string> = {
  resolved: 'bg-emerald-500/15 text-emerald-300 ring-1 ring-emerald-500/40',
  unresolved: 'bg-amber-500/15 text-amber-300 ring-1 ring-amber-500/40',
  inconclusive: 'bg-slate-500/15 text-slate-300 ring-1 ring-slate-500/40',
}

export function DiagnosisStatusBadge({
  status,
}: {
  status: DiagnosisStatus
}) {
  return (
    <Pill className={DIAG_STYLE[status] ?? 'bg-surface-700 text-slate-300'}>
      {DIAG_LABEL[status] ?? status}
    </Pill>
  )
}

const EVIDENCE_LABEL: Record<EvidenceType, string> = {
  metric: '指标',
  log: '日志',
  trace: '链路',
  kubernetes: 'K8s',
  change: '变更',
  knowledge: '知识',
}

export function EvidenceTypeBadge({ type }: { type: EvidenceType }) {
  return (
    <Pill className="bg-surface-700 text-slate-300">
      {EVIDENCE_LABEL[type] ?? type}
    </Pill>
  )
}
