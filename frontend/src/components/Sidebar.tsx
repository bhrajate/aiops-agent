import { useEffect, useRef, useState } from 'react'
import { NavLink, Link, useNavigate, useLocation } from 'react-router-dom'
import {
  LayoutDashboard,
  Siren,
  Search,
  ScrollText,
  BookOpen,
  FlaskConical,
  Settings,
  LogOut,
  PanelLeftClose,
  PanelLeftOpen,
  Sun,
  Moon,
  Monitor,
  ChevronRight,
  Activity,
} from 'lucide-react'
import { cn } from '@/lib/format'
import { useAuth } from '@/auth/context'
import { useUi } from '@/store/ui'
import { useTheme } from '@/store/theme'
import { RoleBadge } from './Badges'
import { useOverview } from '@/hooks/queries'

type IconType = typeof LayoutDashboard

interface NavItem {
  to: string
  icon: IconType
  label: string
  exact?: boolean
  // badge 取值为 undefined 时不渲染;0 也不渲染(见 SidebarNavItem)
  badge?: number
  // 需要的权限。无权限时整个入口隐藏 ——
  // 让用户点进去再看 403 是更差的体验,后端仍有兜底。
  requires?: 'audit' | 'review'
}

// 侧边栏分组。值班动线在前(总览→告警→调查),质量闭环居中,系统在后。
function useNavSections(): Array<{ title: string; items: NavItem[] }> {
  const { data: ov } = useOverview(24)
  return [
    {
      title: '值班',
      items: [
        { to: '/', icon: LayoutDashboard, label: '总览', exact: true },
        {
          to: '/incidents',
          icon: Siren,
          label: '告警',
          // 未认领数是唯一需要抢眼的计数:它等于"还没有人接手"。
          badge: ov?.unacknowledged,
        },
        {
          to: '/investigations',
          icon: Search,
          label: '调查',
          badge: ov?.active_investigations,
        },
      ],
    },
    {
      title: '质量闭环',
      items: [
        {
          to: '/golden-cases',
          icon: FlaskConical,
          label: '评测集',
          badge: ov?.golden_pending,
          requires: 'review',
        },
        { to: '/knowledge', icon: BookOpen, label: '知识库' },
      ],
    },
    {
      title: '系统',
      items: [
        {
          to: '/audit',
          icon: ScrollText,
          label: '审计日志',
          requires: 'audit',
        },
        { to: '/settings', icon: Settings, label: '设置' },
      ],
    },
  ]
}

export function Sidebar() {
  const { sidebarCollapsed, toggleSidebar, setPaletteOpen } = useUi()
  const { user, logout, canReadAudit, canReviewGolden } = useAuth()
  const navigate = useNavigate()
  const sections = useNavSections()
  const [menuOpen, setMenuOpen] = useState(false)
  const menuRef = useRef<HTMLDivElement | null>(null)
  const location = useLocation()

  // 路由切换时关掉用户菜单,否则它会浮在新页面上。
  useEffect(() => {
    setMenuOpen(false)
  }, [location.pathname])

  useEffect(() => {
    if (!menuOpen) return
    function onDocClick(e: MouseEvent) {
      if (!menuRef.current?.contains(e.target as Node)) setMenuOpen(false)
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setMenuOpen(false)
    }
    document.addEventListener('mousedown', onDocClick)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDocClick)
      document.removeEventListener('keydown', onKey)
    }
  }, [menuOpen])

  function handleLogout() {
    setMenuOpen(false)
    logout()
    navigate('/login', { replace: true })
  }

  function allowed(item: NavItem): boolean {
    if (item.requires === 'audit') return canReadAudit
    if (item.requires === 'review') return canReviewGolden
    return true
  }

  if (sidebarCollapsed) {
    return (
      <aside className="flex h-full w-14 shrink-0 flex-col items-center gap-1 overflow-hidden border-r border-line-soft bg-card py-3">
        {/* 品牌标记的可及名用"AIOps 值班台"而非"展开侧边栏":它与下面那个
            折叠按钮功能相同,但若两者共用同一个 aria-label,读屏用户会连续听到
            两个同名控件而无法区分是哪个。展开的意图放在 title 里。
            (e2e/workbench.spec.ts 断言折叠态下"展开侧边栏"只匹配一个控件) */}
        <button
          type="button"
          onClick={toggleSidebar}
          aria-label="AIOps 值班台"
          title="AIOps 值班台 · 点击展开"
          className="rounded-lg p-1.5 text-accent hover:bg-card-soft"
        >
          <Activity className="h-5 w-5" />
        </button>
        <button
          type="button"
          onClick={toggleSidebar}
          aria-label="展开侧边栏"
          className="rounded-lg p-2 text-muted hover:bg-card-soft hover:text-content"
        >
          <PanelLeftOpen className="h-4 w-4" />
        </button>
        <div className="mt-1 flex flex-col items-center gap-1">
          {sections.flatMap((s) =>
            s.items.filter(allowed).map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.exact}
                aria-label={item.label}
                title={item.label}
                className={({ isActive }) =>
                  cn(
                    'relative rounded-lg p-2 text-muted hover:bg-card-soft hover:text-content',
                    isActive && 'bg-card-soft text-content',
                  )
                }
              >
                <item.icon className="h-4 w-4" />
                {item.badge != null && item.badge > 0 && (
                  <span
                    className="absolute right-1 top-1 inline-flex h-2 w-2 rounded-full bg-danger ring-2 ring-card"
                    aria-hidden
                  />
                )}
              </NavLink>
            )),
          )}
        </div>
        <div className="mt-auto">
          <ThemeToggleIcon />
        </div>
      </aside>
    )
  }

  return (
    <aside className="flex h-full w-60 shrink-0 flex-col overflow-hidden border-r border-line-soft bg-card">
      {/* 品牌行 */}
      <div className="flex items-center gap-2 border-b border-line-soft px-3 py-3">
        <Link
          to="/"
          className="-ml-1 flex min-w-0 flex-1 items-center gap-2 rounded-lg px-1 py-1 hover:bg-card-soft"
        >
          <Activity className="h-5 w-5 shrink-0 text-accent" />
          <div className="min-w-0">
            <div className="truncate text-sm font-semibold text-content">
              AIOps 值班台
            </div>
            <div className="truncate text-2xs text-faint">
              Incident Workbench
            </div>
          </div>
        </Link>
        <button
          type="button"
          onClick={toggleSidebar}
          aria-label="折叠侧边栏"
          className="rounded-lg p-1.5 text-muted hover:bg-card-soft hover:text-content"
        >
          <PanelLeftClose className="h-4 w-4" />
        </button>
      </div>

      {/* 搜索 → 命令面板 */}
      <div className="px-3 pt-3">
        <button
          type="button"
          onClick={() => setPaletteOpen(true)}
          aria-label="打开命令面板"
          className="flex w-full items-center gap-2 rounded-md border border-line-soft bg-bg-soft px-2.5 py-1.5 text-left text-xs text-faint transition-colors hover:border-line hover:text-muted"
        >
          <Search className="h-3.5 w-3.5" />
          <span className="flex-1 truncate">跳转 / 搜索</span>
          <kbd className="rounded bg-card-soft px-1 py-0.5 text-2xs text-faint">
            ⌘K
          </kbd>
        </button>
      </div>

      <nav className="flex-1 overflow-y-auto px-2 py-3">
        {sections.map((section) => {
          const items = section.items.filter(allowed)
          if (items.length === 0) return null
          return (
            <div key={section.title} className="mb-4">
              <div className="px-2 pb-1 text-2xs font-semibold uppercase tracking-wide text-faint">
                {section.title}
              </div>
              <div className="space-y-0.5">
                {items.map((item) => (
                  <SidebarNavItem key={item.to} item={item} />
                ))}
              </div>
            </div>
          )
        })}
      </nav>

      {/* 用户行 */}
      <div ref={menuRef} className="relative border-t border-line-soft p-2">
        <button
          type="button"
          onClick={() => setMenuOpen((v) => !v)}
          aria-haspopup="menu"
          aria-expanded={menuOpen}
          className="flex w-full items-center gap-2 rounded-lg p-1.5 text-left transition-colors hover:bg-card-soft"
        >
          <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-accent/15 text-xs font-semibold uppercase text-accent">
            {(user?.sub ?? '?').slice(0, 2)}
          </span>
          <span className="min-w-0 flex-1">
            <span className="block truncate text-xs font-medium text-content">
              {user?.sub ?? '未登录'}
            </span>
            <span className="block truncate text-2xs text-faint">
              {user?.roles?.join(' · ') || '无角色'}
            </span>
          </span>
          <ChevronRight
            className={cn(
              'h-3.5 w-3.5 shrink-0 text-faint transition-transform',
              menuOpen && '-rotate-90',
            )}
          />
        </button>

        {menuOpen && (
          <div
            role="menu"
            className="anim-scale absolute bottom-full left-2 right-2 z-50 mb-1 origin-bottom rounded-lg border border-line bg-card p-1 shadow-lg"
          >
            <div className="px-2.5 py-2">
              <div className="truncate text-xs font-semibold text-content">
                {user?.sub}
              </div>
              {user?.email && (
                <div className="mt-0.5 truncate text-2xs text-faint">
                  {user.email}
                </div>
              )}
              <div className="mt-1.5 flex flex-wrap gap-1">
                {user?.roles?.map((r) => (
                  <RoleBadge key={r} role={r} />
                ))}
              </div>
              {/* ABAC 范围:值班人员需要知道自己看到的是全量还是子集 ——
                  否则"列表里没有"会被误读成"没有故障"。 */}
              <div className="mt-2 space-y-0.5 text-2xs text-faint">
                <div className="truncate">
                  集群:{user?.clusters?.join(', ') || '—'}
                </div>
                <div className="truncate">
                  命名空间:{user?.namespaces?.join(', ') || '—'}
                </div>
              </div>
            </div>
            <div className="my-1 h-px bg-line-soft" />
            <ThemeMenuItem />
            <div className="my-1 h-px bg-line-soft" />
            <button
              type="button"
              role="menuitem"
              onClick={handleLogout}
              className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-xs text-muted hover:bg-card-soft hover:text-danger"
            >
              <LogOut className="h-3.5 w-3.5" />
              退出登录
            </button>
          </div>
        )}
      </div>
    </aside>
  )
}

function SidebarNavItem({ item }: { item: NavItem }) {
  return (
    <NavLink
      to={item.to}
      end={item.exact}
      className={({ isActive }) =>
        cn(
          'flex items-center gap-2 rounded-md px-2 py-1.5 text-xs transition-colors',
          'text-muted hover:bg-card-soft hover:text-content',
          isActive && 'bg-card-soft font-medium text-content',
        )
      }
    >
      <item.icon className="h-4 w-4 shrink-0" />
      <span className="flex-1 truncate">{item.label}</span>
      {/* 0 不渲染:"未认领 0"不需要一个红点去强调,那会让红点失去意义。 */}
      {item.badge != null && item.badge > 0 && (
        <span className="tabular ml-auto inline-flex min-w-[18px] items-center justify-center rounded-full bg-danger/90 px-1.5 text-2xs font-medium text-white">
          {item.badge > 99 ? '99+' : item.badge}
        </span>
      )}
    </NavLink>
  )
}

function themeMeta(preference: string, mode: string) {
  const label =
    preference === 'system' ? '跟随系统' : preference === 'dark' ? '深色' : '浅色'
  const Icon =
    preference === 'system' ? Monitor : mode === 'dark' ? Moon : Sun
  return { label, Icon }
}

function ThemeMenuItem() {
  const { preference, mode, cycle } = useTheme()
  const { label, Icon } = themeMeta(preference, mode)
  return (
    <button
      type="button"
      role="menuitem"
      onClick={cycle}
      className="flex w-full items-center justify-between gap-2 rounded-md px-2.5 py-1.5 text-xs text-muted hover:bg-card-soft hover:text-content"
    >
      <span className="flex items-center gap-2">
        <Icon className="h-3.5 w-3.5" />
        主题
      </span>
      <span className="text-2xs text-faint">{label}</span>
    </button>
  )
}

function ThemeToggleIcon() {
  const { preference, mode, cycle } = useTheme()
  const { label, Icon } = themeMeta(preference, mode)
  return (
    <button
      type="button"
      onClick={cycle}
      aria-label={`主题:${label}`}
      title={`主题:${label}`}
      className="rounded-lg p-2 text-muted hover:bg-card-soft hover:text-content"
    >
      <Icon className="h-4 w-4" />
    </button>
  )
}
