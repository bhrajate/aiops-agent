"""F8: the release quality gates must actually fail on regression.

The gates existed but were never wired into CI, and were never tested — an
untested guardrail is indistinguishable from no guardrail. Each gate is checked
independently so one failing gate can't be masked by another.
"""
from __future__ import annotations

from aiops_worker.evaluation.metrics import (
    GATE_EVIDENCE_CITATION,
    GATE_HALLUCINATION_MAX,
    GATE_P95_SEC,
    GATE_TOP3_RECALL,
    gate_report,
)
from aiops_worker.evaluation.models import EvaluationRunSummary

GATE_TOP3 = "top3_recall>=0.70"
GATE_CITE = "evidence_citation==1.0"
GATE_HALLU = "hallucination<0.05"
GATE_P95 = "p95_first_diag<300s"


def _summary(**over) -> EvaluationRunSummary:
    """A run that passes every gate; override one field to break one gate."""
    base = dict(
        total_cases=10,
        top1_hits=8,
        top3_hits=10,
        evidence_citation_rate=1.0,
        hallucination_rate=0.0,
        p95_first_diag_sec=1.0,
    )
    base.update(over)
    return EvaluationRunSummary(**base)


def test_healthy_run_passes_every_gate():
    report = gate_report(_summary())
    assert all(report.values()), f"健康运行不应有失败门槛: {report}"


def test_top3_recall_gate_fails_below_threshold():
    # 6/10 = 0.60 < 0.70
    report = gate_report(_summary(top3_hits=6))
    assert report[GATE_TOP3] is False
    # 其他门槛不受影响 —— 各门槛必须能独立失败,不被彼此掩盖。
    assert report[GATE_CITE] and report[GATE_HALLU] and report[GATE_P95]


def test_evidence_citation_gate_fails_below_100_percent():
    report = gate_report(_summary(evidence_citation_rate=0.99))
    assert report[GATE_CITE] is False, "引用率 99% 应失败:关键结论必须 100% 有实时证据"
    assert report[GATE_TOP3] and report[GATE_P95]


def test_hallucination_gate_fails_at_or_above_threshold():
    report = gate_report(_summary(hallucination_rate=GATE_HALLUCINATION_MAX))
    assert report[GATE_HALLU] is False, "幻觉率等于阈值应失败(严格小于)"
    report_ok = gate_report(_summary(hallucination_rate=GATE_HALLUCINATION_MAX - 0.001))
    assert report_ok[GATE_HALLU] is True


def test_p95_gate_fails_at_or_above_threshold():
    report = gate_report(_summary(p95_first_diag_sec=GATE_P95_SEC))
    assert report[GATE_P95] is False, "P95 等于阈值应失败(严格小于)"


def test_thresholds_are_at_documented_values():
    """门槛值是发布契约(architecture 18.1);改动应是显式决定,不是手滑。"""
    assert GATE_TOP3_RECALL == 0.70
    assert GATE_EVIDENCE_CITATION == 1.0
    assert GATE_HALLUCINATION_MAX == 0.05
    assert GATE_P95_SEC == 300.0


def test_recall_gate_catches_a_pipeline_that_concludes_nothing():
    """管线什么都不结论时,引用率/幻觉率是**空真**(1.0 / 0.0),
    只有召回门槛能抓住这种退化 —— 否则"全部弃权"会假装通过。"""
    report = gate_report(
        _summary(
            top1_hits=0,
            top3_hits=0,
            evidence_citation_rate=1.0,  # 无 supported 结论 → 空真
            hallucination_rate=0.0,
        )
    )
    assert report[GATE_TOP3] is False
    assert report[GATE_CITE] is True  # 说明单看这两项会漏
    assert report[GATE_HALLU] is True


def test_gate_report_keys_are_stable():
    """CI 依赖这些键名判定失败原因;改名等于悄悄改 CI 行为。"""
    assert set(gate_report(_summary())) == {GATE_TOP3, GATE_CITE, GATE_HALLU, GATE_P95}
