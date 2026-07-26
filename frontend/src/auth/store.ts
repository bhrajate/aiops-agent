// 认证状态的本地持久化 + 跨模块事件总线。
// token/claims 存 localStorage;api/client 与 React 层通过自定义事件解耦通信。
//
// ⚠️ 安全取舍(本轮已知、暂不改造后端):
//   - token 存 localStorage 便于跨标签页共享与 SSE(EventSource 无法带请求头),
//     但对 XSS 暴露。更稳妥的做法是后端下发 HttpOnly + Secure + SameSite Cookie,
//     前端不接触 token。后续若后端支持,应迁移到该方案并移除此处持久化。
import type { UserClaims } from '@/api/types'

const TOKEN_KEY = 'aiops_token'
const USER_KEY = 'aiops_user'
// token 绝对过期时间戳(epoch ms);由登录响应的 expires_in 推算,供主动登出用。
const EXPIRES_AT_KEY = 'aiops_expires_at'

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

// token 绝对过期时间戳(epoch ms)。无记录或不可用时返回 null。
export function getExpiresAt(): number | null {
  try {
    const raw = localStorage.getItem(EXPIRES_AT_KEY)
    if (!raw) return null
    const n = Number(raw)
    return Number.isFinite(n) ? n : null
  } catch {
    return null
  }
}

// 保存会话。expiresIn 为登录响应的剩余有效秒数(可选);换算成绝对过期时间戳持久化。
export function saveSession(
  token: string,
  user: UserClaims,
  expiresIn?: number,
): void {
  try {
    localStorage.setItem(TOKEN_KEY, token)
    localStorage.setItem(USER_KEY, JSON.stringify(user))
    if (typeof expiresIn === 'number' && expiresIn > 0) {
      localStorage.setItem(
        EXPIRES_AT_KEY,
        String(Date.now() + expiresIn * 1000),
      )
    }
    // 未提供 expiresIn(如 /auth/me 刷新)时保留既有过期时间,不覆盖。
  } catch {
    // localStorage 不可用时忽略(隐私模式等)
  }
}

export function clearSession(): void {
  try {
    localStorage.removeItem(TOKEN_KEY)
    localStorage.removeItem(USER_KEY)
    localStorage.removeItem(EXPIRES_AT_KEY)
  } catch {
    // 忽略
  }
}

// ── 事件总线(api/client 无 React 依赖,通过事件通知上层)──
// 401:未认证/令牌失效 → 上层清理并跳转登录
// 403:权限不足 → 上层弹出无权限提示,不跳登录
// session_expired:本地判定 token 已过期(主动登出),提示重新登录
export const AUTH_UNAUTHORIZED = 'aiops:unauthorized'
export const AUTH_FORBIDDEN = 'aiops:forbidden'
export const AUTH_SESSION_EXPIRED = 'aiops:session-expired'

export function emitUnauthorized(): void {
  window.dispatchEvent(new CustomEvent(AUTH_UNAUTHORIZED))
}

export function emitSessionExpired(message?: string): void {
  window.dispatchEvent(
    new CustomEvent(AUTH_SESSION_EXPIRED, { detail: { message } }),
  )
}

export function emitForbidden(message?: string): void {
  window.dispatchEvent(
    new CustomEvent(AUTH_FORBIDDEN, { detail: { message } }),
  )
}
