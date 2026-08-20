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
  // 三个维度语义不同(后端 store/alertgroups.go):
  //   services  受影响的服务数(Pod 已归约到所属工作负载)—— 驱动深度 RCA 闸门
  //   resources 受影响的具体资源数(Pod 级)
  //   groups    去重单元数(告警规则×资源),反映噪声量级
  services?: number
  resources?: number
  groups?: number
}

export interface Incident {
  incident_id: string
  tenant_id?: string
  cluster_id?: string
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
  signal_count?: number
  first_seen: string
  last_seen: string
  resolved_at?: string | null
  closed_at?: string | null
  // 拓扑关联的 incident(疑似同源)。后端刻意不合并 incident 而是链接:
  // 一条误判的拓扑边会把两次无关故障焊死,而拆分比合并难得多。
  relations?: IncidentRelation[]
  // 详情响应中后端附带的调查引用(与 incident 平级,在 getIncident 中合并进来)
  current_investigation_id?: string
  investigation_ids?: string[]
  investigations?: Array<{
    investigation_id: string
    phase?: InvestigationPhase
    trigger_reason?: string
  }>
  // 两层聚合模型:该 Incident 下的告警去重单元(哪些资源/规则在告警)
  alert_groups?: AlertGroup[]
}

// AlertGroup 是"去重单元":同资源+同规则的重复告警收敛为一条。
// 一个 Incident(相关性单元)可包含多个 AlertGroup。
export interface AlertGroup {
  group_id: string
  namespace: string
  resource: ResourceRef
  severity: Severity
  fault_category: string
  title: string
  status: 'open' | 'resolved'
  signal_count: number
  first_seen: string
  last_seen: string
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
  // 被确定性降级的"无实时证据支撑"结论数(evidence-first 不变量)。
  // >0 表示模型声称已确认但拿不出实时证据 —— 模型质量信号。
  ungrounded_downgrades?: number
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
  tenant_id?: string
  incident_id: string
  incident_version?: number
  workflow_id?: string
  run_id?: string
  phase: InvestigationPhase
  budget: Budget
  usage: Usage
  trigger_reason?: string
  triggered_by?: string
  // 模型与策略版本:结论可复现的前提。出了错误诊断要能回答
  // "当时用的哪个模型、哪版 prompt"。
  model_version?: string
  prompt_version?: string
  policy_version?: string
  // ⚠️ 时间字段是 started_at / ended_at,**不是** created_at / updated_at。
  // 后端 model.Investigation 只有前者(见 control-plane/internal/model/model.go);
  // 此前这里声明的是 created_at,读出来恒为 undefined —— 于是"开始于"显示为
  // Invalid Date、耗时算成 NaN,而 NaN 在界面上渲染成空白,看不出是错的。
  started_at: string
  ended_at?: string | null
  // 以下为与 investigation 平级、在 getInvestigation 中合并进来的字段
  hypotheses?: Hypothesis[]
  diagnosis?: DiagnosisResult | null
  tool_calls?: ToolCall[]
  evidence?: Evidence[]
  feedback?: Feedback[]
}

// SSE 事件(GET /v1/investigations/{id}/events)
export interface InvestigationEvent {
  event_id?: string
  event_type: string
  phase?: InvestigationPhase
  payload?: Record<string, unknown>
  ts?: string
}

// 人工反馈动作。
// reject 与 correct 的区别:两者都表示结论错了,但只有 correct 给出了
// 正确答案(标注真值)。后端只对 confirm/correct 提升评测用例。
export type FeedbackAction = 'confirm' | 'correct' | 'reject' | 'close'

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

// ── 值班总览(GET /v1/overview)────────────────────────────
// 与 control-plane/internal/api/overview.go 的 overviewResponse 对齐。

// {key, count} 通用形状。后端已定序(级别按 P1→P4,其余按计数降序),
// 前端**不要再排序** —— 重排会让级别分布把 P4 放到最前。
export interface CountPair {
  key: string
  count: number
}

export interface TrendBucket {
  ts: string
  new: number
  resolved: number
  investigations: number
}

export type QueueHealthStatus = 'ok' | 'lagging' | 'stuck'

export interface QueueHealth {
  outbox_pending: number
  outbox_dead: number
  oldest_pending_age_sec: number
  dead_letters: number
  health: QueueHealthStatus
}

export interface Overview {
  window_hours: number
  generated_at: string

  // 未闭环现状(不受时间窗约束)
  open_total: number
  open_p1: number
  open_p2: number
  unacknowledged: number
  active_investigations: number
  stalled_investigations: number

  by_severity: CountPair[]
  by_status: CountPair[]
  by_fault_category: CountPair[]
  by_phase: CountPair[]
  by_diagnosis: CountPair[]
  by_evidence_type: CountPair[] | null
  by_feedback: CountPair[] | null

  incidents_in_window: number
  investigations_started: number
  signals_aggregated: number
  cost_usd: number
  tokens: number
  tool_calls: number

  // null 表示样本不足。不要显示成 0 —— 0 会被读成"秒级解决"。
  mttr_seconds: number | null
  mttr_sample_size: number
  p95_investigation_seconds: number | null
  investigation_sample_size: number

  trend: TrendBucket[]

  // null 表示查询失败或无权查看。同理不要显示成 0。
  queue: QueueHealth | null
  golden_pending: number
}

// ── 调查队列(GET /v1/investigations)──────────────────────
// 列表行 = 调查本体 + 所属 incident 的展示字段(后端 JOIN 好)。
export interface InvestigationListItem extends Investigation {
  cluster_id: string
  namespace?: string
  incident_title?: string
  incident_severity?: Severity
  incident_status?: IncidentStatus
  fault_category?: string
  evidence_count: number
  hypothesis_count: number
}

// ── 审计日志(GET /v1/audit)───────────────────────────────
export type AuditResult = 'ok' | 'denied' | 'error' | 'allowed'

export interface AuditEntry {
  id: number
  tenant_id: string
  actor: string
  action: string
  target_type?: string
  target_id?: string
  scope?: Record<string, unknown>
  result?: AuditResult
  detail?: Record<string, unknown>
  created_at: string
}

export interface AuditActionCount {
  action: string
  result: string
  count: number
}

export interface AuditPage {
  entries: AuditEntry[]
  count: number
  // 0 表示没有更多(游标翻页,不用 OFFSET:审计表持续写入,
  // OFFSET 会在新记录插入时漏行,而这是问责依据)
  next_cursor: number
  action_counts: AuditActionCount[] | null
}

// ── 评测用例(GET /v1/golden-cases)────────────────────────
export type ReviewStatus = 'pending' | 'approved' | 'rejected'

export interface GoldenCase {
  case_id: string
  tenant_id: string
  incident_id?: string
  investigation_id?: string
  fault_category: string
  root_cause: string
  affected_component?: string
  expected_top_causes: string[]
  signal_fixture?: Record<string, unknown>
  review_status: ReviewStatus
  source: string
  promoted_by?: string
  reviewed_by?: string
  reviewed_at?: string
  review_note?: string
  created_at: string
}

// ── 知识库(GET /v1/knowledge)─────────────────────────────
export interface KnowledgeItem {
  knowledge_id: string
  kind: string
  title: string
  content: string
  applies_to?: Record<string, unknown>
  version?: string
  valid_until?: string
  created_at?: string
}

// ── Incident 关联(getIncident 的 relations 平级字段)──────
export interface IncidentRelation {
  incident_id: string
  related_incident_id: string
  relation: 'upstream' | 'downstream'
  via_edge?: Record<string, unknown>
  confidence: number
  created_at?: string
}

// ── 认证(SECURITY.md §1)────────────────────────────────
// JWT claims 最小集;namespaces 含 '*' 表示全部命名空间。
export interface UserClaims {
  sub: string
  email?: string
  roles: string[]
  clusters?: string[]
  namespaces?: string[]
}

export interface LoginRequest {
  username: string
  password: string
}

// POST /v1/auth/login 响应
export interface LoginResponse {
  token: string
  expires_in: number
  user: UserClaims
}
