import { useState } from 'react'
import { useNavigate, useLocation, Navigate } from 'react-router-dom'
import { useAuth } from '@/auth/context'
import { Button } from '@/components/ui'
import { HttpError } from '@/api/client'
import { Siren, LogIn } from 'lucide-react'

interface DemoAccount {
  username: string
  password: string
  desc: string
}

// 演示账号仅在开发构建注入;生产构建(import.meta.env.DEV === false)下
// 该数组为空,配合下方条件渲染,明文凭证不会进入生产 bundle。
const DEMO_ACCOUNTS: DemoAccount[] = import.meta.env.DEV
  ? [
      { username: 'alice', password: 'alice-pass', desc: 'sre · 全命名空间' },
      { username: 'bob', password: 'bob-pass', desc: 'oncall · payment+cart' },
      {
        username: 'viewer',
        password: 'viewer-pass',
        desc: 'viewer · 只读 payment',
      },
    ]
  : []

const inputCls =
  'w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 text-sm text-slate-200 outline-none focus:border-accent'

export function LoginPage() {
  const { isAuthenticated, login } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  // 登录后跳回原页面(ProtectedRoute 写入的 state.from),默认回首页
  const from =
    (location.state as { from?: string } | null)?.from ?? '/'

  if (isAuthenticated) {
    return <Navigate to={from} replace />
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setLoading(true)
    try {
      await login(username.trim(), password)
      navigate(from, { replace: true })
    } catch (err) {
      setError(
        err instanceof HttpError
          ? `登录失败:${err.message}`
          : '登录失败,请确认后端 :8088 已启动',
      )
    } finally {
      setLoading(false)
    }
  }

  function fillDemo(acc: DemoAccount) {
    setUsername(acc.username)
    setPassword(acc.password)
    setError(null)
  }

  return (
    <div className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex flex-col items-center gap-2 text-center">
          <Siren className="h-8 w-8 text-accent" />
          <h1 className="text-lg font-semibold text-slate-100">
            AIOps Incident Workbench
          </h1>
          <p className="text-xs text-slate-500">登录以进入值班台</p>
        </div>

        <form
          onSubmit={submit}
          className="rounded-lg border border-surface-700 bg-surface-850 p-5 shadow-sm"
        >
          <label className="mb-3 block text-xs text-slate-400">
            用户名
            <input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoFocus
              autoComplete="username"
              className={`mt-1 ${inputCls}`}
              placeholder="alice"
            />
          </label>
          <label className="mb-4 block text-xs text-slate-400">
            密码
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
              className={`mt-1 ${inputCls}`}
              placeholder="••••••••"
            />
          </label>

          {error && (
            <p className="mb-3 rounded-md bg-red-500/10 px-3 py-2 text-xs text-red-300">
              {error}
            </p>
          )}

          <Button
            type="submit"
            variant="primary"
            loading={loading}
            disabled={!username || !password}
            className="w-full"
          >
            <LogIn className="h-4 w-4" />
            登录
          </Button>
        </form>

        {DEMO_ACCOUNTS.length > 0 && (
        <div className="mt-4 rounded-lg border border-surface-700 bg-surface-900/60 p-4">
          <p className="mb-2 text-xs font-medium text-slate-400">
            演示账号(点击自动填充)
          </p>
          <ul className="space-y-1.5">
            {DEMO_ACCOUNTS.map((acc) => (
              <li key={acc.username}>
                <button
                  type="button"
                  onClick={() => fillDemo(acc)}
                  className="flex w-full items-center justify-between rounded-md bg-surface-800 px-3 py-2 text-left text-xs hover:bg-surface-700"
                >
                  <span className="font-mono text-slate-200">
                    {acc.username} / {acc.password}
                  </span>
                  <span className="text-slate-500">{acc.desc}</span>
                </button>
              </li>
            ))}
          </ul>
        </div>
        )}
      </div>
    </div>
  )
}
