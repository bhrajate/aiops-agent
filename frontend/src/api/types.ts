// 与 shared/schemas/contracts.md 对齐的数据类型定义。
// 修改需同步契约文件。

export type IncidentStatus = 'open' | 'acknowledged' | 'resolved' | 'closed'
export type Severity = 'P1' | 'P2' | 'P3' | 'P4'

export interface ResourceRef {
  kind: string
  name: string
  namespace: string
  uid?: string
}

export interface BlastRadius {
  namespaces: number
  // 后端实际返回 resources;services 为早期契约命名,保留可选兼容
  resources?: number
  services?: number
}

export interface Incident {
  incident_id: string
  version: number
  grouping_key: string
  status: IncidentStatus
  severity: Severity
  title?: string
  fault_category: string
  affected_resources: ResourceRef[]
  blast_radius: BlastRadius
  topology_refs: string[]
  change_refs: string[]
  first_seen: string
  last_seen: string
  // 详情响应中后端附带的调查引用(与 incident 平级,在 getIncident 中合并进来)
  current_investigation_id?: string
  investigation_ids?: string[]
  investigations?: Array<{
    investigation_id: string
    phase?: InvestigationPhase
    trigger_reason?: string
  }>
}

// 调查阶段状态机(contracts.md / 文档 7.3)
export type InvestigationPhase =
  | 'queued'
  | 'triaging'
  | 'triage_published'
  | 'planning'
  | 'collecting'
  | 'synthesizing'
  | 'concluded'
  | 'needs_human'
  | 'waiting_feedback'
  | 'closed'
  | 'cancelled'

export interface Budget {
  max_duration_sec: number
  max_rounds: number
  max_tokens: number
  max_cost_usd: number
  max_tool_calls: number
}

export interface Usage {
  elapsed_sec: number
  rounds: number
  tokens: number
  cost_usd: number
  tool_calls: number
}

export type EvidenceType =
  | 'metric'
  | 'log'
  | 'trace'
  | 'kubernetes'
  | 'change'
  | 'knowledge'

export type RedactionStatus = 'clean' | 'redacted'

export interface TimeRange {
  from: string
  to: string
}

export interface Evidence {
  evidence_id: string
  type: EvidenceType
  source: string
  tool_name: string
  query?: { expr?: string; redacted?: boolean; [k: string]: unknown }
  time_range?: TimeRange
  summary: string
  // raw 不在响应中(存对象存储);raw_ref 可选保留兼容
  raw_ref?: string
  content_hash?: string
  freshness?: string
  redaction_status?: RedactionStatus
  created_at?: string
}

export type HypothesisStatus =
  | 'proposed'
  | 'supported'
  | 'rejected'
  | 'unresolved'

export interface Hypothesis {
  hypothesis_id: string
  rank: number
  statement: string
  component_ref?: ResourceRef
  confidence: number
  supporting_evidence_ids: string[]
  contradicting_evidence_ids: string[]
  missing_evidence?: string[]
  status: HypothesisStatus
}

export type DiagnosisStatus = 'resolved' | 'unresolved' | 'inconclusive'

// DiagnosisResult 内内联的精简假设(见 contracts.md 10.6)
export interface DiagnosisHypothesis {
  rank: number
  statement: string
  confidence: number
  supporting_evidence_ids: string[]
  contradicting_evidence_ids: string[]
}

export interface DiagnosisResult {
  incident_id: string
  status: DiagnosisStatus
  confirmed_facts: string[]
  hypotheses: DiagnosisHypothesis[]
  missing_information: string[]
  next_actions: string[]
  // 首版恒为 null(默认只读,无自动修复)
  remediation_proposal: null | Record<string, unknown>
}

// 工具调用记录(用于详情页“已调用工具”)
export interface ToolCall {
  tool_name: string
  status?: string
  scope?: Record<string, unknown>
  evidence_id?: string
  started_at?: string
  finished_at?: string
}

export interface Investigation {
  investigation_id: string
  incident_id: string
  incident_version?: number
  phase: InvestigationPhase
  budget: Budget
  usage: Usage
  // 以下为与 investigation 平级、在 getInvestigation 中合并进来的字段
  hypotheses?: Hypothesis[]
  diagnosis?: DiagnosisResult | null
  tool_calls?: ToolCall[]
  evidence?: Evidence[]
  feedback?: Feedback[]
  trigger_reason?: string
  created_at?: string
  updated_at?: string
}

// SSE 事件(GET /v1/investigations/{id}/events)
export interface InvestigationEvent {
  event_id?: string
  event_type: string
  phase?: InvestigationPhase
  payload?: Record<string, unknown>
  ts?: string
}

// 人工反馈动作
export type FeedbackAction = 'confirm' | 'correct' | 'close'

export interface FeedbackRequest {
  author: string
  action: FeedbackAction
  confirmed_root_cause?: string
  comment?: string
}

// 人工反馈历史记录(GET /v1/investigations/{id} 返回的 feedback 平级字段)
export interface Feedback {
  feedback_id: string
  author: string
  action: FeedbackAction
  confirmed_root_cause?: string | null
  comment?: string | null
  review_status?: string
  created_at?: string
}

// Signal 注入(POST /v1/signals)
export type SignalSource =
  | 'alertmanager'
  | 'kubernetes'
  | 'cicd'
  | 'itsm'
  | 'slo'
export type SignalType = 'alert' | 'change' | 'event' | 'resolved'

export interface SignalRequest {
  tenant_id: string
  cluster_id: string
  source: SignalSource
  signal_type: SignalType
  resource_ref: ResourceRef
  severity: string
  starts_at: string
  ends_at?: string | null
  labels?: Record<string, string>
}

export interface ApiError {
  error: { code: string; message: string }
}
