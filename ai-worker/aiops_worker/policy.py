"""确定性决策逻辑。这里的任何判断都**不会**交给 LLM
(架构 6.3:「这些判断不能交给 LLM」)。

全是无 I/O 的纯函数 -> 极易单元测试,且在工作流代码中调用时对重放安全。
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
    """确定性的深度 RCA 闸门(架构 6.3)。

    满足以下**任一**条件即进入深度 RCA:
      - 级别为 P1 或 P2;
      - 初判明确建议做深度 RCA;
      - 影响面正在扩大(受影响服务 >1 个或命名空间 >1 个);
      - 故障与近期变更高度相关。
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


# 记录在「声称 SUPPORTED 但缺乏证明」的假设上的原因说明。
UNGROUNDED_DOWNGRADE_REASON = "no_realtime_evidence"


def enforce_evidence_grounding(
    synthesis: SynthesisResult, evidences: list[Evidence]
) -> tuple[SynthesisResult, list[str]]:
    """把所有没有实时证据支撑的 SUPPORTED 假设降级(架构 12.2 / 18.1)。

    一条假设只有在引用了至少一条**真实存在的实时**证据时,才可以断言根因
    (``status=supported``)。参考知识(runbook,``type=knowledge``)可以启发假设,
    但永远不能证明假设,所以只有 runbook 支撑的结论不算结论。

    如果没有这道闸门,流水线就等于信任模型自报的状态:一条什么都没引用的
    ``supported`` 假设会一路通过 ``has_supported_conclusion`` -> CONCLUDED ->
    ``DiagnosisStatus.RESOLVED``,界面上则会显示一个背后零证据的「已确认根因」。
    离线评估闸门衡量的正是这件事(``evidence_citation_rate`` /
    ``hallucination_rate``);本函数是它在运行时的对应物,并且有意做成确定性的 ——
    这项检查绝不能交给被检查输出的那个模型自己来做。

    返回(可能被改写过的)综合结果,以及被降级的假设 id 列表,便于调用方发出
    审计事件。
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
            # 保留结论陈述(它可能仍是最佳线索),但撤下「已断言」这一层。
            # 记录缺什么,让下一轮有明确目标;若循环已无预算,则表现为 needs_human。
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
    """由综合出的假设组装 DiagnosisResult。

    假设状态 -> 诊断状态的映射是确定性的。第一版为只读,
    ``remediation_proposal`` **永远**为 null。
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
