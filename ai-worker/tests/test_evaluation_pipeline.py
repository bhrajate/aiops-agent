"""黄金用例的契约校验,以及端到端离线重放(不依赖 Temporal)。"""
from __future__ import annotations

import pytest

from aiops_worker.evaluation.metrics import gate_report
from aiops_worker.evaluation.models import GoldenCase, SignalFixture
from aiops_worker.evaluation.runner import run_evaluation
from aiops_worker.evaluation.seed_cases import SEED_GOLDEN_CASES, load_seed_cases

FAULT_CLASSES = {
    "release_regression", "resource_saturation", "dependency_failure", "config_error"
}


def test_seed_cases_load_and_validate():
    cases = load_seed_cases()
    assert len(cases) == 5
    for c in cases:
        assert isinstance(c, GoldenCase)
        assert c.expected_top_causes  # 契约要求:至少 1 个关键词


def test_seed_cases_cover_four_fault_classes():
    seen = {c.fault_category for c in SEED_GOLDEN_CASES}
    assert FAULT_CLASSES.issubset(seen)


def test_golden_case_requires_expected_causes():
    with pytest.raises(ValueError):
        GoldenCase(
            case_id="bad", fault_category="release_regression",
            root_cause="x", expected_top_causes=[],
            signal_fixture=SignalFixture(incident={"incident_id": "i"}),
        )


def test_golden_case_strips_blank_expected_causes():
    c = GoldenCase(
        case_id="ok", fault_category="release_regression", root_cause="x",
        expected_top_causes=["  ", "连接池", ""],
        signal_fixture=SignalFixture(incident={"incident_id": "i"}),
    )
    assert c.expected_top_causes == ["连接池"]


def test_fixture_converts_to_context():
    c = load_seed_cases()[0]
    ctx = c.signal_fixture.to_context()
    assert ctx.incident.incident_id == "inc-release-001"
    assert ctx.signals  # 信号已完整传递下来


async def test_end_to_end_replay_all_cases_hit_top1():
    cases = load_seed_cases()
    summary = await run_evaluation(cases)
    # MockProvider 能识别场景 -> 每个 seed 用例都应得出结论并命中根因。
    assert summary.total_cases == 5
    assert summary.top1_hits == 5
    assert summary.top3_hits == 5
    assert summary.evidence_citation_rate == 1.0
    assert summary.hallucination_rate == 0.0
    assert summary.p95_first_diag_sec >= 0.0


async def test_end_to_end_gates_pass():
    summary = await run_evaluation(load_seed_cases())
    gates = gate_report(summary)
    assert all(gates.values()), gates


async def test_supported_conclusions_cite_realtime_evidence():
    summary = await run_evaluation(load_seed_cases())
    # 每一条被断言的根因都必须引用实时证据 id。
    assert summary.detail["supported_conclusions"] >= 5
    assert summary.detail["unsupported_root_causes"] == 0


async def test_replay_is_deterministic():
    a = await run_evaluation(load_seed_cases())
    b = await run_evaluation(load_seed_cases())
    # 除墙钟耗时外,多次运行的评分结果完全一致。
    da = [r.model_dump(exclude={"first_diag_sec"}) for r in a.results]
    db = [r.model_dump(exclude={"first_diag_sec"}) for r in b.results]
    assert da == db


async def test_no_deep_rca_when_low_severity_no_change():
    # P4、无变更、单服务单命名空间、类别未知 -> 只做初判。
    case = GoldenCase(
        case_id="gc-triage-only", fault_category="config_error",
        root_cause="x", expected_top_causes=["配置"],
        signal_fixture=SignalFixture(
            incident={
                "incident_id": "inc-low", "severity": "P4",
                "blast_radius": {"services": 1, "namespaces": 1},
                "change_refs": [],
            },
            signals=[{"signal_id": "s1", "source": "x", "signal_type": "y",
                      "labels": {}}],
        ),
    )
    summary = await run_evaluation([case])
    # 初判只对已知场景建议做深度 RCA;类别未知 + P4 -> 不建议。
    r = summary.results[0]
    assert r.notes == "escalated"


async def test_replay_exercises_all_four_reasoning_capabilities():
    """离线重放必须调用**四个**推理能力,而不是三个。

    此前 pipeline 只采集证据、从不调用 provider.analyze():于是
      - analyzer 退化不会让质量闸门变红(citation / hallucination 都算不到它);
      - synthesize 恒收到 analyzer_results=[],离线看到的入参形状与生产不同,
        分数因此不能代表生产会发布什么。

    这条用例守住"四个能力都被跑到"。若日后有人为了让评测更快而抽掉某个能力,
    闸门会照常全绿,只有这里会红。
    """
    from aiops_worker.evaluation.pipeline import OfflineReplayPipeline
    from aiops_worker.model_gateway.mock import MockProvider

    class _Spy(MockProvider):
        def __init__(self):
            super().__init__()
            self.calls = {"triage": 0, "plan": 0, "analyze": 0, "synthesize": 0}
            self.analyzer_results_seen: list[int] = []

        async def quick_triage(self, context):
            self.calls["triage"] += 1
            return await super().quick_triage(context)

        async def build_plan(self, context, triage, supplemental_from=None):
            self.calls["plan"] += 1
            return await super().build_plan(
                context, triage, supplemental_from=supplemental_from
            )

        async def analyze(self, context, spec, evidences):
            self.calls["analyze"] += 1
            # 分析器只应看到**自己**采到的证据,与工作流的 run_analyzer 一致。
            assert len(evidences) == 1, "分析器不该看到其他分析器的证据"
            return await super().analyze(context, spec, evidences)

        async def synthesize(self, context, evidences, analyzer_results, round_index):
            self.calls["synthesize"] += 1
            self.analyzer_results_seen.append(len(analyzer_results))
            return await super().synthesize(
                context, evidences, analyzer_results, round_index
            )

    spy = _Spy()
    pipe = OfflineReplayPipeline(provider=spy)
    for case in load_seed_cases():
        await pipe.replay(case.case_id, case.signal_fixture.to_context())

    for name, n in spy.calls.items():
        assert n > 0, f"离线重放从未调用 {name} 能力 —— 它不受质量闸门保护"
    # 综合器必须真的收到分析器结论(修复前恒为 0)。
    assert all(n > 0 for n in spy.analyzer_results_seen), spy.analyzer_results_seen
    # 每个用例至少跑一个分析器,所以 analyze 次数不少于用例数。
    assert spy.calls["analyze"] >= spy.calls["synthesize"]
