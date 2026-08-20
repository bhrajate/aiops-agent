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
  Overview,
  InvestigationListItem,
  AuditPage,
  AuditResult,
  GoldenCase,
  ReviewStatus,
  KnowledgeItem,
} from './types'

export interface IncidentListParams {
  status?: IncidentStatus
  severity?: Severity
  limit?: number
}

function buildQuery(
  params: Record<string, string | number | boolean | undefined>,
): string {
  const usp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    // 0 是有效值(如 before_id=0 表示首页),只跳过 undefined 与空串。
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
  // 两层模型:alert_groups 与 incident 平级返回,合并进来供详情页展示
  const alertGroups = Array.isArray(data.alert_groups)
    ? (data.alert_groups as Incident['alert_groups'])
    : incident.alert_groups
  // relations 同样平级返回:拓扑上疑似同源的其他 incident
  const relations = Array.isArray(data.relations)
    ? (data.relations as Incident['relations'])
    : incident.relations
  return {
    ...incident,
    investigations,
    alert_groups: alertGroups,
    relations,
  }
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

// /v1/signals 用 webhook HMAC 签名鉴权而非用户 JWT(SECURITY.md §4)。
// 演示环境缺签名可能返回 401,此处跳过全局登录跳转,由 UI 友好提示。
export function postSignal(payload: SignalRequest): Promise<unknown> {
  return request<unknown>('/v1/signals', {
    method: 'POST',
    body: payload,
    skipAuthRedirect: true,
  })
}

// ── 值班总览 ────────────────────────────────────────────
// 单个端点返回整块首屏。若拆成八个请求,任意一个失败会让页面呈现
// **部分真实**的状态,而值班人员无法分辨哪个数字是坏的。
export function getOverview(hours = 24): Promise<Overview> {
  return request<Overview>(`/v1/overview${buildQuery({ hours })}`)
}

// ── 调查队列(跨 incident)──────────────────────────────
export interface InvestigationListParams {
  // 竖线分隔的多阶段,如 'collecting|planning'
  phase?: string
  // 只看非终态 —— 回答"现在有什么在跑"
  active?: boolean
  limit?: number
}

export async function listInvestigations(
  params: InvestigationListParams = {},
): Promise<InvestigationListItem[]> {
  const data = await request<unknown>(
    `/v1/investigations${buildQuery({
      phase: params.phase,
      active: params.active ? 'true' : undefined,
      limit: params.limit,
    })}`,
  )
  return unwrapList<InvestigationListItem>(data)
}

// ── 认领 / 标记已解决 ───────────────────────────────────
// 只支持 acknowledged / resolved。关闭走调查反馈的 close 动作 ——
// 关闭意味着"这个结论我认了",必须与反馈一起记录。
export function updateIncidentStatus(
  incidentId: string,
  status: 'acknowledged' | 'resolved',
): Promise<{ incident_id: string; status: string; changed: boolean }> {
  return request(`/v1/incidents/${encodeURIComponent(incidentId)}/status`, {
    method: 'POST',
    body: { status },
  })
}

// ── 审计日志 ────────────────────────────────────────────
export interface AuditParams {
  actor?: string
  action?: string
  target_type?: string
  target_id?: string
  result?: AuditResult | ''
  hours?: number
  limit?: number
  before_id?: number
}

export function listAudit(params: AuditParams = {}): Promise<AuditPage> {
  return request<AuditPage>(`/v1/audit${buildQuery({ ...params })}`)
}

// ── 评测用例(反馈闭环)─────────────────────────────────
export async function listGoldenCases(
  status: ReviewStatus | 'all' = 'pending',
  limit = 50,
): Promise<GoldenCase[]> {
  const data = await request<Record<string, unknown>>(
    `/v1/golden-cases${buildQuery({ status, limit })}`,
  )
  if (Array.isArray(data.golden_cases)) return data.golden_cases as GoldenCase[]
  return unwrapList<GoldenCase>(data)
}

export function reviewGoldenCase(
  caseId: string,
  status: 'approved' | 'rejected',
  note?: string,
): Promise<unknown> {
  return request(`/v1/golden-cases/${encodeURIComponent(caseId)}/review`, {
    method: 'POST',
    body: { status, note },
  })
}

// ── 知识库检索 ──────────────────────────────────────────
export async function searchKnowledge(q: string): Promise<KnowledgeItem[]> {
  const data = await request<unknown>(`/v1/knowledge${buildQuery({ q })}`)
  return unwrapList<KnowledgeItem>(data)
}
