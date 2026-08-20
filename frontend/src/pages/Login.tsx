import { useState } from 'react'
import { useNavigate, useLocation, Navigate } from 'react-router-dom'
import { useAuth } from '@/auth/context'
import { Button, Field, inputCls, Callout } from '@/components/ui'
import { useTheme } from '@/store/theme'
import { HttpError } from '@/api/client'
import { Activity, LogIn, Sun, Moon, Monitor } from 'lucide-react'

interface DemoAccount {
  username: string
  password: string
  role: string
  scope: string
}

// 演示账号仅在开发构建注入;生产构建(import.meta.env.DEV === false)下
// 该数组为空,配合下方条件渲染,明文凭证不会进入生产 bundle。
const DEMO_ACCOUNTS: DemoAccount[] = import.meta.env.DEV
  ? [
      {
        username: 'alice',
        password: 'alice-pass',
        role: 'sre',
        scope: '全集群 / 全命名空间',
      },
      {
        username: 'bob',
        password: 'bob-pass',
        role: 'oncall',
        scope: 'prod-cn-1 的 payment + cart',
      },
      {
        username: 'viewer',
        password: 'viewer-pass',
        role: 'viewer',
        scope: '只读 payment',
      },
    ]
  : []

export function LoginPage() {
  const { isAuthenticated, login } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const { preference, mode, cycle } = useTheme()

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  // 登录后跳回原页面(ProtectedRoute 写入的 state.from),默认回首页
  const from = (location.state as { from?: string } | null)?.from ?? '/'

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
          ? err.status === 401
            ? '用户名或密码错误'
            : `登录失败:${err.message}`
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

  const ThemeIcon =
    preference === 'system' ? Monitor : mode === 'dark' ? Moon : Sun

  return (
    <div className="flex min-h-screen items-center justify-center bg-bg px-4 py-10">
      <button
        type="button"
        onClick={cycle}
        aria-label="切换主题"
        title="切换主题"
        className="fixed right-4 top-4 rounded-lg p-2 text-muted hover:bg-card-soft hover:text-content"
      >
        <ThemeIcon className="h-4 w-4" />
      </button>

      <div className="anim-rise w-full max-w-sm">
        <div className="mb-6 flex flex-col items-center gap-2 text-center">
          <span className="flex h-11 w-11 items-center justify-center rounded-xl bg-accent/15">
            <Activity className="h-6 w-6 text-accent" />
          </span>
          <h1 className="text-lg font-semibold text-content">
            AIOps 值班台
          </h1>
          <p className="text-xs text-faint">
            Incident 驱动 · 证据优先 · 默认只读
          </p>
        </div>

        <form
          onSubmit={submit}
          className="rounded-xl border border-line-soft bg-card p-5"
        >
          <div className="space-y-3">
            <Field label="用户名">
              <input
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoFocus
                autoComplete="username"
                className={inputCls}
                placeholder="alice"
              />
            </Field>
            <Field label="密码">
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                className={inputCls}
                placeholder="••••••••"
              />
            </Field>
          </div>

          {error && (
            <div className="mt-3">
              <Callout tone="danger">{error}</Callout>
            </div>
          )}

          <Button
            type="submit"
            variant="primary"
            loading={loading}
            disabled={!username || !password}
            className="mt-4 w-full"
          >
            <LogIn className="h-4 w-4" />
            登录
          </Button>
        </form>

        {DEMO_ACCOUNTS.length > 0 && (
          <div className="mt-4 rounded-xl border border-line-soft bg-card p-4">
            <p className="mb-2 text-xs font-medium text-muted">
              演示账号(点击填充)
            </p>
            <ul className="space-y-1.5">
              {DEMO_ACCOUNTS.map((acc) => (
                <li key={acc.username}>
                  <button
                    type="button"
                    onClick={() => fillDemo(acc)}
                    className="flex w-full items-center justify-between gap-2 rounded-lg border border-line-soft bg-bg-soft px-3 py-2 text-left transition-colors hover:border-line hover:bg-card-soft"
                  >
                    <span className="min-w-0">
                      <span className="block truncate font-mono text-2xs text-content">
                        {acc.username} / {acc.password}
                      </span>
                      <span className="block truncate text-2xs text-faint">
                        {acc.scope}
                      </span>
                    </span>
                    <span className="shrink-0 rounded bg-card-soft px-1.5 py-0.5 text-2xs uppercase text-muted">
                      {acc.role}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
            {/* 三个账号权限不同是刻意的:切换账号能直接看到 ABAC 生效
                (bob 看不到 inventory 命名空间的 incident)。 */}
            <p className="mt-2 text-2xs leading-relaxed text-faint">
              三个账号的集群 / 命名空间范围不同,切换后可以直接看到 ABAC
              过滤的效果。
            </p>
          </div>
        )}
      </div>
    </div>
  )
}
