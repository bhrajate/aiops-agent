import type { ReactNode } from 'react'
import type {
  IncidentStatus,
  Severity,
  InvestigationPhase,
  HypothesisStatus,
  DiagnosisStatus,
  EvidenceType,
  ReviewStatus,
  AuditResult,
  QueueHealthStatus,
} from '@/api/types'
import { cn, evidenceTypeLabel } from '@/lib/format'
import { isActivePhase as isActive, phaseLabel as label } from '@/lib/phase'

interface PillProps {
  children: ReactNode
  className?: string
  title?: string
}

function Pill({ children, className, title }: PillProps) {
  return (
    <span
      title={title}
      className={cn(
        'inline-flex items-center gap-1 whitespace-nowrap rounded px-1.5 py-0.5 text-2xs font-medium',
        className,
      )}
    >
      {children}
    </span>
  )
}

// 级别用 ring 而非纯底色:值班台一屏可能有几十个 badge,
// 纯底色堆在一起会糊成色块,细边框保留可辨识的轮廓。
const SEVERITY_STYLE: Record<Severity, string> = {
  P1: 'bg-p1/15 text-p1 ring-1 ring-p1/40',
  P2: 'bg-p2/15 text-p2 ring-1 ring-p2/40',
  P3: 'bg-p3/15 text-p3 ring-1 ring-p3/40',
  P4: 'bg-p4/15 text-p4 ring-1 ring-p4/40',
}

export function SeverityBadge({
  severity,
  showDot,
}: {
  severity: Severity
  showDot?: boolean
}) {
  return (
    <Pill
      className={SEVERITY_STYLE[severity] ?? 'bg-card-soft text-muted'}
      title={`严重级别 ${severity}`}
    >
      {showDot && (
        <span
          className="inline-block h-1.5 w-1.5 rounded-full bg-current"
          aria-hidden
        />
      )}
      {severity}
    </Pill>
  )
}

const STATUS_LABEL: Record<IncidentStatus, string> = {
  open: '未认领',
  acknowledged: '处置中',
  resolved: '已解决',
  closed: '已关闭',
}
const STATUS_STYLE: Record<IncidentStatus, string> = {
  // open 刻意用 danger:它的语义是"还没有人接手",这是值班台上
  // 最需要被看见的状态。此前用的"进行中"会让人以为已经有人在处理了。
  open: 'bg-danger/15 text-danger',
  acknowledged: 'bg-warn/15 text-warn',
  resolved: 'bg-ok/15 text-ok',
  closed: 'bg-card-soft text-faint',
}

export function statusLabel(status: IncidentStatus): string {
  return STATUS_LABEL[status] ?? status
}

export function StatusBadge({ status }: { status: IncidentStatus }) {
  return (
    <Pill className={STATUS_STYLE[status] ?? 'bg-card-soft text-muted'}>
      {statusLabel(status)}
    </Pill>
  )
}

const PHASE_STYLE: Record<InvestigationPhase, string> = {
  queued: 'bg-card-soft text-muted',
  triaging: 'bg-accent/15 text-accent',
  triage_published: 'bg-accent/15 text-accent',
  planning: 'bg-info/15 text-info',
  collecting: 'bg-info/15 text-info',
  synthesizing: 'bg-info/15 text-info',
  concluded: 'bg-ok/15 text-ok',
  needs_human: 'bg-warn/15 text-warn',
  waiting_feedback: 'bg-warn/15 text-warn',
  closed: 'bg-card-soft text-faint',
  cancelled: 'bg-card-soft text-faint',
}

// 阶段判定与中文名在 lib/phase.ts —— 那是纯逻辑且须与后端两处一致,
// 单独成模块便于直接测试(见 lib/phase.test.ts)。这里只做转发,
// 保持既有 import 路径可用。
export { isActivePhase, isTerminalPhase, phaseLabel } from '@/lib/phase'

export function PhaseBadge({
  phase,
  live,
}: {
  phase: InvestigationPhase
  // live:进行中的阶段加一个脉动点,让"还在跑"一眼可见。
  live?: boolean
}) {
  const showDot = live ?? isActive(phase)
  return (
    <Pill className={PHASE_STYLE[phase] ?? 'bg-card-soft text-muted'}>
      {showDot && (
        <span
          className="inline-block h-1.5 w-1.5 animate-pulse-dot rounded-full bg-current"
          aria-hidden
        />
      )}
      {label(phase)}
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
  proposed: 'bg-card-soft text-muted',
  supported: 'bg-ok/15 text-ok',
  rejected: 'bg-danger/15 text-danger',
  unresolved: 'bg-warn/15 text-warn',
}

export function HypothesisStatusBadge({
  status,
}: {
  status: HypothesisStatus
}) {
  return (
    <Pill className={HYP_STYLE[status] ?? 'bg-card-soft text-muted'}>
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
  resolved: 'bg-ok/15 text-ok ring-1 ring-ok/40',
  unresolved: 'bg-warn/15 text-warn ring-1 ring-warn/40',
  inconclusive: 'bg-card-soft text-muted ring-1 ring-line',
}

export function diagnosisLabel(status: DiagnosisStatus): string {
  return DIAG_LABEL[status] ?? status
}

export function DiagnosisStatusBadge({
  status,
}: {
  status: DiagnosisStatus
}) {
  return (
    <Pill className={DIAG_STYLE[status] ?? 'bg-card-soft text-muted'}>
      {diagnosisLabel(status)}
    </Pill>
  )
}

export function EvidenceTypeBadge({ type }: { type: EvidenceType }) {
  return (
    <Pill className="bg-card-soft text-muted ring-1 ring-line-soft">
      {evidenceTypeLabel(type)}
    </Pill>
  )
}

const REVIEW_LABEL: Record<ReviewStatus, string> = {
  pending: '待审',
  approved: '已批准',
  rejected: '已驳回',
}
const REVIEW_STYLE: Record<ReviewStatus, string> = {
  pending: 'bg-warn/15 text-warn',
  approved: 'bg-ok/15 text-ok',
  rejected: 'bg-card-soft text-faint',
}

export function ReviewStatusBadge({ status }: { status: ReviewStatus }) {
  return (
    <Pill className={REVIEW_STYLE[status] ?? 'bg-card-soft text-muted'}>
      {REVIEW_LABEL[status] ?? status}
    </Pill>
  )
}

const AUDIT_LABEL: Record<AuditResult, string> = {
  ok: '成功',
  allowed: '允许',
  denied: '已拒绝',
  error: '错误',
}
const AUDIT_STYLE: Record<AuditResult, string> = {
  ok: 'bg-ok/15 text-ok',
  allowed: 'bg-ok/15 text-ok',
  // denied 用 danger:被拒绝的访问是安全信号,混在灰色里就看不见了。
  denied: 'bg-danger/15 text-danger',
  error: 'bg-warn/15 text-warn',
}

export function AuditResultBadge({ result }: { result?: AuditResult }) {
  if (!result) return <span className="text-faint">—</span>
  return (
    <Pill className={AUDIT_STYLE[result] ?? 'bg-card-soft text-muted'}>
      {AUDIT_LABEL[result] ?? result}
    </Pill>
  )
}

const QUEUE_LABEL: Record<QueueHealthStatus, string> = {
  ok: '正常',
  lagging: '滞后',
  stuck: '卡住',
}
const QUEUE_STYLE: Record<QueueHealthStatus, string> = {
  ok: 'bg-ok/15 text-ok',
  lagging: 'bg-warn/15 text-warn',
  stuck: 'bg-danger/15 text-danger',
}

export function QueueHealthBadge({
  health,
}: {
  health: QueueHealthStatus
}) {
  return (
    <Pill
      className={QUEUE_STYLE[health] ?? 'bg-card-soft text-muted'}
      title="投递管道状态:按最老待投递记录的年龄判定,不按条数"
    >
      {QUEUE_LABEL[health] ?? health}
    </Pill>
  )
}

// 角色徽标(侧边栏与用户菜单)
export function RoleBadge({ role }: { role: string }) {
  const style =
    role === 'admin'
      ? 'bg-info/15 text-info'
      : role === 'sre'
        ? 'bg-accent/15 text-accent'
        : role === 'oncall'
          ? 'bg-ok/15 text-ok'
          : 'bg-card-soft text-faint'
  return <Pill className={cn('uppercase', style)}>{role}</Pill>
}
