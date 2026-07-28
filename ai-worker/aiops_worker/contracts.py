"""以 Pydantic v2 模型表达的冻结数据契约。

对应 shared/schemas/contracts.md(Signal / Incident / Investigation /
Evidence / Hypothesis / DiagnosisResult),外加 worker 内部的 activity 载荷。
凡是跨越 Model Gateway 边界的取值都要按这些 schema 校验 —— 这里正是
「模型输出必须通过 schema 校验」这一提示注入防线的落地点(架构文档 14.2)。

时间统一用 ISO-8601 字符串承载,以保证 JSON 在 Go 控制面与 Python worker 之间
往返时保持稳定(使用 Temporal 默认的 JSON 转换器)。
"""
from __future__ import annotations

from enum import Enum
from typing import Any, Literal, Optional

from pydantic import BaseModel, ConfigDict, Field

# ---------------------------------------------------------------------------
# 枚举
# ---------------------------------------------------------------------------


class Phase(str, Enum):
    """调查阶段状态机(contracts.md / 架构 7.3)。"""

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
    """五种允许的分析器(架构 8.2)。集合固定 —— 规划器只能从中挑选,
    不能凭空造出新的分析器。"""

    KUBERNETES = "kubernetes"
    METRICS = "metrics"
    LOGS = "logs"
    TRACES = "traces"
    CHANGE = "change"


# 固定的工具白名单目录(contracts.md / 架构 9.1)。
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

# 各工具允许规划器传入的参数键。其余键会被 :func:`validate_plan` 丢弃 ——
# 规划器可以收窄**查什么**,但绝不能改变**在哪里查**(scope 始终由网关注入)。
#
# 未出现在此映射中的工具完全不接受规划器参数(K8s 系工具纯粹由 scope 驱动),
# 因此一旦传参即视为违反计划约束。
TOOL_ARG_KEYS: dict[str, tuple[str, ...]] = {
    "query_metrics": ("expr",),   # PromQL;网关在 AST 层注入 cluster/namespace
    "search_logs": ("query",),    # LogQL;网关注入 stream 选择器
    "get_traces": ("service",),   # service.name 标签;网关强制 namespace/cluster 标签
}

# 单个规划器参数值的长度上限。模型若吐出失控的表达式,不能就此变成失控的
# 后端查询串。
MAX_TOOL_ARG_LEN = 512

# 各分析器分别允许调用哪些工具。在 activity 中强制执行。
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
# 核心领域契约
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
    """不可变的证据记录。``type=knowledge`` 标记的是**参考**知识(runbook),
    它只能用于启发假设或查询,绝不能用来证明根因(架构 12.2)。
    其余类型都属于**实时**证据。"""

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
    """嵌入 DiagnosisResult 的精简版假设结构(contracts.md 10.6)。"""

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
    # 第一版是只读的:remediation_proposal **永远**为 null。
    remediation_proposal: None = None


# ---------------------------------------------------------------------------
# 有界执行:预算与用量(架构 8.4 / contracts.md)
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
    # 因缺少实时证据而被确定性降级的 SUPPORTED 假设数量。
    # 它不是预算维度,而是质量信号。
    ungrounded_downgrades: int = 0

    def add_model_usage(self, tokens: int, cost_usd: float) -> None:
        self.tokens += int(tokens)
        self.cost_usd += float(cost_usd)

    def budget_exceeded(self, budget: Budget) -> Optional[str]:
        """返回第一个耗尽的预算维度名,均未耗尽则返回 None。

        确定性实现:不含随机数,也不读时钟。``elapsed_sec`` 由工作流通过
        ``workflow.now()`` 提供,因此这里对重放是安全的。
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
# Model Gateway 用量信封(随每次模型调用一并返回)
# ---------------------------------------------------------------------------


class ModelUsage(BaseModel):
    """单次模型调用的 token / 成本核算(架构 12.1)。

    MockProvider 会用确定性的估算值填充这些字段,因此在没有真实模型的情况下,
    预算核算也能端到端跑通。
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
# 初判与计划(规划器被限制在允许的分析器/工具范围内)
# ---------------------------------------------------------------------------


class TriageResult(BaseModel):
    model_config = ConfigDict(extra="allow")
    summary: str
    suspected_fault_category: Optional[str] = None
    severity_assessment: str = "P3"
    recommend_deep_rca: bool = False
    rationale: str = ""


class AnalyzerSpec(BaseModel):
    """计划中的一个分析器步骤。``tools`` 必须是该分析器白名单工具的子集
    (由 :func:`validate_plan` 校验)。

    ``queries`` 允许规划器给工具调用**传参**(要评估哪条 PromQL、要 grep 哪条
    LogQL、要检索哪个服务的链路),而不必总是退回网关的通用默认值。键为工具名,
    值为受 :data:`TOOL_ARG_KEYS` 限制的参数映射。

    scope 的所有权仍在 Tool Gateway:它在 AST 层强制注入 cluster/namespace
    匹配器,并拒绝跨 scope 的匹配器,因此带参查询只能收窄问题,绝不会放大影响面
    (架构 9.2 / 14.2)。
    """

    model_config = ConfigDict(extra="allow")
    analyzer: AnalyzerType
    objective: str = ""
    tools: list[str] = Field(default_factory=list)
    queries: dict[str, dict[str, Any]] = Field(default_factory=dict)

    def args_for(self, tool: str) -> dict[str, Any]:
        """返回 ``tool`` 已校验的参数(未传参时为空)。"""
        return dict(self.queries.get(tool) or {})


class InvestigationPlan(BaseModel):
    model_config = ConfigDict(extra="allow")
    analyzers: list[AnalyzerSpec] = Field(default_factory=list)
    # 需要查阅的参考 runbook(retrieve_runbook)。仅作参考知识使用。
    runbook_queries: list[str] = Field(default_factory=list)


class AnalyzerResult(BaseModel):
    """单次分析器运行的结构化结果。分析器之间只交换结构化状态 ——
    绝不传递自由格式的对话文本(架构 8.2)。"""

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
# 工作流入参(docs/INTEGRATION.md 中的启动参数)与故障上下文
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
    """工作流最终返回的载荷。"""

    model_config = ConfigDict(extra="allow")
    investigation_id: str
    incident_id: str
    final_phase: Phase
    usage: Usage
    diagnosis_status: Optional[DiagnosisStatus] = None
    escalation_reason: Optional[str] = None


def validate_plan(plan: InvestigationPlan) -> InvestigationPlan:
    """强制规划器只能选用允许的分析器 / 工具 / 参数。

    用于防范被攻陷或产生幻觉的模型调用白名单之外的工具(架构 9.2 / 14.2)。
    违规时抛出 ValueError,调用方将其视为硬性策略失败。

    工具**参数**采取净化而非拒绝的策略:未知或超长的参数键会被丢弃,使该次调用
    退化为网关默认值,而不是让整份计划失败。但引用未知工具 —— 或引用该分析器无权
    使用的工具 —— 仍是硬错误,因为这说明模型是在试图越出授权,而不只是把参数写得
    过于具体。
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
    """只保留本 spec 中工具对应的参数映射,且键必须在白名单内、值必须是字符串、
    长度必须有界。其余一律丢弃。"""
    clean: dict[str, dict[str, Any]] = {}
    for tool, args in (queries or {}).items():
        if tool not in tools:
            continue  # 该分析器本轮并不运行这个工具,参数无效
        keys = TOOL_ARG_KEYS.get(tool)
        if not keys or not isinstance(args, dict):
            continue  # 该工具不接受规划器参数(例如各 K8s 工具)
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
