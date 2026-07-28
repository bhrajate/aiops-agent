"""Temporal Activity 集合。

所有非确定性工作都放在这里:内部 API 的 I/O 以及模型调用。工作流只负责编排,
本身不做任何 I/O(架构 7.2)。

这些 activity 都挂在 :class:`InvestigationActivities` 上,这样所选的
:class:`ModelProvider` 只需在 worker 启动时注入一次。内部 API 的 base URL 取自
每个调查的启动参数(``control_internal_url``),而不是全局配置,因此单个 worker
可以服务多个控制面。

入参与出参都是 Pydantic 模型,由 Temporal 的 Pydantic 数据转换器承载
(线上传输为 JSON -> 与 Go 控制面跨语言兼容)。
"""
from __future__ import annotations

from typing import Optional

from pydantic import BaseModel, ConfigDict, Field
from temporalio import activity

from .contracts import (
    ANALYZER_TOOLS,
    AnalyzerResult,
    AnalyzerSpec,
    Evidence,
    Hypothesis,
    IncidentContext,
    InvestigationPlan,
    ModelUsage,
    SynthesisResult,
    TriageResult,
    Usage,
    validate_plan,
)
from .internal_api import InternalAPIClient, ToolDenied
from .model_gateway.base import ModelProvider
from .policy import (
    build_diagnosis,
    enforce_evidence_grounding,
    evaluate_deep_rca_policy,
)

# ---------------------------------------------------------------------------
# Activity 的入参 / 出参信封
# ---------------------------------------------------------------------------


class ContextInput(BaseModel):
    investigation_id: str
    control_internal_url: str


class TriageInput(BaseModel):
    context: IncidentContext


class TriageOutput(BaseModel):
    triage: TriageResult
    usage: ModelUsage


class DeepRCAInput(BaseModel):
    context: IncidentContext
    triage: TriageResult


class PlanInput(BaseModel):
    context: IncidentContext
    triage: TriageResult
    supplemental_from: Optional[SynthesisResult] = None


class PlanOutput(BaseModel):
    plan: InvestigationPlan
    usage: ModelUsage


class RunbookInput(BaseModel):
    investigation_id: str
    incident_id: str
    control_internal_url: str
    queries: list[str] = Field(default_factory=list)


class RunAnalyzerInput(BaseModel):
    investigation_id: str
    incident_id: str
    control_internal_url: str
    context: IncidentContext
    spec: AnalyzerSpec
    scope: dict = Field(default_factory=dict)


class RunAnalyzerOutput(BaseModel):
    result: AnalyzerResult
    evidences: list[Evidence] = Field(default_factory=list)
    usage: ModelUsage
    tool_calls: int = 0
    denied_tools: list[str] = Field(default_factory=list)


class SynthesizeInput(BaseModel):
    investigation_id: str
    control_internal_url: str
    context: IncidentContext
    evidences: list[Evidence] = Field(default_factory=list)
    analyzer_results: list[AnalyzerResult] = Field(default_factory=list)
    round_index: int = 0


class SynthesizeOutput(BaseModel):
    synthesis: SynthesisResult
    usage: ModelUsage
    # 那些在没有实时证据的情况下自称 SUPPORTED、随后被确定性降级的假设
    # (证据优先不变式)。此处非空即是值得呈现在时间线上的模型质量信号。
    ungrounded_downgraded: list[str] = Field(default_factory=list)


class PublishDiagnosisInput(BaseModel):
    investigation_id: str
    incident_id: str
    control_internal_url: str
    context: IncidentContext
    synthesis: SynthesisResult
    escalated: bool = False
    phase: str = "concluded"


class PhaseInput(BaseModel):
    investigation_id: str
    control_internal_url: str
    phase: str


class EventInput(BaseModel):
    model_config = ConfigDict(extra="allow")
    investigation_id: str
    control_internal_url: str
    event_type: str
    payload: dict = Field(default_factory=dict)


class UsageInput(BaseModel):
    investigation_id: str
    control_internal_url: str
    usage: Usage


# ---------------------------------------------------------------------------
# Activity 具体实现
# ---------------------------------------------------------------------------


class InvestigationActivities:
    """把各 activity 聚在一起,以便在启动时注入 ModelProvider。"""

    def __init__(self, provider: ModelProvider, http_timeout_sec: float = 15.0, internal_token: str = ""):
        self._provider = provider
        self._timeout = http_timeout_sec
        self._internal_token = internal_token

    def _client(self, base_url: str) -> InternalAPIClient:
        return InternalAPIClient(base_url, timeout_sec=self._timeout, internal_token=self._internal_token)

    # -- context -------------------------------------------------------------

    @activity.defn
    async def load_incident_context(self, arg: ContextInput) -> IncidentContext:
        client = self._client(arg.control_internal_url)
        return await client.load_context(arg.investigation_id)

    # -- triage --------------------------------------------------------------

    @activity.defn
    async def run_quick_triage(self, arg: TriageInput) -> TriageOutput:
        triage, usage = await self._provider.quick_triage(arg.context)
        return TriageOutput(triage=triage, usage=usage)

    # -- 深度 RCA 策略(**确定性**判断,不交给 LLM) ---------------------------

    @activity.defn
    async def evaluate_deep_rca_policy(self, arg: DeepRCAInput) -> bool:
        return evaluate_deep_rca_policy(arg.context, arg.triage)

    # -- planner -------------------------------------------------------------

    @activity.defn
    async def build_investigation_plan(self, arg: PlanInput) -> PlanOutput:
        plan, usage = await self._provider.build_plan(arg.context, arg.triage)
        # 强制白名单:规划器只能挑选被许可的分析器 / 工具。
        plan = validate_plan(plan)
        return PlanOutput(plan=plan, usage=usage)

    @activity.defn
    async def build_supplemental_plan(self, arg: PlanInput) -> PlanOutput:
        plan, usage = await self._provider.build_plan(
            arg.context, arg.triage, supplemental_from=arg.supplemental_from
        )
        plan = validate_plan(plan)
        return PlanOutput(plan=plan, usage=usage)

    # -- runbook 检索(仅作参考知识) ----------------------------------------

    @activity.defn
    async def retrieve_runbooks(self, arg: RunbookInput) -> list[Evidence]:
        """通过内部 tool gateway 获取参考 runbook。

        返回的 Evidence 的 type=knowledge,即它只能用于启发假设 / 建议查询方向 ——
        绝不能用来**证明**根因(架构 12.2)。参考知识与实时证据的区分由下游按
        evidence type 强制执行。
        """
        client = self._client(arg.control_internal_url)
        out: list[Evidence] = []
        for q in arg.queries:
            try:
                ev = await client.invoke_tool(
                    investigation_id=arg.investigation_id,
                    incident_id=arg.incident_id,
                    tool="retrieve_runbook",
                    arguments={"query": q},
                )
                out.append(ev)
            except ToolDenied as exc:
                activity.logger.warning("runbook denied: %s", exc.reason)
        return out

    # -- analyzer ------------------------------------------------------------

    @activity.defn
    async def run_analyzer(self, arg: RunAnalyzerInput) -> RunAnalyzerOutput:
        """运行单个分析器:调用其白名单工具收集 Evidence,再让模型产出结构化分析。

        工具结果以**数据**形式进入模型(在 provider 内部做围栏包裹与净化),
        绝不作为指令(架构 14.2)。"""
        client = self._client(arg.control_internal_url)
        allowed = set(ANALYZER_TOOLS.get(arg.spec.analyzer, ()))
        evidences: list[Evidence] = []
        denied: list[str] = []
        tool_calls = 0

        for tool in arg.spec.tools:
            if tool not in allowed:
                # 纵深防御:跳过一切超出该分析器授权范围的工具。
                denied.append(tool)
                continue
            tool_calls += 1
            # 规划器传入的查询参数(已由 validate_plan 净化)让该分析器可以提出
            # **具体**的问题,而不必退回网关的通用默认值。但 scope 没有商量空间:
            # 无论如何网关都会重新注入 cluster/namespace。
            arguments: dict = {"analyzer": arg.spec.analyzer.value}
            arguments.update(arg.spec.args_for(tool))
            try:
                ev = await client.invoke_tool(
                    investigation_id=arg.investigation_id,
                    incident_id=arg.incident_id,
                    tool=tool,
                    arguments=arguments,
                    scope=arg.scope or None,
                )
                evidences.append(ev)
            except ToolDenied as exc:
                denied.append(exc.tool)

        result, usage = await self._provider.analyze(arg.context, arg.spec, evidences)
        return RunAnalyzerOutput(
            result=result,
            evidences=evidences,
            usage=usage,
            tool_calls=tool_calls,
            denied_tools=denied,
        )

    # -- synthesizer ---------------------------------------------------------

    @activity.defn
    async def synthesize_hypotheses(self, arg: SynthesizeInput) -> SynthesizeOutput:
        synthesis, usage = await self._provider.synthesize(
            arg.context, arg.evidences, arg.analyzer_results, arg.round_index
        )
        # 证据优先不变式,在**落库之前**强制执行,这样业务库(事实源)里绝不会
        # 存在没有实时证据支撑却被断言的根因。该过程是确定性的 —— 见 policy.py。
        synthesis, downgraded = enforce_evidence_grounding(synthesis, arg.evidences)
        if downgraded:
            activity.logger.warning(
                "downgraded %d ungrounded supported hypothes(es): %s",
                len(downgraded),
                ",".join(downgraded),
            )
        client = self._client(arg.control_internal_url)
        # 通过内部 API 持久化假设(整体替换)。
        await client.put_hypotheses(arg.investigation_id, synthesis.hypotheses)
        return SynthesizeOutput(
            synthesis=synthesis, usage=usage, ungrounded_downgraded=downgraded
        )

    # -- diagnosis -----------------------------------------------------------

    @activity.defn
    async def publish_diagnosis(self, arg: PublishDiagnosisInput) -> str:
        diagnosis = build_diagnosis(
            arg.incident_id, arg.context, arg.synthesis, arg.escalated
        )
        client = self._client(arg.control_internal_url)
        await client.put_diagnosis(arg.investigation_id, diagnosis, arg.phase)
        return diagnosis.status.value

    # -- 工作流使用的旁路写入 -------------------------------------------------

    @activity.defn
    async def record_phase(self, arg: PhaseInput) -> None:
        client = self._client(arg.control_internal_url)
        await client.set_phase(arg.investigation_id, arg.phase)

    @activity.defn
    async def record_event(self, arg: EventInput) -> None:
        client = self._client(arg.control_internal_url)
        # 幂等键由在 activity 重试之间保持**稳定**的 Temporal 标识推导而来
        # (workflow_id + activity_id)—— **不是**新生成的 uuid4,那样每次尝试都不同,
        # 会让去重失效。该键必须在 activity 内部生成(工作流代码必须保持确定性)。
        info = activity.info()
        idem = f"{info.workflow_id}:{info.activity_id}"
        await client.emit_event(
            arg.investigation_id, arg.event_type, arg.payload, idempotency_key=idem
        )

    @activity.defn
    async def record_usage(self, arg: UsageInput) -> None:
        client = self._client(arg.control_internal_url)
        await client.put_usage(arg.investigation_id, arg.usage)
