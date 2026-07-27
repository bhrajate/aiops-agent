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
    Evidence,
    Hypothesis,
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


# Reason recorded on a hypothesis that claimed SUPPORTED without proof.
UNGROUNDED_DOWNGRADE_REASON = "no_realtime_evidence"


def enforce_evidence_grounding(
    synthesis: SynthesisResult, evidences: list[Evidence]
) -> tuple[SynthesisResult, list[str]]:
    """Downgrade any SUPPORTED hypothesis that is not grounded in real-time
    evidence (architecture 12.2 / 18.1).

    A hypothesis may only assert a root cause (``status=supported``) if it cites
    at least one piece of **real-time** evidence that actually exists. Reference
    knowledge (runbooks, ``type=knowledge``) can seed a hypothesis but can never
    prove one, so a conclusion backed only by a runbook is not a conclusion.

    Without this gate the pipeline trusts the model's self-reported status: a
    ``supported`` hypothesis citing nothing would flow through
    ``has_supported_conclusion`` -> CONCLUDED -> ``DiagnosisStatus.RESOLVED``,
    and the UI would show a confirmed root cause with zero evidence behind it.
    The offline evaluation gate measures exactly this
    (``evidence_citation_rate`` / ``hallucination_rate``); this is the runtime
    counterpart, and it is deterministic on purpose -- the check must not be
    delegated to the model whose output it is checking.

    Returns the (possibly rewritten) synthesis plus the ids of downgraded
    hypotheses so the caller can emit an audit event.
    """
    realtime_ids = {
        e.evidence_id for e in evidences if not e.is_reference_knowledge
    }
    downgraded: list[str] = []
    out: list[Hypothesis] = []
    for h in synthesis.hypotheses:
        if h.status == HypothesisStatus.SUPPORTED and not (
            set(h.supporting_evidence_ids) & realtime_ids
        ):
            downgraded.append(h.hypothesis_id)
            # Keep the statement (it may still be the best lead) but strip the
            # assertion. Record what is missing so the next round has a target;
            # if the loop is out of budget this surfaces as needs_human.
            missing = list(h.missing_evidence)
            want = "支持该结论的实时证据(指标/日志/追踪/K8s 状态)"
            if want not in missing:
                missing.append(want)
            out.append(
                h.model_copy(
                    update={
                        "status": HypothesisStatus.UNRESOLVED,
                        "missing_evidence": missing,
                        "downgrade_reason": UNGROUNDED_DOWNGRADE_REASON,
                    }
                )
            )
        else:
            out.append(h)
    if not downgraded:
        return synthesis, []
    return synthesis.model_copy(update={"hypotheses": out}), downgraded


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
