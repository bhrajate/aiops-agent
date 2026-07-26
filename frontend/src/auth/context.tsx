import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react'
import type { ReactNode } from 'react'
import type { UserClaims } from '@/api/types'
import { login as apiLogin, fetchMe } from '@/api/auth'
import {
  getToken,
  getStoredUser,
  saveSession,
  clearSession,
  AUTH_UNAUTHORIZED,
} from './store'

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

  const logout = useCallback(() => {
    clearSession()
    setUser(null)
  }, [])

  const login = useCallback(async (username: string, password: string) => {
    const res = await apiLogin({ username, password })
    saveSession(res.token, res.user)
    setUser(res.user)
  }, [])

  // 全局 401 事件 → 清理会话(client 已清 token,这里同步 React 状态)
  useEffect(() => {
    const onUnauthorized = () => {
      clearSession()
      setUser(null)
    }
    window.addEventListener(AUTH_UNAUTHORIZED, onUnauthorized)
    return () => window.removeEventListener(AUTH_UNAUTHORIZED, onUnauthorized)
  }, [])

  // 启动时若已有 token,后台校验并刷新 claims(令牌失效会走 401 清理)
  useEffect(() => {
    if (getToken()) {
      fetchMe()
        .then((u) => {
          setUser(u)
          const t = getToken()
          if (t) saveSession(t, u)
        })
        .catch(() => {
          // 401 已由 client 处理;其他错误保留本地 claims
        })
    }
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
