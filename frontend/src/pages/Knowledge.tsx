import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { BookOpen, Search, ChevronDown, ChevronRight } from 'lucide-react'
import { useKnowledgeSearch } from '@/hooks/queries'
import {
  Card,
  PageHeader,
  Spinner,
  ErrorState,
  EmptyState,
  Button,
  Mono,
  inputCls,
} from '@/components/ui'
import { cn, formatTime, knowledgeKindLabel } from '@/lib/format'
import { HttpError } from '@/api/client'
import type { KnowledgeItem } from '@/api/types'

// 知识库检索页。
//
// 后端 /v1/knowledge 是 Agent 做 RAG 时用的同一个检索入口。
// 把它暴露给人有实际价值:值班人员想确认"Agent 引用的那条 runbook 是否还有效"
// 时,此前只能去数据库看。同一个入口保证人看到的与 Agent 检索到的一致。
export function KnowledgePage() {
  const [params, setParams] = useSearchParams()
  const initial = params.get('q') ?? ''
  const [input, setInput] = useState(initial)
  const [query, setQuery] = useState(initial)

  // URL 变化(命令面板跳进来)时同步到本地状态
  useEffect(() => {
    const q = params.get('q') ?? ''
    setInput(q)
    setQuery(q)
  }, [params])

  const { data, isLoading, error, refetch, isFetching } = useKnowledgeSearch(
    query,
    query.trim().length > 0,
  )

  function submit(e: React.FormEvent) {
    e.preventDefault()
    const q = input.trim()
    setQuery(q)
    const next = new URLSearchParams()
    if (q) next.set('q', q)
    setParams(next, { replace: true })
  }

  return (
    <>
      <PageHeader
        title="知识库"
        subtitle="Runbook / 架构文档 / 历史故障 —— 与 Agent 做 RAG 时用的是同一个检索入口"
        extra={
          <form onSubmit={submit} className="flex items-center gap-2">
            <div className="relative min-w-[240px] flex-1 md:max-w-md">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-faint" />
              <input
                value={input}
                onChange={(e) => setInput(e.target.value)}
                placeholder="检索 runbook / 架构 / 历史故障…"
                className={cn(inputCls, 'pl-8')}
              />
            </div>
            <Button
              type="submit"
              variant="primary"
              size="sm"
              loading={isFetching && query.length > 0}
            >
              检索
            </Button>
          </form>
        }
      />

      <div className="anim-rise px-6 py-5">
        {!query ? (
          <Card>
            <EmptyState
              icon={<BookOpen className="h-7 w-7" />}
              title="输入关键词开始检索"
              hint="例如 checkout、连接池、发布回滚。后端按标题与正文匹配,返回前 10 条。"
            />
          </Card>
        ) : isLoading ? (
          <Card>
            <Spinner label="检索中…" />
          </Card>
        ) : error ? (
          <Card>
            <ErrorState
              message={
                error instanceof HttpError
                  ? `${error.message}(${error.code})`
                  : '检索失败'
              }
              onRetry={() => refetch()}
            />
          </Card>
        ) : !data || data.length === 0 ? (
          <Card>
            <EmptyState
              title={`没有匹配「${query}」的条目`}
              hint="知识库内容由 shared/seed 与运维流程写入,可换个关键词再试"
            />
          </Card>
        ) : (
          <div className="space-y-3">
            <p className="text-2xs text-faint">
              命中 {data.length} 条{data.length >= 10 && '(上限 10 条)'}
            </p>
            {data.map((item) => (
              <KnowledgeCard key={item.knowledge_id} item={item} />
            ))}
          </div>
        )}
      </div>
    </>
  )
}

function KnowledgeCard({ item }: { item: KnowledgeItem }) {
  const [expanded, setExpanded] = useState(false)
  // 失效时间已过的条目要显眼:Agent 会引用它做诊断,而一份过期的
  // runbook 给出的处置步骤可能已经不适用当前架构。
  const expired =
    item.valid_until != null && new Date(item.valid_until).getTime() < Date.now()

  const preview = item.content.slice(0, 320)
  const truncated = item.content.length > preview.length

  return (
    <Card className="p-4">
      <button
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-start justify-between gap-3 text-left"
      >
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-1.5">
            <Mono>{knowledgeKindLabel(item.kind)}</Mono>
            {item.version && (
              <span className="text-2xs text-faint">v{item.version}</span>
            )}
            {expired && (
              <span className="rounded bg-danger/15 px-1.5 py-0.5 text-2xs text-danger">
                已过期
              </span>
            )}
          </div>
          <h3 className="mt-1.5 text-sm font-medium text-content">
            {item.title}
          </h3>
        </div>
        {expanded ? (
          <ChevronDown className="mt-1 h-4 w-4 shrink-0 text-faint" />
        ) : (
          <ChevronRight className="mt-1 h-4 w-4 shrink-0 text-faint" />
        )}
      </button>

      <div className="mt-2 whitespace-pre-wrap break-words text-xs leading-relaxed text-muted">
        {expanded ? item.content : preview}
        {!expanded && truncated && '…'}
      </div>

      <div className="mt-2.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-faint">
        {item.applies_to &&
          Object.entries(item.applies_to).map(([k, v]) => (
            <span key={k}>
              {k}:{String(v)}
            </span>
          ))}
        {item.valid_until && (
          <span className={expired ? 'text-danger' : undefined}>
            有效至 {formatTime(item.valid_until)}
          </span>
        )}
        {item.created_at && <span>创建于 {formatTime(item.created_at)}</span>}
      </div>
    </Card>
  )
}
