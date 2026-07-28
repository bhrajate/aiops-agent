"""确定性的深度 RCA 策略、契约校验,以及诊断结论组装。"""
from __future__ import annotations

import pytest
from pydantic import ValidationError

from aiops_worker.contracts import (
    AnalyzerSpec,
    AnalyzerType,
    Budget,
    DiagnosisResult,
    DiagnosisStatus,
    Hypothesis,
    HypothesisStatus,
    InvestigationPlan,
    SynthesisResult,
    Usage,
    validate_plan,
)
from aiops_worker.policy import build_diagnosis, evaluate_deep_rca_policy
from tests.conftest import make_context


# -- 深度 RCA 策略是确定性的(绝不交给 LLM) -------------------------------


def test_deep_rca_true_for_p1_p2():
    for sev in ("P1", "P2"):
        ctx = make_context(severity=sev, with_change=False)
        ctx.incident.blast_radius = {}
        # 即使初判没有给出建议也照样成立
        from aiops_worker.contracts import TriageResult

        triage = TriageResult(summary="x", recommend_deep_rca=False)
        assert evaluate_deep_rca_policy(ctx, triage) is True


def test_deep_rca_false_for_low_severity_no_signals():
    from aiops_worker.contracts import TriageResult

    ctx = make_context(severity="P4", with_change=False)
    ctx.incident.blast_radius = {"services": 1, "namespaces": 1}
    triage = TriageResult(summary="x", recommend_deep_rca=False)
    assert evaluate_deep_rca_policy(ctx, triage) is False


def test_deep_rca_true_on_recent_change():
    from aiops_worker.contracts import TriageResult

    ctx = make_context(severity="P3", with_change=True)
    ctx.incident.blast_radius = {"services": 1, "namespaces": 1}
    triage = TriageResult(summary="x", recommend_deep_rca=False)
    assert evaluate_deep_rca_policy(ctx, triage) is True


def test_deep_rca_true_on_expanding_blast_radius():
    from aiops_worker.contracts import TriageResult

    ctx = make_context(severity="P3", with_change=False)
    ctx.incident.blast_radius = {"services": 5, "namespaces": 1}
    triage = TriageResult(summary="x", recommend_deep_rca=False)
    assert evaluate_deep_rca_policy(ctx, triage) is True


def test_deep_rca_is_pure_and_repeatable():
    from aiops_worker.contracts import TriageResult

    ctx = make_context()
    triage = TriageResult(summary="x", recommend_deep_rca=False)
    r1 = evaluate_deep_rca_policy(ctx, triage)
    r2 = evaluate_deep_rca_policy(ctx, triage)
    assert r1 == r2


# -- Pydantic 契约校验 -------------------------------------------------------


def test_confidence_bounds_enforced():
    with pytest.raises(ValidationError):
        Hypothesis(hypothesis_id="h", rank=1, statement="s", confidence=1.5)


def test_diagnosis_remediation_always_null():
    d = DiagnosisResult(incident_id="inc-1", status=DiagnosisStatus.UNRESOLVED)
    assert d.remediation_proposal is None
    with pytest.raises(ValidationError):
        DiagnosisResult(
            incident_id="inc-1",
            status=DiagnosisStatus.RESOLVED,
            remediation_proposal={"action": "rollback"},
        )


def test_evidence_type_literal_enforced():
    from aiops_worker.contracts import Evidence

    with pytest.raises(ValidationError):
        Evidence(evidence_id="ev-1", type="bogus")


def test_validate_plan_rejects_tool_outside_analyzer_grant():
    plan = InvestigationPlan(
        analyzers=[
            AnalyzerSpec(analyzer=AnalyzerType.METRICS, tools=["search_logs"])
        ]
    )
    with pytest.raises(ValueError):
        validate_plan(plan)


def test_validate_plan_rejects_unknown_tool():
    plan = InvestigationPlan(
        analyzers=[AnalyzerSpec(analyzer=AnalyzerType.METRICS, tools=["rm_rf"])]
    )
    with pytest.raises(ValueError):
        validate_plan(plan)


# -- 诊断结论组装 -----------------------------------------------------------


def test_build_diagnosis_resolved_when_supported():
    ctx = make_context()
    syn = SynthesisResult(
        hypotheses=[
            Hypothesis(
                hypothesis_id="h1",
                rank=1,
                statement="root cause",
                confidence=0.8,
                supporting_evidence_ids=["ev-1"],
                status=HypothesisStatus.SUPPORTED,
            )
        ]
    )
    d = build_diagnosis("inc-123", ctx, syn, escalated=False)
    assert d.status == DiagnosisStatus.RESOLVED
    assert d.confirmed_facts == ["root cause"]
    assert d.remediation_proposal is None


def test_build_diagnosis_unresolved_when_escalated():
    ctx = make_context()
    syn = SynthesisResult(
        hypotheses=[
            Hypothesis(
                hypothesis_id="h1",
                rank=1,
                statement="maybe",
                confidence=0.3,
                missing_evidence=["more metrics"],
                status=HypothesisStatus.UNRESOLVED,
            )
        ]
    )
    d = build_diagnosis("inc-123", ctx, syn, escalated=True)
    assert d.status == DiagnosisStatus.UNRESOLVED
    assert "more metrics" in d.missing_information


# -- 预算核算 ---------------------------------------------------------------


def test_usage_budget_exceeded_dimensions():
    budget = Budget(max_rounds=3, max_tokens=1000, max_cost_usd=1.0, max_tool_calls=5)
    u = Usage()
    assert u.budget_exceeded(budget) is None
    u.rounds = 3
    assert u.budget_exceeded(budget) == "max_rounds"
    u2 = Usage(tokens=2000)
    assert u2.budget_exceeded(budget) == "max_tokens"
    u3 = Usage(cost_usd=1.5)
    assert u3.budget_exceeded(budget) == "max_cost_usd"
    u4 = Usage(tool_calls=10)
    assert u4.budget_exceeded(budget) == "max_tool_calls"
    u5 = Usage(elapsed_sec=999)
    assert u5.budget_exceeded(Budget(max_duration_sec=300)) == "max_duration_sec"
