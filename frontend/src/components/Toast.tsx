import { useEffect, useState } from 'react'
import { AUTH_FORBIDDEN, AUTH_SESSION_EXPIRED } from '@/auth/store'
import {
  ShieldAlert,
  CheckCircle2,
  Info,
  AlertTriangle,
  X,
} from 'lucide-react'
import { cn } from '@/lib/format'

export type ToastTone = 'success' | 'error' | 'warn' | 'info'

interface ToastItem {
  id: number
  message: string
  tone: ToastTone
}

const TOAST_EVENT = 'aiops:toast'

// pushToast 从任意位置(含非组件的 mutation 回调)发出提示。
// 走自定义事件而不是 context:调用方不必是 ToastHost 的后代,
// 也不用为了一句提示把组件树串起来。
export function pushToast(message: string, tone: ToastTone = 'info'): void {
  window.dispatchEvent(
    new CustomEvent(TOAST_EVENT, { detail: { message, tone } }),
  )
}

const TONE_STYLE: Record<ToastTone, string> = {
  success: 'border-ok/40 text-ok',
  error: 'border-danger/40 text-danger',
  warn: 'border-warn/40 text-warn',
  info: 'border-info/40 text-info',
}

const TONE_ICON: Record<ToastTone, typeof Info> = {
  success: CheckCircle2,
  error: ShieldAlert,
  warn: AlertTriangle,
  info: Info,
}

// 全局轻量 toast:自有 pushToast + 监听 403 / 会话过期事件。不引入第三方库。
export function ToastHost() {
  const [items, setItems] = useState<ToastItem[]>([])

  useEffect(() => {
    const push = (message: string, tone: ToastTone) => {
      const id = Date.now() + Math.random()
      setItems((prev) => {
        // 上限 4 条:值班时若某个轮询持续报错,无上限会堆满整屏
        // 并挡住下面的操作按钮。
        const next = [...prev, { id, message, tone }]
        return next.length > 4 ? next.slice(-4) : next
      })
      // 错误留久一点:它通常需要被读完并采取行动。
      setTimeout(
        () => setItems((prev) => prev.filter((t) => t.id !== id)),
        tone === 'error' ? 8000 : 4500,
      )
    }
    const onToast = (e: Event) => {
      const d = (e as CustomEvent<{ message: string; tone: ToastTone }>).detail
      push(d.message, d.tone ?? 'info')
    }
    const onForbidden = (e: Event) => {
      const detail = (e as CustomEvent<{ message?: string }>).detail
      push(detail?.message || '无权限访问该资源', 'error')
    }
    const onExpired = (e: Event) => {
      const detail = (e as CustomEvent<{ message?: string }>).detail
      push(detail?.message || '登录已过期,请重新登录', 'warn')
    }
    window.addEventListener(TOAST_EVENT, onToast)
    window.addEventListener(AUTH_FORBIDDEN, onForbidden)
    window.addEventListener(AUTH_SESSION_EXPIRED, onExpired)
    return () => {
      window.removeEventListener(TOAST_EVENT, onToast)
      window.removeEventListener(AUTH_FORBIDDEN, onForbidden)
      window.removeEventListener(AUTH_SESSION_EXPIRED, onExpired)
    }
  }, [])

  function dismiss(id: number) {
    setItems((prev) => prev.filter((t) => t.id !== id))
  }

  if (items.length === 0) return null

  return (
    <div
      className="fixed bottom-4 right-4 z-[60] flex flex-col gap-2"
      role="status"
      aria-live="polite"
    >
      {items.map((t) => {
        const Icon = TONE_ICON[t.tone]
        return (
          <div
            key={t.id}
            className={cn(
              'anim-scale flex items-start gap-2 rounded-lg border bg-card px-3 py-2 text-xs shadow-lg',
              TONE_STYLE[t.tone],
            )}
          >
            <Icon className="mt-0.5 h-4 w-4 shrink-0" />
            <span className="max-w-xs">{t.message}</span>
            <button
              onClick={() => dismiss(t.id)}
              aria-label="关闭提示"
              className="ml-1 rounded p-0.5 opacity-70 hover:bg-card-soft hover:opacity-100"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          </div>
        )
      })}
    </div>
  )
}
