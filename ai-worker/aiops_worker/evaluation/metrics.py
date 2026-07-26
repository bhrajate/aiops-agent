"""Quality-gate metric computation (architecture 18.1).

Pure functions, no I/O -> unit-testable with constructed inputs. The scorer
turns a :class:`ReplayOutcome` into an :class:`EvaluationResult`; the aggregator
turns a batch of results into an :class:`EvaluationRunSummary`.

Definitions (first version):
- Top-1 hit: the highest-ranked hypothesis statement contains any expected
  root-cause keyword (case-insensitive substring).
- Top-3 hit: any of the top-3 hypotheses does.
- Key conclusion = a hypothesis the pipeline marked SUPPORTED (i.e. an asserted
  root cause). evidence_citation_rate = fraction of supported conclusions that
  cite >=1 real-time evidence id (target 100%).
- hallucination_rate = fraction of supported conclusions with NO real-time
  evidence backing (an unsupported root-cause claim; target < 5%).
"""
from __future__ import annotations

import math

from ..contracts import HypothesisStatus
from .models import EvaluationResult, EvaluationRunSummary
from .pipeline import ReplayOutcome


def _match_keyword(statement: str, expected: list[str]) -> str | None:
    s = (statement or "").lower()
    for kw in expected:
        if kw.lower() in s:
            return kw
    return None


def score_outcome(
    case_id: str,
    fault_category: str,
    expected_top_causes: list[str],
    outcome: ReplayOutcome,
) -> EvaluationResult:
    """Score one replay against a golden case's expected causes."""
    syn = outcome.synthesis
    hyps = sorted(syn.hypotheses, key=lambda h: h.rank) if syn else []
    predicted = [h.statement for h in hyps]

    top1_hit = False
    top3_hit = False
    matched: str | None = None
    if hyps:
        m1 = _match_keyword(hyps[0].statement, expected_top_causes)
        if m1:
            top1_hit = True
            matched = m1
        for h in hyps[:3]:
            m = _match_keyword(h.statement, expected_top_causes)
            if m:
                top3_hit = True
                matched = matched or m
                break

    # Key conclusions = SUPPORTED hypotheses (asserted root causes).
    realtime_ids = outcome.realtime_evidence_ids
    supported = [h for h in hyps if h.status == HypothesisStatus.SUPPORTED]
    supported_with_ev = 0
    unsupported = 0
    for h in supported:
        cited = [e for e in h.supporting_evidence_ids if e in realtime_ids]
        if cited:
            supported_with_ev += 1
        else:
            unsupported += 1

    status = outcome.diagnosis.status.value if outcome.diagnosis else "unresolved"
    return EvaluationResult(
        case_id=case_id,
        fault_category=fault_category,
        diagnosis_status=status,
        predicted_causes=predicted,
        matched_keyword=matched,
        top1_hit=top1_hit,
        top3_hit=top3_hit,
        supported_conclusions=len(supported),
        supported_with_evidence=supported_with_ev,
        unsupported_root_causes=unsupported,
        first_diag_sec=round(outcome.first_diag_sec, 6),
        notes=("escalated" if outcome.escalated else ""),
    )


def percentile(values: list[float], pct: float) -> float:
    """Linear-interpolation percentile (``pct`` in [0,100]). Empty -> 0.0."""
    if not values:
        return 0.0
    if len(values) == 1:
        return float(values[0])
    ordered = sorted(values)
    rank = (pct / 100.0) * (len(ordered) - 1)
    lo = math.floor(rank)
    hi = math.ceil(rank)
    if lo == hi:
        return float(ordered[lo])
    frac = rank - lo
    return float(ordered[lo] + (ordered[hi] - ordered[lo]) * frac)


def aggregate(
    results: list[EvaluationResult],
    *,
    tenant_id: str = "default",
    model_version: str = "mock",
    prompt_version: str = "v1",
    policy_version: str = "v1",
) -> EvaluationRunSummary:
    """Aggregate per-case results into a run summary."""
    total = len(results)
    top1 = sum(1 for r in results if r.top1_hit)
    top3 = sum(1 for r in results if r.top3_hit)

    total_supported = sum(r.supported_conclusions for r in results)
    total_with_ev = sum(r.supported_with_evidence for r in results)
    total_unsupported = sum(r.unsupported_root_causes for r in results)

    # No asserted root cause anywhere -> vacuously 100% cited, 0% hallucination.
    citation_rate = (total_with_ev / total_supported) if total_supported else 1.0
    hallucination_rate = (
        total_unsupported / total_supported
    ) if total_supported else 0.0

    p95 = percentile([r.first_diag_sec for r in results], 95.0)

    by_category: dict[str, dict[str, int]] = {}
    for r in results:
        c = by_category.setdefault(
            r.fault_category, {"total": 0, "top1": 0, "top3": 0}
        )
        c["total"] += 1
        c["top1"] += int(r.top1_hit)
        c["top3"] += int(r.top3_hit)

    return EvaluationRunSummary(
        tenant_id=tenant_id,
        model_version=model_version,
        prompt_version=prompt_version,
        policy_version=policy_version,
        total_cases=total,
        top1_hits=top1,
        top3_hits=top3,
        evidence_citation_rate=round(citation_rate, 6),
        hallucination_rate=round(hallucination_rate, 6),
        p95_first_diag_sec=round(p95, 6),
        detail={
            "by_category": by_category,
            "supported_conclusions": total_supported,
            "supported_with_evidence": total_with_ev,
            "unsupported_root_causes": total_unsupported,
        },
        results=results,
    )


# First-version quality gates (architecture 18.1).
GATE_TOP3_RECALL = 0.70
GATE_EVIDENCE_CITATION = 1.0
GATE_HALLUCINATION_MAX = 0.05
GATE_P95_SEC = 300.0


def gate_report(summary: EvaluationRunSummary) -> dict[str, bool]:
    """Evaluate the summary against the first-version release gates."""
    return {
        "top3_recall>=0.70": summary.top3_rate >= GATE_TOP3_RECALL,
        "evidence_citation==1.0": summary.evidence_citation_rate >= GATE_EVIDENCE_CITATION,
        "hallucination<0.05": summary.hallucination_rate < GATE_HALLUCINATION_MAX,
        "p95_first_diag<300s": summary.p95_first_diag_sec < GATE_P95_SEC,
    }
