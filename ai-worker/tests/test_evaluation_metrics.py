"""用已知的构造输入验证指标计算(不连数据库,不走网络)。"""
from __future__ import annotations

import pytest

from aiops_worker.contracts import (
    DiagnosisResult,
    DiagnosisStatus,
    Evidence,
    Hypothesis,
    HypothesisStatus,
    Incident,
    IncidentContext,
    SynthesisResult,
)
from aiops_worker.evaluation.metrics import (
    aggregate,
    gate_report,
    percentile,
    score_outcome,
)
from aiops_worker.evaluation.models import EvaluationResult
from aiops_worker.evaluation.pipeline import ReplayOutcome


def _ctx() -> IncidentContext:
    return IncidentContext(incident=Incident(incident_id="inc-x"))


def _outcome(hyps, evidences, status=DiagnosisStatus.RESOLVED, sec=1.0) -> ReplayOutcome:
    syn = SynthesisResult(hypotheses=hyps)
    diag = DiagnosisResult(incident_id="inc-x", status=status)
    return ReplayOutcome(
        context=_ctx(), triage=None, deep_rca=True, plan=None,
        evidences=evidences, synthesis=syn, diagnosis=diag,
        escalated=(status != DiagnosisStatus.RESOLVED), first_diag_sec=sec,
    )


def _hyp(rank, statement, status, support):
    return Hypothesis(
        hypothesis_id=f"h{rank}", rank=rank, statement=statement,
        confidence=0.8 if status == HypothesisStatus.SUPPORTED else 0.2,
        supporting_evidence_ids=support, status=status,
    )


def _ev(eid, etype="metric"):
    return Evidence(evidence_id=eid, type=etype, summary="s")


def test_top1_hit_when_rank1_matches():
    out = _outcome(
        [_hyp(1, "新版本连接池回归导致错误率飙升", HypothesisStatus.SUPPORTED, ["e1"])],
        [_ev("e1")],
    )
    r = score_outcome("c1", "release_regression", ["连接池", "版本"], out)
    assert r.top1_hit and r.top3_hit
    assert r.matched_keyword in {"连接池", "版本"}


def test_top3_hit_but_not_top1():
    hyps = [
        _hyp(1, "偶发流量尖峰导致抖动", HypothesisStatus.REJECTED, []),
        _hyp(2, "下游依赖超时导致级联失败", HypothesisStatus.SUPPORTED, ["e1"]),
    ]
    out = _outcome(hyps, [_ev("e1")])
    r = score_outcome("c2", "dependency_failure", ["依赖", "级联"], out)
    assert not r.top1_hit
    assert r.top3_hit


def test_no_hit_when_keywords_absent():
    out = _outcome(
        [_hyp(1, "完全不相关的结论", HypothesisStatus.SUPPORTED, ["e1"])], [_ev("e1")]
    )
    r = score_outcome("c3", "release_regression", ["连接池"], out)
    assert not r.top1_hit and not r.top3_hit


def test_citation_and_hallucination_counts():
    # h1 为 supported 且**有**实时证据;h2 为 supported 但**毫无**证据 -> 计入幻觉。
    hyps = [
        _hyp(1, "根因A 连接池", HypothesisStatus.SUPPORTED, ["e1"]),
        _hyp(2, "根因B 连接池", HypothesisStatus.SUPPORTED, ["missing"]),
    ]
    out = _outcome(hyps, [_ev("e1")])
    r = score_outcome("c4", "release_regression", ["连接池"], out)
    assert r.supported_conclusions == 2
    assert r.supported_with_evidence == 1
    assert r.unsupported_root_causes == 1


def test_reference_knowledge_does_not_count_as_citation():
    # supported 假设只引用了 knowledge 类证据 id -> 计入幻觉,
    # 因为参考知识永远不能**证明**根因(架构 12.2)。
    hyps = [_hyp(1, "连接池根因", HypothesisStatus.SUPPORTED, ["kb1"])]
    out = _outcome(hyps, [_ev("kb1", "knowledge")])
    r = score_outcome("c5", "release_regression", ["连接池"], out)
    assert r.supported_with_evidence == 0
    assert r.unsupported_root_causes == 1


def test_aggregate_rates_and_gates():
    results = [
        EvaluationResult(case_id="a", fault_category="release_regression",
                         diagnosis_status="resolved", top1_hit=True, top3_hit=True,
                         supported_conclusions=1, supported_with_evidence=1,
                         unsupported_root_causes=0, first_diag_sec=0.10),
        EvaluationResult(case_id="b", fault_category="dependency_failure",
                         diagnosis_status="resolved", top1_hit=False, top3_hit=True,
                         supported_conclusions=1, supported_with_evidence=1,
                         unsupported_root_causes=0, first_diag_sec=0.20),
    ]
    s = aggregate(results)
    assert s.total_cases == 2
    assert s.top1_hits == 1 and s.top3_hits == 2
    assert s.top1_rate == 0.5 and s.top3_rate == 1.0
    assert s.evidence_citation_rate == 1.0
    assert s.hallucination_rate == 0.0
    gates = gate_report(s)
    assert gates["top3_recall>=0.70"]
    assert gates["evidence_citation==1.0"]
    assert gates["hallucination<0.05"]


def test_aggregate_no_supported_is_not_hallucination():
    results = [
        EvaluationResult(case_id="a", fault_category="x", diagnosis_status="unresolved",
                         supported_conclusions=0, supported_with_evidence=0,
                         unsupported_root_causes=0, first_diag_sec=0.1),
    ]
    s = aggregate(results)
    # 空集情形:没有任何被断言的根因 -> 引用率记 100%,幻觉率记 0%。
    assert s.evidence_citation_rate == 1.0
    assert s.hallucination_rate == 0.0


def test_hallucination_gate_fails_over_threshold():
    results = []
    # 20 条有支撑、2 条无支撑 -> 幻觉率 10%,超过 5% 的闸门阈值。
    for i in range(18):
        results.append(EvaluationResult(
            case_id=f"ok{i}", fault_category="x", diagnosis_status="resolved",
            supported_conclusions=1, supported_with_evidence=1,
            unsupported_root_causes=0, first_diag_sec=0.1))
    for i in range(2):
        results.append(EvaluationResult(
            case_id=f"bad{i}", fault_category="x", diagnosis_status="resolved",
            supported_conclusions=1, supported_with_evidence=0,
            unsupported_root_causes=1, first_diag_sec=0.1))
    s = aggregate(results)
    assert abs(s.hallucination_rate - 0.1) < 1e-9
    assert not gate_report(s)["hallucination<0.05"]


@pytest.mark.parametrize("pct,expected", [(0, 1.0), (100, 5.0), (50, 3.0)])
def test_percentile(pct, expected):
    assert percentile([1.0, 2.0, 3.0, 4.0, 5.0], pct) == expected


def test_percentile_empty_and_single():
    assert percentile([], 95) == 0.0
    assert percentile([2.5], 95) == 2.5
