import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import type { ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import type { UserClaims } from '@/api/types'
import { login as apiLogin, fetchMe } from '@/api/auth'
import {
  getToken,
  getStoredUser,
  getExpiresAt,
  saveSession,
  clearSession,
  emitSessionExpired,
  AUTH_UNAUTHORIZED,
} from './store'

// token 过期前提前主动登出的余量(ms):留出缓冲,避免临界请求撞 401。
const EXPIRY_LEEWAY_MS = 5_000
// setTimeout 单次可靠上限(约 24.8 天);超出则分段重排,避免溢出立即触发。
const MAX_TIMER_MS = 2_147_483_647

// 具备写权限(启动/取消调查、反馈/确认/关闭)的角色。viewer 只读。
const WRITE_ROLES = ['oncall', 'sre', 'admin']

interface AuthContextValue {
  user: UserClaims | null
  isAuthenticated: boolean
  // 是否具备写操作权限(前端体验优化,后端仍强制)
  canWrite: boolean
  hasRole: (role: string) => boolean
  login: (username: string, password: string) => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<UserClaims | null>(() => getStoredUser())
  const queryClient = useQueryClient()
  // 过期定时器句柄(跨渲染保持,便于清理/重排)。
  const expiryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const clearExpiryTimer = useCallback(() => {
    if (expiryTimerRef.current !== null) {
      clearTimeout(expiryTimerRef.current)
      expiryTimerRef.current = null
    }
  }, [])

  // 会话失效收尾:清 localStorage、清 React 状态、清 react-query 缓存。
  // queryClient.clear() 避免共用浏览器时下个用户看到上个会话的缓存数据。
  const teardownSession = useCallback(() => {
    clearExpiryTimer()
    clearSession()
    setUser(null)
    queryClient.clear()
  }, [clearExpiryTimer, queryClient])

  const logout = useCallback(() => {
    teardownSession()
  }, [teardownSession])

  // 依据本地记录的绝对过期时间安排一个定时器:到期前 EXPIRY_LEEWAY_MS 主动登出并提示。
  // 已过期则立即触发;时长超出 setTimeout 上限则分段重排。
  const scheduleExpiry = useCallback(() => {
    clearExpiryTimer()
    const expiresAt = getExpiresAt()
    if (expiresAt === null) return // 无过期信息(如旧会话)则依赖下次请求 401 兜底

    const fireExpired = () => {
      teardownSession()
      emitSessionExpired('登录已过期,请重新登录')
    }

    const delay = expiresAt - EXPIRY_LEEWAY_MS - Date.now()
    if (delay <= 0) {
      fireExpired()
      return
    }
    if (delay > MAX_TIMER_MS) {
      // 超长有效期:先睡到上限再重新评估。
      expiryTimerRef.current = setTimeout(scheduleExpiry, MAX_TIMER_MS)
      return
    }
    expiryTimerRef.current = setTimeout(fireExpired, delay)
  }, [clearExpiryTimer, teardownSession])

  const login = useCallback(
    async (username: string, password: string) => {
      const res = await apiLogin({ username, password })
      saveSession(res.token, res.user, res.expires_in)
      setUser(res.user)
      scheduleExpiry()
    },
    [scheduleExpiry],
  )

  // 全局 401 事件 → 清理会话(client 已清 token,这里同步 React 状态与缓存)
  useEffect(() => {
    const onUnauthorized = () => {
      teardownSession()
    }
    window.addEventListener(AUTH_UNAUTHORIZED, onUnauthorized)
    return () => window.removeEventListener(AUTH_UNAUTHORIZED, onUnauthorized)
  }, [teardownSession])

  // 启动时若已有 token:排定过期定时器,并后台校验/刷新 claims(令牌失效走 401 清理)
  useEffect(() => {
    if (getToken()) {
      scheduleExpiry()
      fetchMe()
        .then((u) => {
          setUser(u)
          const t = getToken()
          // 刷新 claims 时不带 expires_in,保留既有过期时间。
          if (t) saveSession(t, u)
        })
        .catch(() => {
          // 401 已由 client 处理;其他错误保留本地 claims
        })
    }
    return () => clearExpiryTimer()
    // 仅在挂载时执行一次
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const value = useMemo<AuthContextValue>(() => {
    const roles = user?.roles ?? []
    return {
      user,
      isAuthenticated: !!user,
      canWrite: roles.some((r) => WRITE_ROLES.includes(r)),
      hasRole: (role: string) => roles.includes(role),
      login,
      logout,
    }
  }, [user, login, logout])

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth 必须在 AuthProvider 内使用')
  return ctx
}
