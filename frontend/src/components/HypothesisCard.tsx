import { useState } from 'react'
import type { Hypothesis } from '@/api/types'
import { HypothesisStatusBadge } from './Badges'
import { ProgressBar } from './ui'
import { cn, pct, formatResource } from '@/lib/format'
import {
  ChevronDown,
  ChevronRight,
  ThumbsUp,
  ThumbsDown,
  HelpCircle,
} from 'lucide-react'

function confidenceTone(c: number): 'ok' | 'warn' | 'danger' {
  if (c >= 0.66) return 'ok'
  if (c >= 0.33) return 'warn'
  return 'danger'
}

interface Props {
  hypothesis: Hypothesis
  onOpenEvidence: (id: string) => void
}

export function HypothesisCard({ hypothesis: h, onOpenEvidence }: Props) {
  // 默认只展开排名第一的:值班时先看最可能的那条,其余按需展开。
  const [expanded, setExpanded] = useState(h.rank === 1)

  const supporting = h.supporting_evidence_ids ?? []
  const contradicting = h.contradicting_evidence_ids ?? []

  return (
    <div className="rounded-lg border border-line-soft bg-bg-soft">
      <button
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
        className="flex w-full items-start gap-3 px-4 py-3 text-left"
      >
        <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded bg-card-soft text-2xs font-bold text-muted">
          #{h.rank}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-start justify-between gap-2">
            <p className="text-sm font-medium text-content">{h.statement}</p>
            <div className="flex shrink-0 items-center gap-2">
              <HypothesisStatusBadge status={h.status} />
              {expanded ? (
                <ChevronDown className="h-4 w-4 text-faint" />
              ) : (
                <ChevronRight className="h-4 w-4 text-faint" />
              )}
            </div>
          </div>
          {h.component_ref && (
            <p className="mt-0.5 font-mono text-2xs text-faint">
              {formatResource(h.component_ref)}
              {h.component_ref.namespace
                ? ` · ${h.component_ref.namespace}`
                : ''}
            </p>
          )}
          <div className="mt-2 flex items-center gap-3">
            <div className="flex-1">
              <ProgressBar
                value={h.confidence}
                max={1}
                tone={confidenceTone(h.confidence)}
              />
            </div>
            <span className="tabular w-10 shrink-0 text-right font-mono text-2xs text-muted">
              {pct(h.confidence)}
            </span>
          </div>
          {/* 收起时也显示证据计数:0 条支持证据的高置信度假设是危险信号,
              不该需要展开才能发现。 */}
          {!expanded && (
            <div className="mt-1.5 flex items-center gap-3 text-2xs text-faint">
              <span
                className={cn(
                  'inline-flex items-center gap-1',
                  supporting.length === 0 && 'text-warn',
                )}
              >
                <ThumbsUp className="h-3 w-3" />
                {supporting.length}
              </span>
              <span className="inline-flex items-center gap-1">
                <ThumbsDown className="h-3 w-3" />
                {contradicting.length}
              </span>
            </div>
          )}
        </div>
      </button>

      {expanded && (
        <div className="space-y-3 border-t border-line-soft px-4 py-3">
          <EvidenceRow
            icon={<ThumbsUp className="h-3.5 w-3.5 text-ok" />}
            label="支持证据"
            ids={supporting}
            onOpen={onOpenEvidence}
            tone="ok"
            emptyWarn="没有任何支持证据 —— 该假设仅为推测"
          />
          <EvidenceRow
            icon={<ThumbsDown className="h-3.5 w-3.5 text-danger" />}
            label="反对证据"
            ids={contradicting}
            onOpen={onOpenEvidence}
            tone="danger"
          />
          {h.missing_evidence && h.missing_evidence.length > 0 && (
            <div>
              <div className="mb-1 flex items-center gap-1.5 text-xs text-muted">
                <HelpCircle className="h-3.5 w-3.5 text-warn" />
                缺失信息
              </div>
              <ul className="ml-5 list-disc space-y-0.5 text-xs text-muted">
                {h.missing_evidence.map((m, i) => (
                  <li key={i}>{m}</li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function EvidenceRow({
  icon,
  label,
  ids,
  onOpen,
  tone,
  emptyWarn,
}: {
  icon: React.ReactNode
  label: string
  ids: string[]
  onOpen: (id: string) => void
  tone: 'ok' | 'danger'
  emptyWarn?: string
}) {
  return (
    <div>
      <div className="mb-1 flex items-center gap-1.5 text-xs text-muted">
        {icon}
        {label}
        <span className="text-faint">({ids?.length ?? 0})</span>
      </div>
      {ids && ids.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {ids.map((id) => (
            <button
              key={id}
              onClick={() => onOpen(id)}
              title="点击查看证据详情"
              className={cn(
                'rounded px-2 py-0.5 font-mono text-2xs ring-1 transition-colors',
                tone === 'ok'
                  ? 'bg-ok/10 text-ok ring-ok/30 hover:bg-ok/20'
                  : 'bg-danger/10 text-danger ring-danger/30 hover:bg-danger/20',
              )}
            >
              {id}
            </button>
          ))}
        </div>
      ) : (
        <span className={cn('text-2xs', emptyWarn ? 'text-warn' : 'text-faint')}>
          {emptyWarn ?? '无'}
        </span>
      )}
    </div>
  )
}
