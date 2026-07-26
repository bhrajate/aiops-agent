// 各业务端点的请求封装,路径严格对齐 INTEGRATION.md 公共 API。
import { request, newIdempotencyKey } from './client'
import type {
  Incident,
  Investigation,
  Evidence,
  FeedbackRequest,
  SignalRequest,
  IncidentStatus,
  Severity,
} from './types'

export interface IncidentListParams {
  status?: IncidentStatus
  severity?: Severity
  limit?: number
}

function buildQuery(params: Record<string, string | number | undefined>): string {
  const usp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') usp.set(k, String(v))
  }
  const s = usp.toString()
  return s ? `?${s}` : ''
}

// 后端可能返回 { items: [...] } 或直接数组,做兼容
function unwrapList<T>(data: unknown): T[] {
  if (Array.isArray(data)) return data as T[]
  if (data && typeof data === 'object') {
    const obj = data as Record<string, unknown>
    for (const key of ['items', 'incidents', 'data', 'results']) {
      if (Array.isArray(obj[key])) return obj[key] as T[]
    }
  }
  return []
}

export async function listIncidents(
  params: IncidentListParams = {},
): Promise<Incident[]> {
  const data = await request<unknown>(
    `/v1/incidents${buildQuery({ ...params })}`,
  )
  return unwrapList<Incident>(data)
}

// 后端返回 { incident: {...}, investigations: [...] };前端组件按扁平结构消费,这里合并。
export async function getIncident(id: string): Promise<Incident> {
  const data = await request<Record<string, unknown>>(
    `/v1/incidents/${encodeURIComponent(id)}`,
  )
  const incident = (data.incident ?? data) as Incident
  const investigations = Array.isArray(data.investigations)
    ? (data.investigations as Incident['investigations'])
    : incident.investigations
  return { ...incident, investigations }
}

export function startInvestigation(
  incidentId: string,
): Promise<Investigation> {
  return request<Investigation>(
    `/v1/incidents/${encodeURIComponent(incidentId)}/investigations`,
    {
      method: 'POST',
      headers: { 'Idempotency-Key': newIdempotencyKey() },
      body: {},
    },
  )
}

// 后端返回 { investigation: {...}, hypotheses, evidence, feedback };合并为扁平结构。
export async function getInvestigation(id: string): Promise<Investigation> {
  const data = await request<Record<string, unknown>>(
    `/v1/investigations/${encodeURIComponent(id)}`,
  )
  const investigation = (data.investigation ?? data) as Investigation
  return {
    ...investigation,
    hypotheses: (data.hypotheses as Investigation['hypotheses']) ?? investigation.hypotheses ?? [],
    evidence: (data.evidence as Investigation['evidence']) ?? investigation.evidence ?? [],
    feedback: (data.feedback as Investigation['feedback']) ?? investigation.feedback ?? [],
  }
}

export function cancelInvestigation(id: string): Promise<void> {
  return request<void>(
    `/v1/investigations/${encodeURIComponent(id)}/cancel`,
    { method: 'POST', body: {} },
  )
}

export function sendFeedback(
  id: string,
  payload: FeedbackRequest,
): Promise<void> {
  return request<void>(
    `/v1/investigations/${encodeURIComponent(id)}/feedback`,
    { method: 'POST', body: payload },
  )
}

export function getEvidence(evidenceId: string): Promise<Evidence> {
  return request<Evidence>(
    `/v1/evidence/${encodeURIComponent(evidenceId)}`,
  )
}

export function postSignal(payload: SignalRequest): Promise<unknown> {
  return request<unknown>('/v1/signals', { method: 'POST', body: payload })
}
