"""Activity wiring + bounded-execution escalation, with NO Temporal server and
NO network. The internal-API client is replaced by an in-memory fake."""
from __future__ import annotations

import pytest

from aiops_worker.activities import (
    ContextInput,
    DeepRCAInput,
    InvestigationActivities,
    PlanInput,
    PublishDiagnosisInput,
    RunAnalyzerInput,
    SynthesizeInput,
    TriageInput,
)
from aiops_worker.contracts import (
    Budget,
    DiagnosisStatus,
    Evidence,
    Usage,
)
from aiops_worker.model_gateway.mock import MockProvider
from tests.conftest import make_context, make_evidence


class FakeInternalClient:
    """In-memory stand-in for InternalAPIClient. Records writes, serves tools."""

    def __init__(self, context):
        self._context = context
        self.phases: list[str] = []
        self.events: list[tuple[str, dict]] = []
        self.hypotheses = None
        self.diagnosis = None
        self.tool_calls: list[str] = []

    async def load_context(self, investigation_id):
        return self._context

    async def invoke_tool(self, investigation_id, incident_id, tool, arguments, scope=None):
        self.tool_calls.append(tool)
        etype = {
            "query_metrics": "metric",
            "search_logs": "log",
            "get_traces": "trace",
            "inspect_dependencies": "trace",
            "get_workload_state": "kubernetes",
            "get_kubernetes_events": "kubernetes",
            "list_recent_changes": "change",
            "retrieve_runbook": "knowledge",
        }.get(tool, "metric")
        return Evidence(
            evidence_id=f"ev-{len(self.tool_calls)}",
            type=etype,
            source="fake",
            tool_name=tool,
            summary=f"{tool} 返回的模拟证据",
        )

    async def set_phase(self, investigation_id, phase):
        self.phases.append(phase)

    async def emit_event(self, investigation_id, event_type, payload, idempotency_key=""):
        self.events.append((event_type, payload, idempotency_key))

    async def put_hypotheses(self, investigation_id, hypotheses):
        self.hypotheses = hypotheses

    async def put_diagnosis(self, investigation_id, diagnosis, phase):
        self.diagnosis = (diagnosis, phase)

    async def put_usage(self, investigation_id, usage):
        pass


@pytest.fixture
def wired(monkeypatch):
    ctx = make_context(fault_category="release_regression", severity="P2")
    fake = FakeInternalClient(ctx)
    acts = InvestigationActivities(MockProvider())
    monkeypatch.setattr(acts, "_client", lambda base_url: fake)
    return acts, fake, ctx


async def test_full_activity_chain_produces_resolved_diagnosis(wired):
    acts, fake, ctx = wired
    base = "http://x"

    loaded = await acts.load_incident_context(
        ContextInput(investigation_id="i1", control_internal_url=base)
    )
    assert loaded.incident.incident_id == "inc-123"

    triage_out = await acts.run_quick_triage(TriageInput(context=ctx))
    assert triage_out.triage.suspected_fault_category == "release_regression"

    deep = await acts.evaluate_deep_rca_policy(
        DeepRCAInput(context=ctx, triage=triage_out.triage)
    )
    assert deep is True

    plan_out = await acts.build_investigation_plan(
        PlanInput(context=ctx, triage=triage_out.triage)
    )
    assert plan_out.plan.analyzers

    all_evidence = []
    analyzer_results = []
    total_tool_calls = 0
    for spec in plan_out.plan.analyzers:
        out = await acts.run_analyzer(
            RunAnalyzerInput(
                investigation_id="i1",
                incident_id="inc-123",
                control_internal_url=base,
                context=ctx,
                spec=spec,
            )
        )
        assert not out.denied_tools  # planner only picked allowed tools
        all_evidence.extend(out.evidences)
        analyzer_results.append(out.result)
        total_tool_calls += out.tool_calls
    assert total_tool_calls > 0

    syn_out = await acts.synthesize_hypotheses(
        SynthesizeInput(
            investigation_id="i1",
            control_internal_url=base,
            context=ctx,
            evidences=all_evidence,
            analyzer_results=analyzer_results,
        )
    )
    assert syn_out.synthesis.has_supported_conclusion
    assert fake.hypotheses is not None  # persisted via internal API

    status = await acts.publish_diagnosis(
        PublishDiagnosisInput(
            investigation_id="i1",
            incident_id="inc-123",
            control_internal_url=base,
            context=ctx,
            synthesis=syn_out.synthesis,
            escalated=False,
            phase="concluded",
        )
    )
    assert status == DiagnosisStatus.RESOLVED.value
    diagnosis, phase = fake.diagnosis
    assert phase == "concluded"
    assert diagnosis.remediation_proposal is None


async def test_run_analyzer_skips_tools_outside_grant(wired):
    acts, fake, ctx = wired
    from aiops_worker.contracts import AnalyzerSpec, AnalyzerType

    # A metrics analyzer that (maliciously) lists a non-granted tool.
    spec = AnalyzerSpec(
        analyzer=AnalyzerType.METRICS, tools=["query_metrics", "search_logs"]
    )
    out = await acts.run_analyzer(
        RunAnalyzerInput(
            investigation_id="i1",
            incident_id="inc-123",
            control_internal_url="http://x",
            context=ctx,
            spec=spec,
        )
    )
    assert "search_logs" in out.denied_tools
    assert "search_logs" not in fake.tool_calls
    assert "query_metrics" in fake.tool_calls


def test_budget_exhaustion_triggers_escalation_simulation():
    """Simulate the workflow's bounded loop: a tiny budget must stop the loop
    and escalate rather than looping forever (architecture 8.4)."""
    budget = Budget(max_rounds=1, max_tokens=10, max_cost_usd=0.00001, max_tool_calls=1)
    usage = Usage()
    escalation_reason = None
    rounds_run = 0

    for round_index in range(100):  # hard ceiling proves it must break early
        usage.rounds = round_index + 1
        stop = usage.budget_exceeded(budget)
        if stop is not None:
            escalation_reason = f"budget_exhausted:{stop}"
            break
        rounds_run += 1
        # Simulate one round consuming resources.
        usage.add_model_usage(tokens=50, cost_usd=0.01)
        usage.tool_calls += 2

    assert escalation_reason is not None
    assert escalation_reason.startswith("budget_exhausted:")
    assert rounds_run <= budget.max_rounds
