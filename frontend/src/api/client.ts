// 轻量 fetch 封装。默认使用相对路径 /v1(经 Vite dev proxy 转发到 :8088),
// 也可通过 VITE_API_BASE 指向独立网关。

const API_BASE = (import.meta.env.VITE_API_BASE ?? '').replace(/\/$/, '')

export function apiUrl(path: string): string {
  // path 形如 /v1/incidents
  return `${API_BASE}${path}`
}

export class HttpError extends Error {
  status: number
  code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
    this.name = 'HttpError'
  }
}

async function parseError(res: Response): Promise<HttpError> {
  let code = 'unknown'
  let message = `请求失败(HTTP ${res.status})`
  try {
    const body = await res.json()
    if (body?.error) {
      code = body.error.code ?? code
      message = body.error.message ?? message
    }
  } catch {
    // 忽略非 JSON 响应体
  }
  return new HttpError(res.status, code, message)
}

interface RequestOptions {
  method?: string
  body?: unknown
  headers?: Record<string, string>
  signal?: AbortSignal
}

export async function request<T>(
  path: string,
  opts: RequestOptions = {},
): Promise<T> {
  const { method = 'GET', body, headers = {}, signal } = opts
  const res = await fetch(apiUrl(path), {
    method,
    headers: {
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...headers,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
  })

  if (!res.ok) {
    throw await parseError(res)
  }

  // 204 或空响应体
  if (res.status === 204) {
    return undefined as T
  }
  const text = await res.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}

// 简易幂等键生成(优先使用原生 randomUUID)
export function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return crypto.randomUUID()
  }
  return `idem-${Date.now()}-${Math.random().toString(16).slice(2)}`
}
