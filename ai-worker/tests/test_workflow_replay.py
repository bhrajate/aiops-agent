"""工作流层面的确定性测试(fix #4)。

在可跳过时间的 ``WorkflowEnvironment`` 上,以内存中的 activity 桩(不走网络、
不连数据库)端到端运行真实的 ``InvestigationWorkflow``,随后用 ``Replayer``
重放录制下来的历史,以防出现破坏确定性的回归(架构 7.2/7.4)。

若临时测试服务器的二进制无法就绪(例如缓存为空的完全离线 CI),这些测试会跳过而
不是失败 —— 套件中其余部分仍会通过 activity 层测试覆盖工作流逻辑。
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

try:  # pragma: no cover - 导入守卫
    from temporalio.testing import WorkflowEnvironment
except Exception:  # pragma: no cover
    WorkflowEnvironment = None  # type: ignore


TASK_QUEUE = "investigation-ai-test"


class _FakeClient:
    """内存版的内部 API 替身,供单个测试内所有 activity 共用。"""

    def __init__(self, context):
        self._context = context

    async def load_context(self, investigation_id):
        return self._context

    async def invoke_tool(self, investigation_id, incident_id, tool, arguments, scope=None):
        from aiops_worker.contracts import Evidence

        self.tool_calls_seen = getattr(self, "tool_calls_seen", [])
        self.tool_calls_seen.append(tool)
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
    # 所有 activity 都经由 self._client(url) 取客户端;这里统一指向 fake。
    acts._client = lambda base_url: fake  # type: ignore[assignment]
    return acts


async def _start_env():
    if WorkflowEnvironment is None:
        pytest.skip("temporalio testing environment unavailable")
    try:
        return await WorkflowEnvironment.start_time_skipping(
            data_converter=pydantic_data_converter
        )
    except Exception as exc:  # pragma: no cover - 无缓存二进制的离线 CI
        pytest.skip(f"cannot start time-skipping test server: {exc}")


async def _run_once(env, budget: Budget) -> tuple[list, str]:
    """把一条工作流跑到 waiting_feedback 状态,关闭它,并返回
    (history_events_json, workflow_id)。"""
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

        # 工作流会走到 concluded -> waiting_feedback 并阻塞;这里用 HumanFeedback
        # 信号解除阻塞,使其确定性地关闭。
        async def _phase() -> str:
            return await handle.query("phase")

        # 等到它确实进入等待反馈状态。
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

    # 得出明确结论 -> concluded -> waiting_feedback -> closed。
    assert result.final_phase == Phase.CLOSED
    assert result.diagnosis_status is not None
    assert result.usage.rounds >= 1
    # 护栏生效:全程未超出工具调用预算。
    assert result.usage.tool_calls <= Budget().max_tool_calls
    assert history is not None


@pytest.mark.asyncio
async def test_runbook_queries_counted_in_budget():
    # 当预算极小(max_tool_calls=1)时,runbook 检索现在也计入并受裁剪:
    # 总工具调用(含 runbook)不得超过预算。回归 round-2 审查发现的预算旁路。
    env = await _start_env()
    try:
        _history, _wf_id, result = await _run_once(
            env, Budget(max_tool_calls=1, max_rounds=1)
        )
    finally:
        await env.shutdown()
    assert result.usage.tool_calls <= 1


@pytest.mark.asyncio
async def test_tool_call_budget_enforced_within_round():
    # max_tool_calls=1 必须在事前就被遵守:即便计划想跑多个分析器,
    # 单轮也不能超出工具调用预算。
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

    # 用**当前**工作流代码重放录制下来的历史。若引入了破坏确定性的改动,
    # 这里会抛出异常。
    replayer = Replayer(
        workflows=[InvestigationWorkflow],
        data_converter=pydantic_data_converter,
    )
    await replayer.replay_workflow(history)
