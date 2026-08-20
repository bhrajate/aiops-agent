import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  Search,
  LayoutDashboard,
  Siren,
  ScrollText,
  BookOpen,
  FlaskConical,
  Settings,
  Sun,
  CornerDownLeft,
  Hash,
} from 'lucide-react'
import { cn } from '@/lib/format'
import { useAuth } from '@/auth/context'
import { useTheme } from '@/store/theme'

interface Command {
  id: string
  label: string
  hint?: string
  icon: typeof Search
  run: () => void
  keywords?: string
}

// 命令面板:⌘K / Ctrl+K。
//
// 只做两件事:路由跳转与直接按 ID 打开 incident/调查。
// 后者是值班场景的真实需求 —— 告警群里贴的是 inc-xxx,
// 此前只能手拼 URL 或回列表里翻。
export function CommandPalette({
  open,
  onClose,
}: {
  open: boolean
  onClose: () => void
}) {
  const navigate = useNavigate()
  const { canReadAudit, canReviewGolden } = useAuth()
  const { cycle: cycleTheme } = useTheme()
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const inputRef = useRef<HTMLInputElement | null>(null)

  useEffect(() => {
    if (open) {
      setQuery('')
      setActive(0)
      // 等 DOM 挂载后聚焦
      const t = setTimeout(() => inputRef.current?.focus(), 0)
      return () => clearTimeout(t)
    }
  }, [open])

  const commands = useMemo<Command[]>(() => {
    const go = (to: string) => () => {
      navigate(to)
      onClose()
    }
    const list: Command[] = [
      {
        id: 'overview',
        label: '值班总览',
        hint: '首页',
        icon: LayoutDashboard,
        run: go('/'),
        keywords: 'overview dashboard zonglan shouye',
      },
      {
        id: 'incidents',
        label: '告警列表',
        hint: 'Incident',
        icon: Siren,
        run: go('/incidents'),
        keywords: 'incidents alerts gaojing',
      },
      {
        id: 'incidents-open',
        label: '仅看未认领告警',
        hint: 'status=open',
        icon: Siren,
        run: go('/incidents?status=open'),
        keywords: 'open unack weirenling',
      },
      {
        id: 'incidents-p1',
        label: '仅看 P1 告警',
        hint: 'severity=P1',
        icon: Siren,
        run: go('/incidents?severity=P1'),
        keywords: 'p1 critical',
      },
      {
        id: 'investigations',
        label: '调查队列',
        hint: 'Investigation',
        icon: Search,
        run: go('/investigations'),
        keywords: 'investigations diaocha',
      },
      {
        id: 'investigations-active',
        label: '仅看进行中的调查',
        hint: 'active=true',
        icon: Search,
        run: go('/investigations?active=true'),
        keywords: 'active running jinxingzhong',
      },
      {
        id: 'knowledge',
        label: '知识库检索',
        hint: 'Runbook / 架构',
        icon: BookOpen,
        run: go('/knowledge'),
        keywords: 'knowledge runbook zhishiku',
      },
      {
        id: 'settings',
        label: '设置',
        icon: Settings,
        run: go('/settings'),
        keywords: 'settings shezhi',
      },
      {
        id: 'theme',
        label: '切换主题',
        hint: '跟随系统 / 深色 / 浅色',
        icon: Sun,
        run: () => {
          cycleTheme()
          onClose()
        },
        keywords: 'theme dark light zhuti',
      },
    ]
    if (canReviewGolden) {
      list.splice(6, 0, {
        id: 'golden',
        label: '评测用例待审队列',
        hint: 'Golden Case',
        icon: FlaskConical,
        run: go('/golden-cases'),
        keywords: 'golden cases pingce daishen',
      })
    }
    if (canReadAudit) {
      list.push({
        id: 'audit',
        label: '审计日志',
        icon: ScrollText,
        run: go('/audit'),
        keywords: 'audit log shenji',
      })
      list.push({
        id: 'audit-denied',
        label: '仅看被拒绝的访问',
        hint: 'result=denied',
        icon: ScrollText,
        run: go('/audit?result=denied'),
        keywords: 'denied forbidden jujue',
      })
    }
    return list
  }, [navigate, onClose, canReadAudit, canReviewGolden, cycleTheme])

  // 直接按 ID 跳转。前缀识别 incident / investigation ——
  // 值班时从告警群拷一个 ID 过来就能开,不用先找到它在列表第几页。
  const idCommands = useMemo<Command[]>(() => {
    const q = query.trim()
    if (!q) return []
    const out: Command[] = []
    if (/^inc[-_]?/i.test(q) || /^incident/i.test(q)) {
      out.push({
        id: `open-incident-${q}`,
        label: `打开 Incident ${q}`,
        hint: '按 ID 直达',
        icon: Hash,
        run: () => {
          navigate(`/incidents/${encodeURIComponent(q)}`)
          onClose()
        },
      })
    }
    if (/^inv[-_]?/i.test(q) || /^investigation/i.test(q)) {
      out.push({
        id: `open-inv-${q}`,
        label: `打开调查 ${q}`,
        hint: '按 ID 直达',
        icon: Hash,
        run: () => {
          navigate(`/investigations/${encodeURIComponent(q)}`)
          onClose()
        },
      })
    }
    // 知识库全文检索兜底:输入的既不是 ID 也匹配不上路由时,
    // 至少给一个"拿这段话去搜知识库"的出口。
    if (q.length >= 2) {
      out.push({
        id: `search-knowledge-${q}`,
        label: `在知识库中搜索“${q}”`,
        icon: BookOpen,
        run: () => {
          navigate(`/knowledge?q=${encodeURIComponent(q)}`)
          onClose()
        },
      })
    }
    return out
  }, [query, navigate, onClose])

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    const routes = q
      ? commands.filter(
          (c) =>
            c.label.toLowerCase().includes(q) ||
            c.hint?.toLowerCase().includes(q) ||
            c.keywords?.toLowerCase().includes(q),
        )
      : commands
    return [...idCommands, ...routes]
  }, [commands, idCommands, query])

  // 过滤结果变化时把高亮收回首项,否则索引可能指向已消失的条目。
  useEffect(() => {
    setActive(0)
  }, [query])

  if (!open) return null

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive((i) => (filtered.length ? (i + 1) % filtered.length : 0))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((i) =>
        filtered.length ? (i - 1 + filtered.length) % filtered.length : 0,
      )
    } else if (e.key === 'Enter') {
      e.preventDefault()
      filtered[active]?.run()
    } else if (e.key === 'Escape') {
      e.preventDefault()
      onClose()
    }
  }

  return (
    <div
      role="dialog"
      aria-modal
      aria-label="命令面板"
      className="anim-fade fixed inset-0 z-50 flex items-start justify-center bg-black/60 p-4 pt-[12vh]"
      onClick={onClose}
    >
      <div
        className="anim-scale w-full max-w-lg overflow-hidden rounded-xl border border-line bg-card shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 border-b border-line-soft px-3 py-2.5">
          <Search className="h-4 w-4 shrink-0 text-faint" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder="跳转页面,或粘贴 inc-/inv- ID 直达"
            className="w-full bg-transparent text-sm text-content outline-none placeholder:text-faint"
          />
          <kbd className="shrink-0 rounded bg-card-soft px-1.5 py-0.5 text-2xs text-faint">
            ESC
          </kbd>
        </div>
        <ul className="max-h-80 overflow-y-auto p-1.5">
          {filtered.length === 0 ? (
            <li className="px-2.5 py-6 text-center text-xs text-faint">
              没有匹配项
            </li>
          ) : (
            filtered.map((c, i) => (
              <li key={c.id}>
                <button
                  type="button"
                  onMouseEnter={() => setActive(i)}
                  onClick={c.run}
                  className={cn(
                    'flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-xs transition-colors',
                    i === active
                      ? 'bg-card-soft text-content'
                      : 'text-muted hover:bg-card-soft',
                  )}
                >
                  <c.icon className="h-4 w-4 shrink-0 text-faint" />
                  <span className="flex-1 truncate">{c.label}</span>
                  {c.hint && (
                    <span className="shrink-0 text-2xs text-faint">
                      {c.hint}
                    </span>
                  )}
                  {i === active && (
                    <CornerDownLeft className="h-3.5 w-3.5 shrink-0 text-faint" />
                  )}
                </button>
              </li>
            ))
          )}
        </ul>
      </div>
    </div>
  )
}
