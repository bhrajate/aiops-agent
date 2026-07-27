"""Offline RCA replay pipeline (no Temporal server).

Reproduces the essential control flow of ``InvestigationWorkflow`` -- triage ->
deep-RCA gate -> plan -> collect evidence -> synthesize -> publish diagnosis --
by calling the deterministic policy functions and a :class:`ModelProvider`
directly. This is what architecture 18.3 calls "历史事故离线回放".

Because the AI worker never touches real tools here, evidence is produced by a
deterministic *offline collector* that stands in for the Tool Gateway: for each
analyzer the plan selected, it emits one real-time Evidence record. Evidence
content is not what we score -- we score whether the reasoning pipeline reaches
the right root cause and cites evidence for it. Reference runbooks (if any) are
emitted as ``type=knowledge`` so the "reference vs real-time" boundary
(architecture 12.2) is exercised.
"""
from __future__ import annotations

import time
from dataclasses import dataclass, field
from typing import Optional

from ..contracts import (
    AnalyzerSpec,
    DiagnosisResult,
    Evidence,
    IncidentContext,
    InvestigationPlan,
    SynthesisResult,
    TriageResult,
    validate_plan,
)
from ..model_gateway.base import ModelProvider
from ..model_gateway.mock import MockProvider
from ..policy import (
    build_diagnosis,
    enforce_evidence_grounding,
    evaluate_deep_rca_policy,
)


@dataclass
class ReplayOutcome:
    """Everything one replay produced, for scoring."""

    context: IncidentContext
    triage: TriageResult
    deep_rca: bool
    plan: Optional[InvestigationPlan]
    evidences: list[Evidence] = field(default_factory=list)
    synthesis: Optional[SynthesisResult] = None
    diagnosis: Optional[DiagnosisResult] = None
    escalated: bool = False
    first_diag_sec: float = 0.0
    # How many asserted root causes the evidence-first guard had to reject.
    ungrounded_downgraded: int = 0

    @property
    def realtime_evidence_ids(self) -> set[str]:
        return {e.evidence_id for e in self.evidences if not e.is_reference_knowledge}


def _offline_collect(
    case_id: str, spec: AnalyzerSpec, seq: int
) -> Evidence:
    """Stand-in for the Tool Gateway: a deterministic real-time Evidence for
    one analyzer step. content_hash is a stable function of the ids."""
    ev_id = f"ev-{case_id}-{spec.analyzer.value}-{seq}"
    type_map = {
        "kubernetes": "kubernetes",
        "metrics": "metric",
        "logs": "log",
        "traces": "trace",
        "change": "change",
    }
    etype = type_map.get(spec.analyzer.value, "metric")
    return Evidence(
        evidence_id=ev_id,
        type=etype,  # type: ignore[arg-type]
        source=f"offline:{spec.analyzer.value}",
        tool_name=(spec.tools[0] if spec.tools else None),
        summary=f"[replay] {spec.analyzer.value} 采集到与 {spec.objective} 相关的实时观测。",
        content_hash=f"h-{ev_id}",
        redaction_status="clean",
    )


class OfflineReplayPipeline:
    """Runs a single golden-case replay end to end.

    Mirrors the workflow's bounded RCA loop: an initial plan+collect+synthesize
    round, and if inconclusive but actionable, one supplemental round. Kept
    deterministic (no randomness); the only clock read is a wall-clock latency
    measurement, which does not affect the diagnosis.
    """

    def __init__(self, provider: Optional[ModelProvider] = None, max_rounds: int = 2):
        self._provider = provider or MockProvider()
        self._max_rounds = max_rounds

    async def replay(self, case_id: str, context: IncidentContext) -> ReplayOutcome:
        start = time.perf_counter()

        triage, _ = await self._provider.quick_triage(context)
        deep = evaluate_deep_rca_policy(context, triage)

        outcome = ReplayOutcome(
            context=context, triage=triage, deep_rca=deep, plan=None
        )

        if not deep:
            # No deep RCA: triage-only. No diagnosis is published; treat as
            # escalated/inconclusive for scoring purposes.
            outcome.escalated = True
            outcome.first_diag_sec = time.perf_counter() - start
            return outcome

        plan, _ = await self._provider.build_plan(context, triage)
        plan = validate_plan(plan)
        outcome.plan = plan

        evidences: list[Evidence] = []
        synthesis: Optional[SynthesisResult] = None
        round_index = 0
        while True:
            # Runbooks (reference knowledge) then analyzers (real-time).
            for i, q in enumerate(plan.runbook_queries):
                evidences.append(
                    Evidence(
                        evidence_id=f"kb-{case_id}-{round_index}-{i}",
                        type="knowledge",
                        source="knowledge",
                        tool_name="retrieve_runbook",
                        summary=f"[reference] 参考手册: {q}",
                        content_hash=f"h-kb-{case_id}-{round_index}-{i}",
                    )
                )
            for j, spec in enumerate(plan.analyzers):
                evidences.append(_offline_collect(case_id, spec, round_index * 100 + j))

            synthesis, _ = await self._provider.synthesize(
                context, evidences, [], round_index
            )
            # Replay the runtime evidence-first guard so offline scores reflect
            # what production would actually publish (F2). Without this, an
            # evaluation run could pass a gate the runtime would refuse.
            synthesis, downgraded = enforce_evidence_grounding(synthesis, evidences)
            outcome.ungrounded_downgraded += len(downgraded)

            if synthesis.has_supported_conclusion:
                break
            if not synthesis.has_actionable_next_query:
                break
            round_index += 1
            if round_index >= self._max_rounds:
                break
            supp, _ = await self._provider.build_plan(
                context, triage, supplemental_from=synthesis
            )
            plan = validate_plan(supp)

        outcome.evidences = evidences
        outcome.synthesis = synthesis or SynthesisResult(hypotheses=[])
        outcome.escalated = not outcome.synthesis.has_supported_conclusion
        outcome.diagnosis = build_diagnosis(
            context.incident.incident_id,
            context,
            outcome.synthesis,
            outcome.escalated,
        )
        outcome.first_diag_sec = time.perf_counter() - start
        return outcome
