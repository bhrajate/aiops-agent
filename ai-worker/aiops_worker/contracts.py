"""Frozen data contracts as Pydantic v2 models.

Mirrors shared/schemas/contracts.md (Signal / Incident / Investigation /
Evidence / Hypothesis / DiagnosisResult) plus the worker-internal activity
payloads. Every value that crosses the Model Gateway boundary is validated
against these schemas -- this is the enforcement point for the
"model output must pass schema validation" prompt-injection defense
(architecture doc 14.2).

Times are carried as ISO-8601 strings so JSON round-trips stay stable across
the Go control plane and the Python worker (default Temporal JSON converter).
"""
from __future__ import annotations

from enum import Enum
from typing import Any, Literal, Optional

from pydantic import BaseModel, ConfigDict, Field

# ---------------------------------------------------------------------------
# Enumerations
# ---------------------------------------------------------------------------


class Phase(str, Enum):
    """Investigation phase state machine (contracts.md / architecture 7.3)."""

    QUEUED = "queued"
    TRIAGING = "triaging"
    TRIAGE_PUBLISHED = "triage_published"
    PLANNING = "planning"
    COLLECTING = "collecting"
    SYNTHESIZING = "synthesizing"
    CONCLUDED = "concluded"
    NEEDS_HUMAN = "needs_human"
    WAITING_FEEDBACK = "waiting_feedback"
    CLOSED = "closed"
    CANCELLED = "cancelled"


class AnalyzerType(str, Enum):
    """The five allowed analyzers (architecture 8.2). Fixed set -- the planner
    may only choose from these; it cannot invent new analyzers."""

    KUBERNETES = "kubernetes"
    METRICS = "metrics"
    LOGS = "logs"
    TRACES = "traces"
    CHANGE = "change"


# Fixed, allow-listed tool catalog (contracts.md / architecture 9.1).
ALLOWED_TOOLS: frozenset[str] = frozenset(
    {
        "get_workload_state",
        "get_kubernetes_events",
        "query_metrics",
        "search_logs",
        "get_traces",
        "list_recent_changes",
        "inspect_dependencies",
        "retrieve_runbook",
    }
)

# Which argument keys each tool accepts from the planner. Anything else is
# dropped by :func:`validate_plan` -- the planner may narrow *what* is asked,
# never *where* it is asked (scope stays gateway-injected).
#
# Tools absent from this map take no planner arguments at all (the K8s tools are
# purely scope-driven), so passing any is a plan violation.
TOOL_ARG_KEYS: dict[str, tuple[str, ...]] = {
    "query_metrics": ("expr",),   # PromQL; gateway injects cluster/namespace at AST level
    "search_logs": ("query",),    # LogQL; gateway injects stream selectors
    "get_traces": ("service",),   # service.name tag; gateway forces namespace/cluster tags
}

# Upper bound on a single planner-supplied argument value. A model that emits a
# runaway expression must not turn into a runaway backend query string.
MAX_TOOL_ARG_LEN = 512

# Which tools each analyzer is permitted to invoke. Enforced in activities.
ANALYZER_TOOLS: dict[AnalyzerType, tuple[str, ...]] = {
    AnalyzerType.KUBERNETES: ("get_workload_state", "get_kubernetes_events"),
    AnalyzerType.METRICS: ("query_metrics",),
    AnalyzerType.LOGS: ("search_logs",),
    AnalyzerType.TRACES: ("get_traces", "inspect_dependencies"),
    AnalyzerType.CHANGE: ("list_recent_changes",),
}


class HypothesisStatus(str, Enum):
    PROPOSED = "proposed"
    SUPPORTED = "supported"
    REJECTED = "rejected"
    UNRESOLVED = "unresolved"


class DiagnosisStatus(str, Enum):
    RESOLVED = "resolved"
    UNRESOLVED = "unresolved"
    INCONCLUSIVE = "inconclusive"


# ---------------------------------------------------------------------------
# Core domain contracts
# ---------------------------------------------------------------------------


class ResourceRef(BaseModel):
    model_config = ConfigDict(extra="allow")
    kind: Optional[str] = None
    name: Optional[str] = None
    namespace: Optional[str] = None
    uid: Optional[str] = None


class Signal(BaseModel):
    model_config = ConfigDict(extra="allow")
    signal_id: str
    tenant_id: str = "default"
    cluster_id: Optional[str] = None
    source: Optional[str] = None
    signal_type: Optional[str] = None
    resource_ref: Optional[ResourceRef] = None
    severity: Optional[str] = None
    starts_at: Optional[str] = None
    ends_at: Optional[str] = None
    labels: dict[str, Any] = Field(default_factory=dict)


class Incident(BaseModel):
    model_config = ConfigDict(extra="allow")
    incident_id: str
    version: int = 1
    grouping_key: Optional[str] = None
    status: str = "open"
    severity: str = "P3"  # P1 | P2 | P3 | P4
    fault_category: Optional[str] = None
    affected_resources: list[ResourceRef] = Field(default_factory=list)
    blast_radius: dict[str, Any] = Field(default_factory=dict)
    topology_refs: list[Any] = Field(default_factory=list)
    change_refs: list[Any] = Field(default_factory=list)
    first_seen: Optional[str] = None
    last_seen: Optional[str] = None


class Evidence(BaseModel):
    """Immutable evidence record. ``type=knowledge`` marks *reference* knowledge
    (runbooks) which may only seed hypotheses/queries, never prove a root cause
    (architecture 12.2). Everything else is *real-time* evidence."""

    model_config = ConfigDict(extra="allow")
    evidence_id: str
    type: Literal["metric", "log", "trace", "kubernetes", "change", "knowledge"]
    source: Optional[str] = None
    tool_name: Optional[str] = None
    query: dict[str, Any] = Field(default_factory=dict)
    time_range: dict[str, Any] = Field(default_factory=dict)
    summary: str = ""
    raw_ref: Optional[str] = None
    content_hash: Optional[str] = None
    freshness: Optional[str] = None
    redaction_status: Literal["clean", "redacted"] = "clean"

    @property
    def is_reference_knowledge(self) -> bool:
        return self.type == "knowledge"


class Hypothesis(BaseModel):
    model_config = ConfigDict(extra="allow")
    hypothesis_id: str
    rank: int
    statement: str
    component_ref: Optional[ResourceRef] = None
    confidence: float = Field(ge=0.0, le=1.0)
    supporting_evidence_ids: list[str] = Field(default_factory=list)
    contradicting_evidence_ids: list[str] = Field(default_factory=list)
    missing_evidence: list[str] = Field(default_factory=list)
    status: HypothesisStatus = HypothesisStatus.PROPOSED


class DiagnosisHypothesis(BaseModel):
    """Slim hypothesis form embedded in a DiagnosisResult (contracts.md 10.6)."""

    model_config = ConfigDict(extra="allow")
    rank: int
    statement: str
    confidence: float = Field(ge=0.0, le=1.0)
    supporting_evidence_ids: list[str] = Field(default_factory=list)
    contradicting_evidence_ids: list[str] = Field(default_factory=list)


class DiagnosisResult(BaseModel):
    model_config = ConfigDict(extra="allow")
    incident_id: str
    status: DiagnosisStatus
    confirmed_facts: list[str] = Field(default_factory=list)
    hypotheses: list[DiagnosisHypothesis] = Field(default_factory=list)
    missing_information: list[str] = Field(default_factory=list)
    next_actions: list[str] = Field(default_factory=list)
    # First version is read-only: remediation_proposal is ALWAYS null.
    remediation_proposal: None = None


# ---------------------------------------------------------------------------
# Bounded execution: budget + usage (architecture 8.4 / contracts.md)
# ---------------------------------------------------------------------------


class Budget(BaseModel):
    max_duration_sec: int = 300
    max_rounds: int = 3
    max_tokens: int = 200_000
    max_cost_usd: float = 2.0
    max_tool_calls: int = 20


class Usage(BaseModel):
    elapsed_sec: float = 0.0
    rounds: int = 0
    tokens: int = 0
    cost_usd: float = 0.0
    tool_calls: int = 0
    # Count of SUPPORTED hypotheses deterministically downgraded for lacking
    # real-time evidence. Not a budget dimension -- a quality signal.
    ungrounded_downgrades: int = 0

    def add_model_usage(self, tokens: int, cost_usd: float) -> None:
        self.tokens += int(tokens)
        self.cost_usd += float(cost_usd)

    def budget_exceeded(self, budget: Budget) -> Optional[str]:
        """Return the name of the first exhausted budget dimension, or None.

        Deterministic: contains no randomness/clock reads. ``elapsed_sec`` is
        supplied by the Workflow via ``workflow.now()`` so this stays
        replay-safe.
        """
        if self.elapsed_sec >= budget.max_duration_sec:
            return "max_duration_sec"
        if self.rounds >= budget.max_rounds:
            return "max_rounds"
        if self.tokens >= budget.max_tokens:
            return "max_tokens"
        if self.cost_usd >= budget.max_cost_usd:
            return "max_cost_usd"
        if self.tool_calls >= budget.max_tool_calls:
            return "max_tool_calls"
        return None


# ---------------------------------------------------------------------------
# Model Gateway usage envelope (returned alongside every model call)
# ---------------------------------------------------------------------------


class ModelUsage(BaseModel):
    """Token/cost accounting for a single model invocation (architecture 12.1).

    MockProvider fills these with deterministic estimates so budget accounting
    works end-to-end without a real model.
    """

    provider: str = "mock"
    model: str = "mock"
    input_tokens: int = 0
    output_tokens: int = 0
    cost_usd: float = 0.0

    @property
    def total_tokens(self) -> int:
        return self.input_tokens + self.output_tokens


# ---------------------------------------------------------------------------
# Triage + Plan (Planner constrained to allowed analyzers/tools)
# ---------------------------------------------------------------------------


class TriageResult(BaseModel):
    model_config = ConfigDict(extra="allow")
    summary: str
    suspected_fault_category: Optional[str] = None
    severity_assessment: str = "P3"
    recommend_deep_rca: bool = False
    rationale: str = ""


class AnalyzerSpec(BaseModel):
    """One analyzer step in a plan. ``tools`` must be a subset of that
    analyzer's allow-listed tools (validated in :func:`validate_plan`).

    ``queries`` lets the planner *parameterize* a tool call (which PromQL to
    evaluate, which LogQL to grep, which service to search traces for) instead
    of always falling back to the gateway's generic default. Keys are tool
    names; values are argument maps restricted to :data:`TOOL_ARG_KEYS`.
    The Tool Gateway still owns scope: it force-injects cluster/namespace
    matchers at the AST level and rejects cross-scope matchers, so a
    parameterized query can narrow the question but never widen the blast
    radius (architecture 9.2 / 14.2).
    """

    model_config = ConfigDict(extra="allow")
    analyzer: AnalyzerType
    objective: str = ""
    tools: list[str] = Field(default_factory=list)
    queries: dict[str, dict[str, Any]] = Field(default_factory=dict)

    def args_for(self, tool: str) -> dict[str, Any]:
        """Validated arguments for ``tool`` (empty when unparameterized)."""
        return dict(self.queries.get(tool) or {})


class InvestigationPlan(BaseModel):
    model_config = ConfigDict(extra="allow")
    analyzers: list[AnalyzerSpec] = Field(default_factory=list)
    # Reference runbooks to consult (retrieve_runbook). Reference knowledge only.
    runbook_queries: list[str] = Field(default_factory=list)


class AnalyzerResult(BaseModel):
    """Structured result from one analyzer run. Analyzers exchange only
    structured state -- never free-form chat (architecture 8.2)."""

    model_config = ConfigDict(extra="allow")
    analyzer: AnalyzerType
    findings: list[str] = Field(default_factory=list)
    evidence_ids: list[str] = Field(default_factory=list)


class SynthesisResult(BaseModel):
    model_config = ConfigDict(extra="allow")
    hypotheses: list[Hypothesis] = Field(default_factory=list)

    @property
    def has_supported_conclusion(self) -> bool:
        return any(h.status == HypothesisStatus.SUPPORTED for h in self.hypotheses)

    @property
    def has_actionable_next_query(self) -> bool:
        return any(h.missing_evidence for h in self.hypotheses)


# ---------------------------------------------------------------------------
# Workflow input (docs/INTEGRATION.md start params) + incident context
# ---------------------------------------------------------------------------


class WorkflowInput(BaseModel):
    model_config = ConfigDict(extra="allow")
    investigation_id: str
    incident_id: str
    incident_version: int = 1
    tenant_id: str = "default"
    cluster_id: Optional[str] = None
    budget: Budget = Field(default_factory=Budget)
    control_internal_url: str = "http://localhost:8090"


class IncidentContext(BaseModel):
    model_config = ConfigDict(extra="allow")
    incident: Incident
    signals: list[Signal] = Field(default_factory=list)
    topology: list[Any] = Field(default_factory=list)
    changes: list[Any] = Field(default_factory=list)


class WorkflowResult(BaseModel):
    """Final workflow return payload."""

    model_config = ConfigDict(extra="allow")
    investigation_id: str
    incident_id: str
    final_phase: Phase
    usage: Usage
    diagnosis_status: Optional[DiagnosisStatus] = None
    escalation_reason: Optional[str] = None


def validate_plan(plan: InvestigationPlan) -> InvestigationPlan:
    """Enforce that the planner only selected allowed analyzers/tools/arguments.

    Guards against a compromised/hallucinated model trying to invoke tools
    outside the allow list (architecture 9.2 / 14.2). Raises ValueError on
    violation; callers treat that as a hard policy failure.

    Tool *arguments* are sanitized rather than rejected: an unknown/oversized
    argument key is dropped so the call degrades to the gateway default instead
    of failing the whole plan. A reference to an unknown tool -- or a tool the
    analyzer may not use -- is still a hard error, because that signals the
    model is trying to escape its grant rather than merely over-specifying.
    """
    for spec in plan.analyzers:
        allowed = set(ANALYZER_TOOLS.get(spec.analyzer, ()))
        for tool in spec.tools:
            if tool not in ALLOWED_TOOLS:
                raise ValueError(f"plan references unknown tool: {tool!r}")
            if tool not in allowed:
                raise ValueError(
                    f"analyzer {spec.analyzer.value!r} may not use tool {tool!r}"
                )
        spec.queries = _sanitize_queries(spec.queries, spec.tools)
    return plan


def _sanitize_queries(
    queries: dict[str, dict[str, Any]], tools: list[str]
) -> dict[str, dict[str, Any]]:
    """Keep only argument maps for tools in this spec, with allow-listed keys,
    string values, and bounded length. Everything else is dropped."""
    clean: dict[str, dict[str, Any]] = {}
    for tool, args in (queries or {}).items():
        if tool not in tools:
            continue  # arguments for a tool this analyzer isn't running
        keys = TOOL_ARG_KEYS.get(tool)
        if not keys or not isinstance(args, dict):
            continue  # tool takes no planner arguments (e.g. K8s tools)
        kept: dict[str, Any] = {}
        for k in keys:
            v = args.get(k)
            if not isinstance(v, str):
                continue
            v = v.strip()
            if v and len(v) <= MAX_TOOL_ARG_LEN:
                kept[k] = v
        if kept:
            clean[tool] = kept
    return clean
