import type { ReactNode, HTMLAttributes } from 'react'
import { cn } from '@/lib/format'
import { Loader2, Inbox, AlertTriangle, RotateCw } from 'lucide-react'

// ── 卡片 ────────────────────────────────────────────────
// 统一的卡片面。所有颜色走 token(bg-card / border-line),
// 明暗切换由 <html> 的类驱动,不需要逐个 class 写 dark: 变体。

type CardProps = HTMLAttributes<HTMLDivElement> & {
  interactive?: boolean
  compact?: boolean
  as?: 'div' | 'section' | 'article'
}

export function Card({
  className,
  interactive,
  compact,
  as: Tag = 'section',
  ...rest
}: CardProps) {
  return (
    <Tag
      className={cn(
        'rounded-xl border border-line-soft bg-card',
        compact ? 'p-3' : '',
        interactive &&
          'transition-colors hover:border-line hover:bg-card-soft',
        className,
      )}
      {...rest}
    />
  )
}

export function CardHeader({
  title,
  right,
  icon,
  subtitle,
}: {
  title: ReactNode
  right?: ReactNode
  icon?: ReactNode
  subtitle?: ReactNode
}) {
  return (
    <div className="flex items-start justify-between gap-3 border-b border-line-soft px-4 py-3">
      <div className="min-w-0">
        <h3 className="flex items-center gap-2 text-sm font-semibold text-content">
          {icon}
          {title}
        </h3>
        {subtitle && (
          <p className="mt-0.5 text-2xs text-faint">{subtitle}</p>
        )}
      </div>
      {right && <div className="shrink-0">{right}</div>}
    </div>
  )
}

// ── 页头 ────────────────────────────────────────────────
// 每个页面顶部的统一条:标题 + 副标题 + 右侧动作 + 可选下方工具栏。
export function PageHeader({
  title,
  subtitle,
  actions,
  extra,
  leading,
}: {
  title: ReactNode
  subtitle?: ReactNode
  actions?: ReactNode
  extra?: ReactNode
  leading?: ReactNode
}) {
  return (
    <header className="app-header px-6 py-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          {leading && <div className="mb-1 text-xs">{leading}</div>}
          <h1 className="truncate text-base font-semibold text-content">
            {title}
          </h1>
          {subtitle && (
            <div className="mt-0.5 text-xs text-faint">{subtitle}</div>
          )}
        </div>
        {actions && (
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            {actions}
          </div>
        )}
      </div>
      {extra && <div className="mt-3">{extra}</div>}
    </header>
  )
}

// ── 按钮 ────────────────────────────────────────────────
type ButtonVariant = 'primary' | 'secondary' | 'danger' | 'ghost' | 'subtle'
type ButtonSize = 'sm' | 'md'

const BTN_VARIANT: Record<ButtonVariant, string> = {
  primary:
    'bg-accent text-accent-fg hover:bg-accent-strong disabled:opacity-50',
  secondary:
    'border border-line bg-card text-content hover:bg-card-soft disabled:opacity-50',
  danger: 'bg-danger text-white hover:opacity-90 disabled:opacity-50',
  ghost:
    'bg-transparent text-muted hover:bg-card-soft hover:text-content disabled:opacity-40',
  subtle:
    'bg-card-soft text-muted hover:text-content disabled:opacity-40',
}

const BTN_SIZE: Record<ButtonSize, string> = {
  sm: 'px-2 py-1 text-xs gap-1',
  md: 'px-3 py-1.5 text-sm gap-1.5',
}

export function Button({
  variant = 'secondary',
  size = 'md',
  loading,
  className,
  children,
  ...rest
}: {
  variant?: ButtonVariant
  size?: ButtonSize
  loading?: boolean
} & React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      {...rest}
      disabled={rest.disabled || loading}
      className={cn(
        'inline-flex items-center justify-center rounded-md font-medium transition-colors disabled:cursor-not-allowed',
        BTN_SIZE[size],
        BTN_VARIANT[variant],
        className,
      )}
    >
      {loading && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
      {children}
    </button>
  )
}

// ── 表单控件 ────────────────────────────────────────────
export const inputCls =
  'w-full rounded-md border border-line bg-bg-soft px-2.5 py-1.5 text-sm text-content outline-none transition-colors placeholder:text-faint focus:border-accent'

export const selectCls =
  'rounded-md border border-line bg-bg-soft px-2.5 py-1.5 text-sm text-content outline-none transition-colors focus:border-accent'

export function Field({
  label,
  hint,
  children,
}: {
  label: ReactNode
  hint?: ReactNode
  children: ReactNode
}) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs text-muted">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-2xs text-faint">{hint}</span>}
    </label>
  )
}

// ── 状态占位 ────────────────────────────────────────────
export function Spinner({ label }: { label?: string }) {
  return (
    <div className="flex items-center justify-center gap-2 py-10 text-muted">
      <Loader2 className="h-5 w-5 animate-spin" />
      {label && <span className="text-sm">{label}</span>}
    </div>
  )
}

export function EmptyState({
  title,
  hint,
  icon,
  action,
}: {
  title: string
  hint?: string
  icon?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 px-6 py-12 text-center">
      <div className="mb-1 text-faint">
        {icon ?? <Inbox className="h-7 w-7" />}
      </div>
      <p className="text-sm font-medium text-muted">{title}</p>
      {hint && <p className="max-w-sm text-xs text-faint">{hint}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  )
}

export function ErrorState({
  message,
  onRetry,
}: {
  message: string
  onRetry?: () => void
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 px-6 py-12 text-center">
      <AlertTriangle className="h-7 w-7 text-danger" />
      <p className="max-w-md text-sm text-danger">{message}</p>
      {onRetry && (
        <Button variant="secondary" size="sm" onClick={onRetry}>
          <RotateCw className="h-3.5 w-3.5" />
          重试
        </Button>
      )}
    </div>
  )
}

// 骨架块:用于首屏加载,避免整页塌成一个 spinner。
export function Skeleton({ className }: { className?: string }) {
  return (
    <div
      className={cn(
        'animate-pulse rounded-md bg-card-soft',
        className,
      )}
    />
  )
}

// ── 进度条 ──────────────────────────────────────────────
export function ProgressBar({
  value,
  max = 1,
  className,
  tone = 'accent',
}: {
  value: number
  max?: number
  className?: string
  tone?: 'accent' | 'warn' | 'danger' | 'ok'
}) {
  const ratio = max > 0 ? Math.min(1, Math.max(0, value / max)) : 0
  const toneClass = {
    accent: 'bg-accent',
    warn: 'bg-warn',
    danger: 'bg-danger',
    ok: 'bg-ok',
  }[tone]
  return (
    <div
      className={cn(
        'h-1.5 w-full overflow-hidden rounded-full bg-card-soft',
        className,
      )}
    >
      <div
        className={cn('h-full rounded-full transition-all', toneClass)}
        style={{ width: `${ratio * 100}%` }}
      />
    </div>
  )
}

// ── 统计卡 ──────────────────────────────────────────────
// 值班总览的主要构件。
//
// value 允许 ReactNode 以便传入 '—':样本不足时必须显示破折号而不是 0,
// 后者会被读成"MTTR 是 0 秒"这种荒谬但看起来正常的结论。
export function StatCard({
  label,
  value,
  hint,
  tone = 'default',
  icon,
  onClick,
  emphasis,
}: {
  label: ReactNode
  value: ReactNode
  hint?: ReactNode
  tone?: 'default' | 'danger' | 'warn' | 'ok' | 'accent'
  icon?: ReactNode
  onClick?: () => void
  // emphasis:值为 0 时是否仍然高亮。未确认告警为 0 是好事,
  // 不该显示成红色 —— 颜色要跟着"是否需要处置"变,不是跟着字段名。
  emphasis?: boolean
}) {
  const toneText = {
    default: 'text-content',
    danger: 'text-danger',
    warn: 'text-warn',
    ok: 'text-ok',
    accent: 'text-accent',
  }[emphasis === false ? 'default' : tone]

  const Tag = onClick ? 'button' : 'div'
  return (
    <Tag
      onClick={onClick}
      className={cn(
        'rounded-xl border border-line-soft bg-card p-3.5 text-left',
        onClick &&
          'transition-colors hover:border-line hover:bg-card-soft',
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-xs text-muted">{label}</span>
        {icon && <span className="shrink-0 text-faint">{icon}</span>}
      </div>
      <div
        className={cn(
          'tabular mt-1.5 text-2xl font-semibold leading-none',
          toneText,
        )}
      >
        {value}
      </div>
      {hint && <div className="mt-1.5 text-2xs text-faint">{hint}</div>}
    </Tag>
  )
}

// ── 分段控件 ────────────────────────────────────────────
// 时间窗切换、状态筛选用。比一排 <select> 更快 —— 值班时少一次点击。
export function SegmentedControl<T extends string | number>({
  value,
  options,
  onChange,
  size = 'md',
}: {
  value: T
  options: Array<{ value: T; label: ReactNode; title?: string }>
  onChange: (v: T) => void
  size?: 'sm' | 'md'
}) {
  return (
    <div
      role="tablist"
      className="inline-flex items-center gap-0.5 rounded-md border border-line-soft bg-bg-soft p-0.5"
    >
      {options.map((o) => (
        <button
          key={String(o.value)}
          role="tab"
          aria-selected={o.value === value}
          title={o.title}
          onClick={() => onChange(o.value)}
          className={cn(
            'rounded px-2 font-medium transition-colors',
            size === 'sm' ? 'py-0.5 text-2xs' : 'py-1 text-xs',
            o.value === value
              ? 'bg-card text-content shadow-sm'
              : 'text-muted hover:text-content',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

// ── 代码 / 单值展示 ─────────────────────────────────────
export function Mono({
  children,
  className,
  title,
}: {
  children: ReactNode
  className?: string
  title?: string
}) {
  return (
    <span
      title={title}
      className={cn(
        'rounded bg-card-soft px-1.5 py-0.5 font-mono text-xs text-muted',
        className,
      )}
    >
      {children}
    </span>
  )
}

// 键值行:详情页的信息块。
export function InfoItem({
  label,
  value,
}: {
  label: ReactNode
  value: ReactNode
}) {
  return (
    <div className="rounded-lg border border-line-soft bg-bg-soft px-3 py-2">
      <div className="text-2xs text-faint">{label}</div>
      <div className="mt-0.5 truncate text-sm text-content">{value}</div>
    </div>
  )
}

// ── 提示条 ──────────────────────────────────────────────
export function Callout({
  tone = 'info',
  children,
  icon,
}: {
  tone?: 'info' | 'warn' | 'danger' | 'ok'
  children: ReactNode
  icon?: ReactNode
}) {
  const toneCls = {
    info: 'border-info/30 bg-info/10 text-info',
    warn: 'border-warn/30 bg-warn/10 text-warn',
    danger: 'border-danger/30 bg-danger/10 text-danger',
    ok: 'border-ok/30 bg-ok/10 text-ok',
  }[tone]
  return (
    <div
      className={cn(
        'flex items-start gap-2 rounded-lg border px-3 py-2 text-xs',
        toneCls,
      )}
    >
      {icon && <span className="mt-0.5 shrink-0">{icon}</span>}
      <div className="min-w-0">{children}</div>
    </div>
  )
}
