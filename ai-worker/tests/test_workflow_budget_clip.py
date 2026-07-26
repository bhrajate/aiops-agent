"""Fix #2 (per-round budget pre-clip) + fix #5 (max_rounds semantics).

Unit-tests the deterministic helper the workflow uses to bound a round's
tool-call fan-out *before* dispatching analyzers, plus the round-count semantics.
"""
from __future__ import annotations

from aiops_worker.contracts import AnalyzerSpec, AnalyzerType
from aiops_worker.workflow import _clip_analyzers_to_budget


def _specs():
    # kubernetes(2 tools) + metrics(1) + logs(1) + change(1) = 5 tool calls total
    return [
        AnalyzerSpec(
            analyzer=AnalyzerType.KUBERNETES,
            tools=["get_workload_state", "get_kubernetes_events"],
        ),
        AnalyzerSpec(analyzer=AnalyzerType.METRICS, tools=["query_metrics"]),
        AnalyzerSpec(analyzer=AnalyzerType.LOGS, tools=["search_logs"]),
        AnalyzerSpec(analyzer=AnalyzerType.CHANGE, tools=["list_recent_changes"]),
    ]


def _total_tools(specs) -> int:
    return sum(len(s.tools) for s in specs)


def test_no_clip_when_budget_ample():
    specs = _specs()
    out = _clip_analyzers_to_budget(specs, remaining=20)
    assert _total_tools(out) == 5
    assert len(out) == 4


def test_clip_truncates_straddling_analyzer():
    # remaining=3: kubernetes(2) fits, metrics(1) fits -> exactly 3, logs/change dropped.
    out = _clip_analyzers_to_budget(_specs(), remaining=3)
    assert _total_tools(out) == 3
    assert [s.analyzer for s in out] == [AnalyzerType.KUBERNETES, AnalyzerType.METRICS]


def test_clip_truncates_within_an_analyzer():
    # remaining=1: only ONE of kubernetes's two tools may run.
    out = _clip_analyzers_to_budget(_specs(), remaining=1)
    assert _total_tools(out) == 1
    assert out[0].analyzer == AnalyzerType.KUBERNETES
    assert out[0].tools == ["get_workload_state"]


def test_clip_zero_or_negative_budget_yields_nothing():
    assert _clip_analyzers_to_budget(_specs(), remaining=0) == []
    assert _clip_analyzers_to_budget(_specs(), remaining=-5) == []


def test_clip_ignores_non_allowlisted_tools():
    # A (malicious/hallucinated) tool outside the analyzer grant costs no budget
    # and is not counted -- mirrors the activity's allow-list enforcement.
    spec = AnalyzerSpec(
        analyzer=AnalyzerType.METRICS, tools=["query_metrics", "search_logs"]
    )
    out = _clip_analyzers_to_budget([spec], remaining=5)
    assert out[0].tools == ["query_metrics"]  # search_logs not allowed for metrics


def test_clip_is_deterministic():
    specs = _specs()
    a = _clip_analyzers_to_budget(specs, remaining=3)
    b = _clip_analyzers_to_budget(specs, remaining=3)
    assert [s.model_dump() for s in a] == [s.model_dump() for s in b]
    # Input specs are not mutated (model_copy used internally).
    assert _total_tools(specs) == 5
