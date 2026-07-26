import { useEffect, useState } from 'react'
import { AUTH_FORBIDDEN, AUTH_SESSION_EXPIRED } from '@/auth/store'
import { ShieldAlert, X } from 'lucide-react'

interface ToastItem {
  id: number
  message: string
}

// 全局轻量 toast:监听 403(权限不足)与会话过期事件并提示。不引入第三方库。
export function ToastHost() {
  const [items, setItems] = useState<ToastItem[]>([])

  useEffect(() => {
    const push = (message: string) => {
      const id = Date.now() + Math.random()
      setItems((prev) => [...prev, { id, message }])
      setTimeout(() => {
        setItems((prev) => prev.filter((t) => t.id !== id))
      }, 5000)
    }
    const onForbidden = (e: Event) => {
      const detail = (e as CustomEvent<{ message?: string }>).detail
      push(detail?.message || '无权限访问该资源')
    }
    const onExpired = (e: Event) => {
      const detail = (e as CustomEvent<{ message?: string }>).detail
      push(detail?.message || '登录已过期,请重新登录')
    }
    window.addEventListener(AUTH_FORBIDDEN, onForbidden)
    window.addEventListener(AUTH_SESSION_EXPIRED, onExpired)
    return () => {
      window.removeEventListener(AUTH_FORBIDDEN, onForbidden)
      window.removeEventListener(AUTH_SESSION_EXPIRED, onExpired)
    }
  }, [])

  function dismiss(id: number) {
    setItems((prev) => prev.filter((t) => t.id !== id))
  }

  if (items.length === 0) return null

  return (
    <div className="fixed bottom-4 right-4 z-[60] flex flex-col gap-2">
      {items.map((t) => (
        <div
          key={t.id}
          className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-surface-850 px-3 py-2 text-sm text-amber-200 shadow-lg"
        >
          <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-amber-400" />
          <span className="max-w-xs">{t.message}</span>
          <button
            onClick={() => dismiss(t.id)}
            className="ml-1 rounded p-0.5 text-amber-300/70 hover:bg-surface-700 hover:text-amber-200"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
      ))}
    </div>
  )
}
