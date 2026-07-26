"""Golden-case contract validation + end-to-end offline replay (no Temporal)."""
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
        assert c.expected_top_causes  # contract: >=1 keyword


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
    assert ctx.signals  # signals carried through


async def test_end_to_end_replay_all_cases_hit_top1():
    cases = load_seed_cases()
    summary = await run_evaluation(cases)
    # MockProvider is scenario-aware -> every seed case should resolve + hit.
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
    # Every asserted root cause must reference a real-time evidence id.
    assert summary.detail["supported_conclusions"] >= 5
    assert summary.detail["unsupported_root_causes"] == 0


async def test_replay_is_deterministic():
    a = await run_evaluation(load_seed_cases())
    b = await run_evaluation(load_seed_cases())
    # Scoring (excluding wall-clock latency) is identical across runs.
    da = [r.model_dump(exclude={"first_diag_sec"}) for r in a.results]
    db = [r.model_dump(exclude={"first_diag_sec"}) for r in b.results]
    assert da == db


async def test_no_deep_rca_when_low_severity_no_change():
    # P4, no change, single service/namespace, unknown category -> triage-only.
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
    # Triage recommends deep RCA only for known scenarios; unknown + P4 -> no.
    r = summary.results[0]
    assert r.notes == "escalated"
