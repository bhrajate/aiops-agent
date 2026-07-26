// Package model 定义与 shared/schemas/contracts.md 对齐的领域类型。
package model

import "time"

// ---- Signal ----

type ResourceRef struct {
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
}

type Signal struct {
	SignalID    string            `json:"signal_id"`
	TenantID    string            `json:"tenant_id"`
	ClusterID   string            `json:"cluster_id"`
	Source      string            `json:"source"`      // alertmanager | kubernetes | cicd | itsm | slo
	SignalType  string            `json:"signal_type"` // alert | change | event | resolved
	ResourceRef ResourceRef       `json:"resource_ref"`
	Severity    string            `json:"severity"`
	StartsAt    *time.Time        `json:"starts_at,omitempty"`
	EndsAt      *time.Time        `json:"ends_at,omitempty"`
	Labels      map[string]string `json:"labels"`
	PayloadRef  string            `json:"payload_ref,omitempty"`
	PayloadHash string            `json:"payload_hash,omitempty"`
	IncidentID  string            `json:"incident_id,omitempty"`
	ReceivedAt  time.Time         `json:"received_at"`
}

// ---- Incident ----

type Incident struct {
	IncidentID        string         `json:"incident_id"`
	TenantID          string         `json:"tenant_id"`
	ClusterID         string         `json:"cluster_id"`
	Version           int            `json:"version"`
	GroupingKey       string         `json:"grouping_key"`
	Status            string         `json:"status"`   // open | acknowledged | resolved | closed
	Severity          string         `json:"severity"` // P1..P4
	Title             string         `json:"title"`
	FaultCategory     string         `json:"fault_category,omitempty"`
	AffectedResources []ResourceRef  `json:"affected_resources"`
	BlastRadius       map[string]any `json:"blast_radius"`
	TopologyRefs      []any          `json:"topology_refs"`
	ChangeRefs        []any          `json:"change_refs"`
	SignalCount       int            `json:"signal_count"`
	FirstSeen         time.Time      `json:"first_seen"`
	LastSeen          time.Time      `json:"last_seen"`
	ResolvedAt        *time.Time     `json:"resolved_at,omitempty"`
	ClosedAt          *time.Time     `json:"closed_at,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

// ---- Investigation ----

type Budget struct {
	MaxDurationSec int     `json:"max_duration_sec"`
	MaxRounds      int     `json:"max_rounds"`
	MaxTokens      int     `json:"max_tokens"`
	MaxCostUSD     float64 `json:"max_cost_usd"`
	MaxToolCalls   int     `json:"max_tool_calls"`
}

func DefaultBudget() Budget {
	return Budget{MaxDurationSec: 300, MaxRounds: 3, MaxTokens: 200000, MaxCostUSD: 2.0, MaxToolCalls: 20}
}

type Usage struct {
	ElapsedSec float64 `json:"elapsed_sec"`
	Rounds     int     `json:"rounds"`
	Tokens     int     `json:"tokens"`
	CostUSD    float64 `json:"cost_usd"`
	ToolCalls  int     `json:"tool_calls"`
}

type Investigation struct {
	InvestigationID string           `json:"investigation_id"`
	TenantID        string           `json:"tenant_id"`
	IncidentID      string           `json:"incident_id"`
	IncidentVersion int              `json:"incident_version"`
	WorkflowID      string           `json:"workflow_id,omitempty"`
	RunID           string           `json:"run_id,omitempty"`
	Phase           string           `json:"phase"`
	TriggerReason   string           `json:"trigger_reason,omitempty"`
	TriggeredBy     string           `json:"triggered_by,omitempty"`
	Budget          Budget           `json:"budget"`
	Usage           Usage            `json:"usage"`
	ModelVersion    string           `json:"model_version,omitempty"`
	PromptVersion   string           `json:"prompt_version,omitempty"`
	PolicyVersion   string           `json:"policy_version,omitempty"`
	Diagnosis       *DiagnosisResult `json:"diagnosis,omitempty"`
	StartedAt       time.Time        `json:"started_at"`
	EndedAt         *time.Time       `json:"ended_at,omitempty"`
}

// ---- Evidence ----

type Evidence struct {
	EvidenceID      string         `json:"evidence_id"`
	TenantID        string         `json:"tenant_id"`
	InvestigationID string         `json:"investigation_id"`
	Type            string         `json:"type"` // metric|log|trace|kubernetes|change|knowledge
	Source          string         `json:"source"`
	ToolName        string         `json:"tool_name,omitempty"`
	Query           map[string]any `json:"query"`
	TimeRange       map[string]any `json:"time_range,omitempty"`
	Summary         string         `json:"summary"`
	RawRef          string         `json:"raw_ref,omitempty"`
	ContentHash     string         `json:"content_hash"`
	Freshness       string         `json:"freshness,omitempty"`
	RedactionStatus string         `json:"redaction_status"`
	CreatedAt       time.Time      `json:"created_at"`
}

// ---- Hypothesis ----

type Hypothesis struct {
	HypothesisID             string         `json:"hypothesis_id"`
	InvestigationID          string         `json:"investigation_id"`
	Rank                     int            `json:"rank"`
	Statement                string         `json:"statement"`
	ComponentRef             map[string]any `json:"component_ref,omitempty"`
	Confidence               float64        `json:"confidence"`
	SupportingEvidenceIDs    []string       `json:"supporting_evidence_ids"`
	ContradictingEvidenceIDs []string       `json:"contradicting_evidence_ids"`
	MissingEvidence          []string       `json:"missing_evidence"`
	Status                   string         `json:"status"` // proposed|supported|rejected|unresolved
}

// ---- DiagnosisResult (文档 10.6) ----

type DiagnosisHypothesis struct {
	Rank                     int      `json:"rank"`
	Statement                string   `json:"statement"`
	Confidence               float64  `json:"confidence"`
	SupportingEvidenceIDs    []string `json:"supporting_evidence_ids"`
	ContradictingEvidenceIDs []string `json:"contradicting_evidence_ids"`
}

type DiagnosisResult struct {
	IncidentID          string                `json:"incident_id"`
	Status              string                `json:"status"` // resolved|unresolved|inconclusive
	ConfirmedFacts      []string              `json:"confirmed_facts"`
	Hypotheses          []DiagnosisHypothesis `json:"hypotheses"`
	MissingInformation  []string              `json:"missing_information"`
	NextActions         []string              `json:"next_actions"`
	RemediationProposal any                   `json:"remediation_proposal"` // 首版恒为 null
}

// ---- 时间线事件 ----

type InvestigationEvent struct {
	ID              int64          `json:"id"`
	InvestigationID string         `json:"investigation_id"`
	Seq             int            `json:"seq"`
	EventType       string         `json:"event_type"`
	Payload         map[string]any `json:"payload"`
	CreatedAt       time.Time      `json:"created_at"`
}

// ---- 人工反馈 ----

type Feedback struct {
	FeedbackID         string    `json:"feedback_id"`
	InvestigationID    string    `json:"investigation_id"`
	Author             string    `json:"author"`
	Action             string    `json:"action"` // confirm|correct|reject|close
	ConfirmedRootCause string    `json:"confirmed_root_cause,omitempty"`
	Comment            string    `json:"comment,omitempty"`
	ReviewStatus       string    `json:"review_status"`
	CreatedAt          time.Time `json:"created_at"`
}
