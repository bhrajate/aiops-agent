"""F2: runtime enforcement of the evidence-first invariant.

Previously ``has_supported_conclusion`` trusted the model's self-reported
status, so a hypothesis claiming ``supported`` while citing nothing (or citing
only a runbook) reached DiagnosisStatus.RESOLVED -- a "confirmed root cause"
with no evidence behind it. These tests pin the deterministic downgrade.
"""
from __future__ import annotations

import pytest

from aiops_worker.activities import InvestigationActivities, SynthesizeInput
from aiops_worker.contracts import (
    Evidence,
    Hypothesis,
    HypothesisStatus,
    Incident,
    IncidentContext,
    SynthesisResult,
)
from aiops_worker.policy import (
    UNGROUNDED_DOWNGRADE_REASON,
    build_diagnosis,
    enforce_evidence_grounding,
)


def _hyp(hid="hyp-1", status=HypothesisStatus.SUPPORTED, support=()):
    return Hypothesis(
        hypothesis_id=hid,
        rank=1,
        statement="根因是新版本回归",
        confidence=0.9,
        supporting_evidence_ids=list(support),
        status=status,
    )


def _realtime(eid="ev-1"):
    return Evidence(evidence_id=eid, type="metric", summary="错误率上升")


def _knowledge(eid="ev-kb"):
    return Evidence(evidence_id=eid, type="knowledge", summary="回滚手册")


# --------------------------------------------------------------------------


def test_supported_with_realtime_evidence_is_kept():
    syn = SynthesisResult(hypotheses=[_hyp(support=["ev-1"])])
    out, downgraded = enforce_evidence_grounding(syn, [_realtime("ev-1")])
    assert downgraded == []
    assert out.hypotheses[0].status == HypothesisStatus.SUPPORTED
    assert out.has_supported_conclusion


def test_supported_citing_nothing_is_downgraded():
    syn = SynthesisResult(hypotheses=[_hyp(support=[])])
    out, downgraded = enforce_evidence_grounding(syn, [_realtime()])
    assert downgraded == ["hyp-1"]
    h = out.hypotheses[0]
    assert h.status == HypothesisStatus.UNRESOLVED
    assert not out.has_supported_conclusion
    # The statement survives as a lead, and the gap is now explicit.
    assert h.statement == "根因是新版本回归"
    assert h.missing_evidence
    assert getattr(h, "downgrade_reason", None) == UNGROUNDED_DOWNGRADE_REASON


def test_supported_citing_only_runbook_is_downgraded():
    """Reference knowledge can seed a hypothesis but never prove one."""
    syn = SynthesisResult(hypotheses=[_hyp(support=["ev-kb"])])
    out, downgraded = enforce_evidence_grounding(syn, [_knowledge("ev-kb")])
    assert downgraded == ["hyp-1"]
    assert out.hypotheses[0].status == HypothesisStatus.UNRESOLVED


def test_supported_citing_nonexistent_evidence_is_downgraded():
    syn = SynthesisResult(hypotheses=[_hyp(support=["ev-does-not-exist"])])
    out, downgraded = enforce_evidence_grounding(syn, [_realtime("ev-1")])
    assert downgraded == ["hyp-1"]
    assert out.hypotheses[0].status == HypothesisStatus.UNRESOLVED


def test_mixed_citation_counts_as_grounded():
    syn = SynthesisResult(hypotheses=[_hyp(support=["ev-kb", "ev-1"])])
    out, downgraded = enforce_evidence_grounding(
        syn, [_knowledge("ev-kb"), _realtime("ev-1")]
    )
    assert downgraded == []
    assert out.hypotheses[0].status == HypothesisStatus.SUPPORTED


def test_non_supported_statuses_untouched():
    syn = SynthesisResult(
        hypotheses=[
            _hyp("h1", HypothesisStatus.PROPOSED, []),
            _hyp("h2", HypothesisStatus.REJECTED, []),
            _hyp("h3", HypothesisStatus.UNRESOLVED, []),
        ]
    )
    out, downgraded = enforce_evidence_grounding(syn, [])
    assert downgraded == []
    assert [h.status for h in out.hypotheses] == [
        HypothesisStatus.PROPOSED,
        HypothesisStatus.REJECTED,
        HypothesisStatus.UNRESOLVED,
    ]


def test_input_is_not_mutated():
    syn = SynthesisResult(hypotheses=[_hyp(support=[])])
    enforce_evidence_grounding(syn, [_realtime()])
    assert syn.hypotheses[0].status == HypothesisStatus.SUPPORTED


def test_downgrade_prevents_resolved_diagnosis():
    """The end-to-end consequence: no RESOLVED without real-time proof."""
    ctx = IncidentContext(incident=Incident(incident_id="inc-1"))
    ungrounded = SynthesisResult(hypotheses=[_hyp(support=[])])

    before = build_diagnosis("inc-1", ctx, ungrounded, escalated=False)
    assert before.status.value == "resolved"  # old behavior, for contrast

    after_syn, _ = enforce_evidence_grounding(ungrounded, [_realtime()])
    after = build_diagnosis("inc-1", ctx, after_syn, escalated=False)
    assert after.status.value == "inconclusive"
    assert after.confirmed_facts == []


# --------------------------------------------------------------------------
# the activity applies it before persisting
# --------------------------------------------------------------------------


class _UngroundedProvider:
    name = "ungrounded"

    async def synthesize(self, context, evidences, analyzer_results, round_index):
        from aiops_worker.contracts import ModelUsage

        return SynthesisResult(hypotheses=[_hyp(support=[])]), ModelUsage()


class _CapturingClient:
    def __init__(self):
        self.persisted = None

    async def put_hypotheses(self, inv_id, hypotheses):
        self.persisted = hypotheses


@pytest.mark.asyncio
async def test_activity_persists_only_grounded_status(monkeypatch):
    acts = InvestigationActivities(_UngroundedProvider())
    client = _CapturingClient()
    monkeypatch.setattr(acts, "_client", lambda _url: client)

    out = await acts.synthesize_hypotheses(
        SynthesizeInput(
            investigation_id="inv-1",
            control_internal_url="http://x",
            context=IncidentContext(incident=Incident(incident_id="inc-1")),
            evidences=[_realtime()],
        )
    )
    assert out.ungrounded_downgraded == ["hyp-1"]
    # The DB is the source of truth -- it must never hold the unproven claim.
    assert client.persisted[0].status == HypothesisStatus.UNRESOLVED
    assert not out.synthesis.has_supported_conclusion
