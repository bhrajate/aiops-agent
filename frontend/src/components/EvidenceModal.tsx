import type { ReactNode } from 'react'
import { useEvidence } from '@/hooks/queries'
import { EvidenceTypeBadge } from './Badges'
import { Spinner, ErrorState } from './ui'
import { X, ShieldCheck, Clock } from 'lucide-react'
import { HttpError } from '@/api/client'
import { formatTime } from '@/lib/format'

function Field({ label, value }: { label: string; value?: ReactNode }) {
  if (value === undefined || value === null || value === '') return null
  return (
    <div className="grid grid-cols-[5.5rem_1fr] gap-2 py-2 text-sm">
      <span className="text-2xs text-faint">{label}</span>
      <span className="min-w-0 break-words text-content">{value}</span>
    </div>
  )
}

/** 证据详情弹窗:GET /v1/evidence/{evidence_id} */
export function EvidenceModal({
  evidenceId,
  onClose,
}: {
  evidenceId: string
  onClose: () => void
}) {
  const { data, isLoading, error, refetch } = useEvidence(evidenceId)

  return (
    <div
      role="dialog"
      aria-modal
      aria-label="证据详情"
      className="anim-fade fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onClick={onClose}
    >
      <div
        className="anim-scale max-h-[82vh] w-full max-w-2xl overflow-auto rounded-xl border border-line bg-card shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="sticky top-0 flex items-center justify-between border-b border-line-soft bg-card px-4 py-3">
          <h3 className="flex min-w-0 items-center gap-2 text-sm font-semibold text-content">
            证据详情
            <span className="truncate font-mono text-2xs text-faint">
              {evidenceId}
            </span>
          </h3>
          <button
            onClick={onClose}
            aria-label="关闭"
            className="rounded p-1 text-muted hover:bg-card-soft hover:text-content"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="p-4">
          {isLoading && <Spinner label="加载证据…" />}
          {error && (
            <ErrorState
              message={
                error instanceof HttpError
                  ? `${error.message}(${error.code})`
                  : '加载失败'
              }
              onRetry={() => refetch()}
            />
          )}
          {data && (
            <div className="divide-y divide-line-soft">
              <div className="flex flex-wrap items-center gap-2 pb-3">
                <EvidenceTypeBadge type={data.type} />
                <span className="text-xs text-muted">
                  来源 {data.source}
                  {data.tool_name && ` · 工具 ${data.tool_name}`}
                </span>
                {data.redaction_status === 'redacted' && (
                  <span className="inline-flex items-center gap-1 rounded bg-warn/15 px-1.5 py-0.5 text-2xs text-warn">
                    <ShieldCheck className="h-3 w-3" />
                    已脱敏
                  </span>
                )}
              </div>
              <Field label="摘要" value={data.summary} />
              {data.time_range && (
                <Field
                  label="时间范围"
                  value={
                    <span className="inline-flex items-center gap-1.5 font-mono text-xs">
                      <Clock className="h-3 w-3 text-faint" />
                      {formatTime(data.time_range.from)} →{' '}
                      {formatTime(data.time_range.to)}
                    </span>
                  }
                />
              )}
              <Field label="新鲜度" value={data.freshness} />
              {data.query?.expr && (
                <Field
                  label="查询"
                  value={
                    <code className="block overflow-x-auto rounded-lg border border-line-soft bg-bg-soft p-2 font-mono text-2xs text-ok">
                      {String(data.query.expr)}
                    </code>
                  }
                />
              )}
              <Field
                label="采集时间"
                value={
                  data.created_at ? formatTime(data.created_at) : undefined
                }
              />
              <Field
                label="原始引用"
                value={
                  data.raw_ref ? (
                    <span className="break-all font-mono text-2xs text-muted">
                      {data.raw_ref}
                    </span>
                  ) : undefined
                }
              />
              <Field
                label="内容哈希"
                value={
                  data.content_hash ? (
                    <span
                      className="break-all font-mono text-2xs text-faint"
                      title="防篡改哈希:证据一旦冻结不可修改"
                    >
                      {data.content_hash}
                    </span>
                  ) : undefined
                }
              />
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
