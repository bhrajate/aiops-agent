import { useEvidence } from '@/hooks/queries'
import { EvidenceTypeBadge } from './Badges'
import { Spinner, ErrorState } from './ui'
import { X } from 'lucide-react'
import { HttpError } from '@/api/client'

function Field({ label, value }: { label: string; value?: React.ReactNode }) {
  if (value === undefined || value === null || value === '') return null
  return (
    <div className="grid grid-cols-[6rem_1fr] gap-2 py-1.5 text-sm">
      <span className="text-slate-500">{label}</span>
      <span className="break-words text-slate-200">{value}</span>
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
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onClick={onClose}
    >
      <div
        className="max-h-[80vh] w-full max-w-2xl overflow-auto rounded-lg border border-surface-600 bg-surface-850 shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-surface-700 px-4 py-3">
          <h3 className="flex items-center gap-2 text-sm font-semibold text-slate-100">
            证据详情
            <span className="font-mono text-xs text-slate-500">
              {evidenceId}
            </span>
          </h3>
          <button
            onClick={onClose}
            className="rounded p-1 text-slate-400 hover:bg-surface-700 hover:text-slate-200"
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
            <div className="divide-y divide-surface-800">
              <div className="flex items-center gap-2 pb-2">
                <EvidenceTypeBadge type={data.type} />
                <span className="text-sm text-slate-400">
                  来源 {data.source} · 工具 {data.tool_name}
                </span>
              </div>
              <Field label="摘要" value={data.summary} />
              <Field
                label="新鲜度"
                value={data.freshness ? `${data.freshness}` : undefined}
              />
              <Field
                label="脱敏"
                value={
                  data.redaction_status === 'redacted'
                    ? '已脱敏'
                    : data.redaction_status === 'clean'
                      ? '无敏感数据'
                      : undefined
                }
              />
              {data.time_range && (
                <Field
                  label="时间范围"
                  value={`${data.time_range.from} → ${data.time_range.to}`}
                />
              )}
              {data.query?.expr && (
                <Field
                  label="查询"
                  value={
                    <code className="block rounded bg-surface-900 p-2 font-mono text-xs text-emerald-300">
                      {String(data.query.expr)}
                    </code>
                  }
                />
              )}
              <Field
                label="原始引用"
                value={
                  data.raw_ref ? (
                    <span className="font-mono text-xs text-slate-400">
                      {data.raw_ref}
                    </span>
                  ) : undefined
                }
              />
              <Field
                label="内容哈希"
                value={
                  data.content_hash ? (
                    <span className="font-mono text-xs text-slate-500">
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
