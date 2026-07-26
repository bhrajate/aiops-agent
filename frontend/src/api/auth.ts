// 认证端点(SECURITY.md §1,仅 hs256 开发模式启用)。
import { request } from './client'
import type { LoginRequest, LoginResponse, UserClaims } from './types'

// POST /v1/auth/login —— 公开端点;401 表示凭证错误,不触发全局跳转。
export function login(payload: LoginRequest): Promise<LoginResponse> {
  return request<LoginResponse>('/v1/auth/login', {
    method: 'POST',
    body: payload,
    skipAuthRedirect: true,
  })
}

// GET /v1/auth/me —— 带 Bearer,校验/刷新当前用户 claims。
export function fetchMe(): Promise<UserClaims> {
  return request<Record<string, unknown>>('/v1/auth/me').then((data) => {
    // 后端可能返回 { user: {...} } 或直接 claims,做兼容
    const user = (data.user ?? data) as UserClaims
    return user
  })
}
