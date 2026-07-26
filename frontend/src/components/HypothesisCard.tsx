import { useState } from 'react'
import type { Hypothesis } from '@/api/types'
import { HypothesisStatusBadge } from './Badges'
import { ProgressBar } from './ui'
import { cn, pct } from '@/lib/format'
import { ChevronDown, ChevronRight, ThumbsUp, ThumbsDown, HelpCircle } from 'lucide-react'

function confidenceTone(c: number): 'ok' | 'warn' | 'danger' | 'accent' {
  if (c >= 0.66) return 'ok'
  if (c >= 0.33) return 'warn'
  return 'danger'
}

interface Props {
  hypothesis: Hypothesis
  onOpenEvidence: (id: string) => void
}

export function HypothesisCard({ hypothesis: h, onOpenEvidence }: Props) {
  const [expanded, setExpanded] = useState(h.rank === 1)

  return (
    <div className="rounded-lg border border-surface-700 bg-surface-800">
      <button
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-start gap-3 px-4 py-3 text-left"
      >
        <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded bg-surface-700 text-xs font-bold text-slate-300">
          #{h.rank}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-start justify-between gap-2">
            <p className="text-sm font-medium text-slate-100">{h.statement}</p>
            <div className="flex shrink-0 items-center gap-2">
              <HypothesisStatusBadge status={h.status} />
              {expanded ? (
                <ChevronDown className="h-4 w-4 text-slate-500" />
              ) : (
                <ChevronRight className="h-4 w-4 text-slate-500" />
              )}
            </div>
          </div>
          {h.component_ref && (
            <p className="mt-0.5 font-mono text-xs text-slate-500">
              {h.component_ref.kind}/{h.component_ref.name}
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
            <span className="w-10 shrink-0 text-right font-mono text-xs text-slate-300">
              {pct(h.confidence)}
            </span>
          </div>
        </div>
      </button>

      {expanded && (
        <div className="space-y-3 border-t border-surface-700 px-4 py-3">
          <EvidenceRow
            icon={<ThumbsUp className="h-3.5 w-3.5 text-emerald-400" />}
            label="支持证据"
            ids={h.supporting_evidence_ids}
            onOpen={onOpenEvidence}
            tone="ok"
          />
          <EvidenceRow
            icon={<ThumbsDown className="h-3.5 w-3.5 text-red-400" />}
            label="反对证据"
            ids={h.contradicting_evidence_ids}
            onOpen={onOpenEvidence}
            tone="danger"
          />
          {h.missing_evidence && h.missing_evidence.length > 0 && (
            <div>
              <div className="mb-1 flex items-center gap-1.5 text-xs text-slate-400">
                <HelpCircle className="h-3.5 w-3.5 text-amber-400" />
                缺失信息
              </div>
              <ul className="ml-5 list-disc space-y-0.5 text-xs text-slate-400">
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
}: {
  icon: React.ReactNode
  label: string
  ids: string[]
  onOpen: (id: string) => void
  tone: 'ok' | 'danger'
}) {
  return (
    <div>
      <div className="mb-1 flex items-center gap-1.5 text-xs text-slate-400">
        {icon}
        {label}
        <span className="text-slate-600">({ids?.length ?? 0})</span>
      </div>
      {ids && ids.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {ids.map((id) => (
            <button
              key={id}
              onClick={() => onOpen(id)}
              className={cn(
                'rounded px-2 py-0.5 font-mono text-xs ring-1 transition-colors',
                tone === 'ok'
                  ? 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30 hover:bg-emerald-500/20'
                  : 'bg-red-500/10 text-red-300 ring-red-500/30 hover:bg-red-500/20',
              )}
            >
              {id}
            </button>
          ))}
        </div>
      ) : (
        <span className="text-xs text-slate-600">无</span>
      )}
    </div>
  )
}
