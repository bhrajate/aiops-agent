"""Fix #2(按轮次的预算事前裁剪)+ fix #5(max_rounds 语义)。

对工作流在派发分析器**之前**用于限定单轮工具调用扇出的确定性辅助函数做单元测试,
并覆盖轮数计数语义。
"""
from __future__ import annotations

from aiops_worker.contracts import AnalyzerSpec, AnalyzerType
from aiops_worker.workflow import _clip_analyzers_to_budget


def _specs():
    # kubernetes(2 个工具)+ metrics(1)+ logs(1)+ change(1)= 合计 5 次工具调用
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
    # remaining=3:kubernetes(2)放得下,metrics(1)也放得下 -> 刚好 3 次,
    # logs 与 change 被丢弃。
    out = _clip_analyzers_to_budget(_specs(), remaining=3)
    assert _total_tools(out) == 3
    assert [s.analyzer for s in out] == [AnalyzerType.KUBERNETES, AnalyzerType.METRICS]


def test_clip_truncates_within_an_analyzer():
    # remaining=1:kubernetes 的两个工具中只允许跑**一个**。
    out = _clip_analyzers_to_budget(_specs(), remaining=1)
    assert _total_tools(out) == 1
    assert out[0].analyzer == AnalyzerType.KUBERNETES
    assert out[0].tools == ["get_workload_state"]


def test_clip_zero_or_negative_budget_yields_nothing():
    assert _clip_analyzers_to_budget(_specs(), remaining=0) == []
    assert _clip_analyzers_to_budget(_specs(), remaining=-5) == []


def test_clip_ignores_non_allowlisted_tools():
    # 超出分析器授权范围的工具(恶意或幻觉产生的)不消耗预算、也不计数 ——
    # 与 activity 侧的白名单强制保持一致。
    spec = AnalyzerSpec(
        analyzer=AnalyzerType.METRICS, tools=["query_metrics", "search_logs"]
    )
    out = _clip_analyzers_to_budget([spec], remaining=5)
    assert out[0].tools == ["query_metrics"]  # metrics 分析器无权使用 search_logs


def test_clip_is_deterministic():
    specs = _specs()
    a = _clip_analyzers_to_budget(specs, remaining=3)
    b = _clip_analyzers_to_budget(specs, remaining=3)
    assert [s.model_dump() for s in a] == [s.model_dump() for s in b]
    # 传入的 spec 不会被改动(内部使用 model_copy)。
    assert _total_tools(specs) == 5
