import { cn } from '@/lib/format'
import { Loader2 } from 'lucide-react'

export function Card({
  children,
  className,
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        'rounded-lg border border-surface-700 bg-surface-850 shadow-sm',
        className,
      )}
    >
      {children}
    </div>
  )
}

export function CardHeader({
  title,
  right,
  icon,
}: {
  title: React.ReactNode
  right?: React.ReactNode
  icon?: React.ReactNode
}) {
  return (
    <div className="flex items-center justify-between border-b border-surface-700 px-4 py-3">
      <h3 className="flex items-center gap-2 text-sm font-semibold text-slate-200">
        {icon}
        {title}
      </h3>
      {right}
    </div>
  )
}

type ButtonVariant = 'primary' | 'secondary' | 'danger' | 'ghost'

const BTN_VARIANT: Record<ButtonVariant, string> = {
  primary:
    'bg-accent-muted text-white hover:bg-accent disabled:opacity-50',
  secondary:
    'bg-surface-700 text-slate-200 hover:bg-surface-600 disabled:opacity-50',
  danger:
    'bg-red-600/90 text-white hover:bg-red-600 disabled:opacity-50',
  ghost:
    'bg-transparent text-slate-300 hover:bg-surface-700 disabled:opacity-40',
}

export function Button({
  variant = 'secondary',
  loading,
  className,
  children,
  ...rest
}: {
  variant?: ButtonVariant
  loading?: boolean
} & React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      {...rest}
      disabled={rest.disabled || loading}
      className={cn(
        'inline-flex items-center justify-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-colors disabled:cursor-not-allowed',
        BTN_VARIANT[variant],
        className,
      )}
    >
      {loading && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
      {children}
    </button>
  )
}

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="flex items-center justify-center gap-2 py-10 text-slate-400">
      <Loader2 className="h-5 w-5 animate-spin" />
      {label && <span className="text-sm">{label}</span>}
    </div>
  )
}

export function EmptyState({
  title,
  hint,
}: {
  title: string
  hint?: string
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-1 py-12 text-center">
      <p className="text-sm font-medium text-slate-300">{title}</p>
      {hint && <p className="text-xs text-slate-500">{hint}</p>}
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
    <div className="flex flex-col items-center justify-center gap-3 py-12 text-center">
      <p className="text-sm text-red-300">{message}</p>
      {onRetry && (
        <Button variant="secondary" onClick={onRetry}>
          重试
        </Button>
      )}
    </div>
  )
}

// 进度条(预算/用量、置信度通用)
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
    warn: 'bg-amber-400',
    danger: 'bg-red-500',
    ok: 'bg-emerald-400',
  }[tone]
  return (
    <div
      className={cn(
        'h-1.5 w-full overflow-hidden rounded-full bg-surface-700',
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
