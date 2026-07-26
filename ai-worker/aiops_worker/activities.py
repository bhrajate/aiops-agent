"""Temporal Activities.

All non-deterministic work lives here: internal-API I/O and model calls. The
Workflow orchestrates these but performs no I/O itself (architecture 7.2).

Activities are grouped on :class:`InvestigationActivities` so the chosen
:class:`ModelProvider` can be injected once at worker startup. The internal-API
base URL comes from each investigation's start params (``control_internal_url``)
rather than global config, so one worker can serve multiple control planes.

Input/output payloads are Pydantic models carried by the Temporal Pydantic data
converter (JSON on the wire -> cross-language compatible with the Go control
plane).
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
from .policy import build_diagnosis, evaluate_deep_rca_policy

# ---------------------------------------------------------------------------
# Activity input/output envelopes
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
# Activity implementations
# ---------------------------------------------------------------------------


class InvestigationActivities:
    """Bundles activities so a ModelProvider can be injected at startup."""

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

    # -- deep RCA policy (DETERMINISTic, not the LLM) ------------------------

    @activity.defn
    async def evaluate_deep_rca_policy(self, arg: DeepRCAInput) -> bool:
        return evaluate_deep_rca_policy(arg.context, arg.triage)

    # -- planner -------------------------------------------------------------

    @activity.defn
    async def build_investigation_plan(self, arg: PlanInput) -> PlanOutput:
        plan, usage = await self._provider.build_plan(arg.context, arg.triage)
        # Enforce allow-list: planner may only pick permitted analyzers/tools.
        plan = validate_plan(plan)
        return PlanOutput(plan=plan, usage=usage)

    @activity.defn
    async def build_supplemental_plan(self, arg: PlanInput) -> PlanOutput:
        plan, usage = await self._provider.build_plan(
            arg.context, arg.triage, supplemental_from=arg.supplemental_from
        )
        plan = validate_plan(plan)
        return PlanOutput(plan=plan, usage=usage)

    # -- runbook retrieval (reference knowledge only) ------------------------

    @activity.defn
    async def retrieve_runbooks(self, arg: RunbookInput) -> list[Evidence]:
        """Fetch reference runbooks via the internal tool gateway.

        Result Evidence has type=knowledge, i.e. it may only seed hypotheses /
        suggested queries -- it can never *prove* a root cause (architecture
        12.2). Reference vs real-time is enforced downstream by evidence type.
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
        """Run one analyzer: invoke its allow-listed tools to gather Evidence,
        then let the model produce a structured analysis.

        Tool results enter the model as DATA (fenced + sanitized inside the
        provider), never as instructions (architecture 14.2)."""
        client = self._client(arg.control_internal_url)
        allowed = set(ANALYZER_TOOLS.get(arg.spec.analyzer, ()))
        evidences: list[Evidence] = []
        denied: list[str] = []
        tool_calls = 0

        for tool in arg.spec.tools:
            if tool not in allowed:
                # Defense-in-depth: skip anything outside the analyzer's grant.
                denied.append(tool)
                continue
            tool_calls += 1
            try:
                ev = await client.invoke_tool(
                    investigation_id=arg.investigation_id,
                    incident_id=arg.incident_id,
                    tool=tool,
                    arguments={"analyzer": arg.spec.analyzer.value},
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
        # Persist hypotheses (full replacement) via internal API.
        client = self._client(arg.control_internal_url)
        await client.put_hypotheses(arg.investigation_id, synthesis.hypotheses)
        return SynthesizeOutput(synthesis=synthesis, usage=usage)

    # -- diagnosis -----------------------------------------------------------

    @activity.defn
    async def publish_diagnosis(self, arg: PublishDiagnosisInput) -> str:
        diagnosis = build_diagnosis(
            arg.incident_id, arg.context, arg.synthesis, arg.escalated
        )
        client = self._client(arg.control_internal_url)
        await client.put_diagnosis(arg.investigation_id, diagnosis, arg.phase)
        return diagnosis.status.value

    # -- side-channel writes used by the workflow ---------------------------

    @activity.defn
    async def record_phase(self, arg: PhaseInput) -> None:
        client = self._client(arg.control_internal_url)
        await client.set_phase(arg.investigation_id, arg.phase)

    @activity.defn
    async def record_event(self, arg: EventInput) -> None:
        client = self._client(arg.control_internal_url)
        # Idempotency key is derived from Temporal identifiers that stay STABLE
        # across activity retries (workflow_id + activity_id) -- NOT a fresh
        # uuid4, which would differ per attempt and defeat dedup. This must be
        # generated inside the activity (workflow code must stay deterministic).
        info = activity.info()
        idem = f"{info.workflow_id}:{info.activity_id}"
        await client.emit_event(
            arg.investigation_id, arg.event_type, arg.payload, idempotency_key=idem
        )

    @activity.defn
    async def record_usage(self, arg: UsageInput) -> None:
        client = self._client(arg.control_internal_url)
        await client.put_usage(arg.investigation_id, arg.usage)
