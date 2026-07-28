"""F1:由规划器传参的工具查询。

在此之前,所有可观测性工具都只跑网关的通用默认查询,因此计划中的 ``objective``
对实际采集到的数据毫无影响。这些测试固定住新行为,**以及**围绕它的各项护栏。
"""
from __future__ import annotations

import pytest

from aiops_worker.activities import InvestigationActivities, RunAnalyzerInput
from aiops_worker.contracts import (
    MAX_TOOL_ARG_LEN,
    AnalyzerSpec,
    AnalyzerType,
    Budget,
    Evidence,
    IncidentContext,
    Incident,
    InvestigationPlan,
    validate_plan,
)
from aiops_worker.model_gateway.mock import MockProvider
from aiops_worker.workflow import _clip_analyzers_to_budget


def _spec(tools, queries):
    return AnalyzerSpec(analyzer=AnalyzerType.METRICS, tools=tools, queries=queries)


# --------------------------------------------------------------------------
# validate_plan 的参数净化
# --------------------------------------------------------------------------


def test_allowed_query_arg_survives():
    plan = validate_plan(
        InvestigationPlan(analyzers=[_spec(["query_metrics"], {"query_metrics": {"expr": "up"}})])
    )
    assert plan.analyzers[0].args_for("query_metrics") == {"expr": "up"}


def test_unknown_arg_key_is_dropped_not_fatal():
    """未知参数键会退化为网关默认值,而不是让整份计划失败 ——
    模型的一个拼写错误不该中断一整轮证据采集。"""
    plan = validate_plan(
        InvestigationPlan(
            analyzers=[
                _spec(
                    ["query_metrics"],
                    {"query_metrics": {"expr": "up", "step": "1s", "namespace": "other"}},
                )
            ]
        )
    )
    assert plan.analyzers[0].args_for("query_metrics") == {"expr": "up"}


def test_args_for_tool_not_in_spec_are_dropped():
    plan = validate_plan(
        InvestigationPlan(
            analyzers=[_spec(["query_metrics"], {"search_logs": {"query": "{}"}})]
        )
    )
    assert plan.analyzers[0].queries == {}


def test_k8s_tools_accept_no_args():
    plan = validate_plan(
        InvestigationPlan(
            analyzers=[
                AnalyzerSpec(
                    analyzer=AnalyzerType.KUBERNETES,
                    tools=["get_workload_state"],
                    queries={"get_workload_state": {"expr": "anything"}},
                )
            ]
        )
    )
    assert plan.analyzers[0].queries == {}


def test_oversized_and_nonstring_args_dropped():
    plan = validate_plan(
        InvestigationPlan(
            analyzers=[
                _spec(["query_metrics"], {"query_metrics": {"expr": "x" * (MAX_TOOL_ARG_LEN + 1)}}),
                _spec(["query_metrics"], {"query_metrics": {"expr": 12345}}),
                _spec(["query_metrics"], {"query_metrics": {"expr": "   "}}),
            ]
        )
    )
    assert all(s.queries == {} for s in plan.analyzers)


# --------------------------------------------------------------------------
# 预算裁剪不得遗留孤立的查询参数
# --------------------------------------------------------------------------


def test_clipping_drops_args_of_clipped_tools():
    spec = AnalyzerSpec(
        analyzer=AnalyzerType.TRACES,
        tools=["get_traces", "inspect_dependencies"],
        queries={"get_traces": {"service": "auth"}},
    )
    # 只够放一个工具 -> inspect_dependencies 被裁掉。
    clipped = _clip_analyzers_to_budget([spec], remaining=1)
    assert clipped[0].tools == ["get_traces"]
    assert clipped[0].args_for("get_traces") == {"service": "auth"}

    # 仍只够放一个工具,但保留下来的那个本身不带参数。
    spec2 = AnalyzerSpec(
        analyzer=AnalyzerType.TRACES,
        tools=["inspect_dependencies", "get_traces"],
        queries={"get_traces": {"service": "auth"}},
    )
    clipped2 = _clip_analyzers_to_budget([spec2], remaining=1)
    assert clipped2[0].tools == ["inspect_dependencies"]
    assert clipped2[0].queries == {}


# --------------------------------------------------------------------------
# 参数确实传到了工具调用上
# --------------------------------------------------------------------------


class _RecordingClient:
    def __init__(self):
        self.calls: list[tuple[str, dict]] = []

    async def invoke_tool(self, *, investigation_id, incident_id, tool, arguments, scope=None):
        self.calls.append((tool, dict(arguments)))
        return Evidence(evidence_id=f"ev-{tool}", type="metric", summary="s")


@pytest.mark.asyncio
async def test_query_args_reach_invoke_tool(monkeypatch):
    acts = InvestigationActivities(MockProvider())
    client = _RecordingClient()
    monkeypatch.setattr(acts, "_client", lambda _url: client)

    ctx = IncidentContext(incident=Incident(incident_id="inc-1"))
    out = await acts.run_analyzer(
        RunAnalyzerInput(
            investigation_id="inv-1",
            incident_id="inc-1",
            control_internal_url="http://x",
            context=ctx,
            spec=_spec(["query_metrics"], {"query_metrics": {"expr": "sum(rate(x[5m]))"}}),
        )
    )
    assert out.tool_calls == 1
    tool, args = client.calls[0]
    assert tool == "query_metrics"
    # 分析器标记依然在,同时带上了规划器给出的表达式。
    assert args["analyzer"] == "metrics"
    assert args["expr"] == "sum(rate(x[5m]))"


# --------------------------------------------------------------------------
# mock 规划器也会走这条路径(使离线演示具有代表性)
# --------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_mock_plan_parameterizes_observability_tools():
    provider = MockProvider()
    ctx = IncidentContext(
        incident=Incident(incident_id="inc-1", fault_category="resource_saturation")
    )
    triage, _ = await provider.quick_triage(ctx)
    plan, _ = await provider.build_plan(ctx, triage)
    plan = validate_plan(plan)

    parameterized = {
        tool: args
        for spec in plan.analyzers
        for tool, args in spec.queries.items()
    }
    assert "query_metrics" in parameterized
    assert "throttled" in parameterized["query_metrics"]["expr"]
    assert "search_logs" in parameterized
    assert "oom" in parameterized["search_logs"]["query"].lower()


@pytest.mark.asyncio
async def test_mock_scenario_table_is_not_mutated_across_calls():
    """场景表是模块级共享状态;计划必须复制它而不是直接引用。"""
    provider = MockProvider()
    ctx = IncidentContext(
        incident=Incident(incident_id="inc-1", fault_category="resource_saturation")
    )
    triage, _ = await provider.quick_triage(ctx)
    plan1, _ = await provider.build_plan(ctx, triage)
    for spec in plan1.analyzers:
        spec.queries.clear()
    plan2, _ = await provider.build_plan(ctx, triage)
    assert any(spec.queries for spec in plan2.analyzers), "scenario table was mutated"


@pytest.mark.asyncio
async def test_budget_still_bounds_parameterized_plans():
    """传参不应改变工具调用的计数方式。"""
    provider = MockProvider()
    ctx = IncidentContext(
        incident=Incident(incident_id="inc-1", fault_category="release_regression")
    )
    triage, _ = await provider.quick_triage(ctx)
    plan, _ = await provider.build_plan(ctx, triage)
    plan = validate_plan(plan)
    budget = Budget(max_tool_calls=2)
    clipped = _clip_analyzers_to_budget(plan.analyzers, remaining=budget.max_tool_calls)
    assert sum(len(s.tools) for s in clipped) == 2
