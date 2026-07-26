"""Workflow-level determinism tests (fix #4).

Runs the real ``InvestigationWorkflow`` end-to-end on a time-skipping
``WorkflowEnvironment`` with in-memory activity stubs (no network, no DB), then
replays the recorded history through ``Replayer`` to guard against
non-deterministic regressions (architecture 7.2/7.4).

If the ephemeral test server binary cannot be provisioned (e.g. fully offline
CI with an empty cache), the tests skip rather than fail -- the rest of the
suite still exercises the workflow logic via the activity-level tests.
"""
from __future__ import annotations

import uuid

import pytest
from temporalio.contrib.pydantic import pydantic_data_converter
from temporalio.worker import Replayer, Worker

from aiops_worker.activities import InvestigationActivities
from aiops_worker.contracts import Budget, Phase, WorkflowInput
from aiops_worker.model_gateway.mock import MockProvider
from aiops_worker.workflow import InvestigationWorkflow
from tests.conftest import make_context

try:  # pragma: no cover - import guard
    from temporalio.testing import WorkflowEnvironment
except Exception:  # pragma: no cover
    WorkflowEnvironment = None  # type: ignore


TASK_QUEUE = "investigation-ai-test"


class _FakeClient:
    """In-memory internal-API stand-in shared by all activities in a test."""

    def __init__(self, context):
        self._context = context

    async def load_context(self, investigation_id):
        return self._context

    async def invoke_tool(self, investigation_id, incident_id, tool, arguments, scope=None):
        from aiops_worker.contracts import Evidence

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
            evidence_id=f"ev-{tool}",
            type=etype,
            source="fake",
            tool_name=tool,
            summary=f"{tool} 返回的模拟证据",
        )

    async def set_phase(self, investigation_id, phase):
        pass

    async def emit_event(self, investigation_id, event_type, payload, idempotency_key=""):
        pass

    async def put_hypotheses(self, investigation_id, hypotheses):
        pass

    async def put_diagnosis(self, investigation_id, diagnosis, phase):
        pass

    async def put_usage(self, investigation_id, usage):
        pass


def _make_activities(context) -> InvestigationActivities:
    acts = InvestigationActivities(MockProvider())
    fake = _FakeClient(context)
    # All activities go through self._client(url); route them to the fake.
    acts._client = lambda base_url: fake  # type: ignore[assignment]
    return acts


async def _start_env():
    if WorkflowEnvironment is None:
        pytest.skip("temporalio testing environment unavailable")
    try:
        return await WorkflowEnvironment.start_time_skipping(
            data_converter=pydantic_data_converter
        )
    except Exception as exc:  # pragma: no cover - offline CI without cached binary
        pytest.skip(f"cannot start time-skipping test server: {exc}")


async def _run_once(env, budget: Budget) -> tuple[list, str]:
    """Run one workflow to its waiting_feedback state, close it, return
    (history_events_json, workflow_id)."""
    ctx = make_context(fault_category="release_regression", severity="P2")
    acts = _make_activities(ctx)
    wf_id = f"wf-{uuid.uuid4()}"

    async with Worker(
        env.client,
        task_queue=TASK_QUEUE,
        workflows=[InvestigationWorkflow],
        activities=[
            acts.load_incident_context,
            acts.run_quick_triage,
            acts.evaluate_deep_rca_policy,
            acts.build_investigation_plan,
            acts.build_supplemental_plan,
            acts.retrieve_runbooks,
            acts.run_analyzer,
            acts.synthesize_hypotheses,
            acts.publish_diagnosis,
            acts.record_phase,
            acts.record_event,
            acts.record_usage,
        ],
    ):
        handle = await env.client.start_workflow(
            InvestigationWorkflow.run,
            WorkflowInput(
                investigation_id="i-replay",
                incident_id="inc-123",
                cluster_id="prod-cn-1",
                budget=budget,
            ),
            id=wf_id,
            task_queue=TASK_QUEUE,
        )

        # The workflow reaches concluded -> waiting_feedback and blocks; unblock
        # it with a HumanFeedback signal so it closes deterministically.
        async def _phase() -> str:
            return await handle.query("phase")

        # Wait until it is actually waiting for feedback.
        import asyncio

        for _ in range(200):
            if await _phase() == Phase.WAITING_FEEDBACK.value:
                break
            await asyncio.sleep(0.05)

        await handle.signal("HumanFeedback", {"action": "close"})
        result = await handle.result()

    history = await handle.fetch_history()
    return history, wf_id, result


@pytest.mark.asyncio
async def test_workflow_end_to_end_reaches_closed():
    env = await _start_env()
    try:
        history, _wf_id, result = await _run_once(env, Budget())
    finally:
        await env.shutdown()

    # Resolved conclusion -> concluded -> waiting_feedback -> closed.
    assert result.final_phase == Phase.CLOSED
    assert result.diagnosis_status is not None
    assert result.usage.rounds >= 1
    # Guardrail respected: never exceeded the tool-call budget.
    assert result.usage.tool_calls <= Budget().max_tool_calls
    assert history is not None


@pytest.mark.asyncio
async def test_tool_call_budget_enforced_within_round():
    # max_tool_calls=1 must be respected ex-ante: even though the plan wants
    # several analyzers, a single round cannot exceed the tool-call budget.
    env = await _start_env()
    try:
        _history, _wf_id, result = await _run_once(
            env, Budget(max_tool_calls=1, max_rounds=1)
        )
    finally:
        await env.shutdown()
    assert result.usage.tool_calls <= 1


@pytest.mark.asyncio
async def test_workflow_history_replays_deterministically():
    env = await _start_env()
    try:
        history, _wf_id, _result = await _run_once(env, Budget())
    finally:
        await env.shutdown()

    # Replay the recorded history against the CURRENT workflow code. A
    # non-deterministic change would raise here.
    replayer = Replayer(
        workflows=[InvestigationWorkflow],
        data_converter=pydantic_data_converter,
    )
    await replayer.replay_workflow(history)
