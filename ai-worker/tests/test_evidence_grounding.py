"""F2:证据优先不变式的运行时强制。

此前 ``has_supported_conclusion`` 信任模型自报的状态,于是一条声称 ``supported``
却什么都没引用(或只引用了 runbook)的假设也能走到 DiagnosisStatus.RESOLVED ——
形成一个背后毫无证据的「已确认根因」。这些测试固定住确定性降级的行为。
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
    # 结论陈述作为线索保留下来,同时缺口被显式标了出来。
    assert h.statement == "根因是新版本回归"
    assert h.missing_evidence
    assert getattr(h, "downgrade_reason", None) == UNGROUNDED_DOWNGRADE_REASON


def test_supported_citing_only_runbook_is_downgraded():
    """参考知识可以启发假设,但永远不能证明假设。"""
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
    """端到端的结果:没有实时证据就不会出现 RESOLVED。"""
    ctx = IncidentContext(incident=Incident(incident_id="inc-1"))
    ungrounded = SynthesisResult(hypotheses=[_hyp(support=[])])

    before = build_diagnosis("inc-1", ctx, ungrounded, escalated=False)
    assert before.status.value == "resolved"  # 旧行为,仅作对照

    after_syn, _ = enforce_evidence_grounding(ungrounded, [_realtime()])
    after = build_diagnosis("inc-1", ctx, after_syn, escalated=False)
    assert after.status.value == "inconclusive"
    assert after.confirmed_facts == []


# --------------------------------------------------------------------------
# activity 在落库之前就会应用该规则
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
    # 数据库是事实源 —— 它绝不能存下未经证实的断言。
    assert client.persisted[0].status == HypothesisStatus.UNRESOLVED
    assert not out.synthesis.has_supported_conclusion
