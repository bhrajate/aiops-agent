// 认证状态的本地持久化 + 跨模块事件总线。
// token/claims 存 localStorage;api/client 与 React 层通过自定义事件解耦通信。
import type { UserClaims } from '@/api/types'

const TOKEN_KEY = 'aiops_token'
const USER_KEY = 'aiops_user'

export function getToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY)
  } catch {
    return null
  }
}

export function getStoredUser(): UserClaims | null {
  try {
    const raw = localStorage.getItem(USER_KEY)
    return raw ? (JSON.parse(raw) as UserClaims) : null
  } catch {
    return null
  }
}

export function saveSession(token: string, user: UserClaims): void {
  try {
    localStorage.setItem(TOKEN_KEY, token)
    localStorage.setItem(USER_KEY, JSON.stringify(user))
  } catch {
    // localStorage 不可用时忽略(隐私模式等)
  }
}

export function clearSession(): void {
  try {
    localStorage.removeItem(TOKEN_KEY)
    localStorage.removeItem(USER_KEY)
  } catch {
    // 忽略
  }
}

// ── 事件总线(api/client 无 React 依赖,通过事件通知上层)──
// 401:未认证/令牌失效 → 上层清理并跳转登录
// 403:权限不足 → 上层弹出无权限提示,不跳登录
export const AUTH_UNAUTHORIZED = 'aiops:unauthorized'
export const AUTH_FORBIDDEN = 'aiops:forbidden'

export function emitUnauthorized(): void {
  window.dispatchEvent(new CustomEvent(AUTH_UNAUTHORIZED))
}

export function emitForbidden(message?: string): void {
  window.dispatchEvent(
    new CustomEvent(AUTH_FORBIDDEN, { detail: { message } }),
  )
}
