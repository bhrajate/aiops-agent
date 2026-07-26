"""Deterministic decision logic. NONE of this is delegated to an LLM
(architecture 6.3: "这些判断不能交给 LLM").

Pure functions with no I/O -> trivially unit-testable and replay-safe if
called from workflow code.
"""
from __future__ import annotations

from .contracts import (
    DiagnosisHypothesis,
    DiagnosisResult,
    DiagnosisStatus,
    HypothesisStatus,
    IncidentContext,
    SynthesisResult,
    TriageResult,
)


def evaluate_deep_rca_policy(context: IncidentContext, triage: TriageResult) -> bool:
    """Deterministic deep-RCA gate (architecture 6.3).

    Deep RCA proceeds if ANY of:
      - severity is P1 or P2;
      - triage explicitly recommends deep RCA;
      - blast radius is expanding (>1 service or >1 namespace affected);
      - the incident is highly correlated with a recent change.
    """
    incident = context.incident
    if incident.severity in {"P1", "P2"}:
        return True
    if triage.recommend_deep_rca:
        return True

    blast = incident.blast_radius or {}
    if int(blast.get("services", 0) or 0) > 1:
        return True
    if int(blast.get("namespaces", 0) or 0) > 1:
        return True

    if incident.change_refs or context.changes:
        return True
    return False


def build_diagnosis(
    incident_id: str,
    context: IncidentContext,
    synthesis: SynthesisResult,
    escalated: bool,
) -> DiagnosisResult:
    """Assemble a DiagnosisResult from synthesized hypotheses.

    Deterministic mapping of hypothesis status -> diagnosis status.
    ``remediation_proposal`` is ALWAYS null in the first version (read-only).
    """
    hyps = sorted(synthesis.hypotheses, key=lambda h: h.rank)

    if escalated:
        status = DiagnosisStatus.UNRESOLVED
    elif any(h.status == HypothesisStatus.SUPPORTED for h in hyps):
        status = DiagnosisStatus.RESOLVED
    elif hyps:
        status = DiagnosisStatus.INCONCLUSIVE
    else:
        status = DiagnosisStatus.UNRESOLVED

    confirmed_facts: list[str] = [
        h.statement for h in hyps if h.status == HypothesisStatus.SUPPORTED
    ]
    missing: list[str] = []
    for h in hyps:
        for m in h.missing_evidence:
            if m not in missing:
                missing.append(m)

    diag_hyps = [
        DiagnosisHypothesis(
            rank=h.rank,
            statement=h.statement,
            confidence=h.confidence,
            supporting_evidence_ids=h.supporting_evidence_ids,
            contradicting_evidence_ids=h.contradicting_evidence_ids,
        )
        for h in hyps
    ]

    next_actions: list[str] = []
    if missing:
        next_actions.append("补充采集缺失证据后重新评估: " + "; ".join(missing))
    if status == DiagnosisStatus.UNRESOLVED:
        next_actions.append("升级人工介入,确认根因与影响面")

    return DiagnosisResult(
        incident_id=incident_id,
        status=status,
        confirmed_facts=confirmed_facts,
        hypotheses=diag_hyps,
        missing_information=missing,
        next_actions=next_actions,
        remediation_proposal=None,
    )
