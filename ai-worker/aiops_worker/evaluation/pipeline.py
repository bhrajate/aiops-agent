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
    AnalyzerResult,
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
    """一次重放产出的全部内容,用于后续打分。"""

    context: IncidentContext
    triage: TriageResult
    deep_rca: bool
    plan: Optional[InvestigationPlan]
    evidences: list[Evidence] = field(default_factory=list)
    synthesis: Optional[SynthesisResult] = None
    diagnosis: Optional[DiagnosisResult] = None
    escalated: bool = False
    first_diag_sec: float = 0.0
    # 证据优先守卫不得不驳回的根因断言数量。
    ungrounded_downgraded: int = 0

    @property
    def realtime_evidence_ids(self) -> set[str]:
        return {e.evidence_id for e in self.evidences if not e.is_reference_knowledge}


def _offline_collect(
    case_id: str, spec: AnalyzerSpec, seq: int
) -> Evidence:
    """Tool Gateway 的替身:为单个分析器步骤产出确定性的实时 Evidence。
    content_hash 是由各 id 计算出的稳定值。"""
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
    """端到端跑完单个黄金用例的重放。

    与工作流的有界 RCA 循环保持一致:先做一轮「规划 + 采集 + 分析 + 综合」,
    若结论不明确但仍有可执行的下一步,则再跑一轮补充采集。整体保持确定性
    (不含随机数);唯一的时钟读取用于测量墙钟耗时,不影响诊断结果。

    ## 它**测不到**什么(重要)

    本管线复现的是推理链,不是 Temporal 编排。以下工作流行为在离线完全不存在,
    因此**质量闸门全绿不代表它们是对的**:

    - **预算护栏**:没有 ``Budget``,没有 ``max_tool_calls`` 事前裁剪
      (``_clip_analyzers_to_budget``),没有 token / 成本 / 时长上限。
      预算相关的改动必须靠 ``test_workflow_budget_clip.py`` 与
      ``test_workflow_replay.py`` 验证。
    - **轮次语义**:这里 ``max_rounds`` 默认 2,工作流的 ``Budget.max_rounds``
      默认 3;工作流还有 ``round_index`` 自增时机、``escalation_reason`` 分支。
    - **升级与阶段迁移**:``needs_human`` / ``waiting_feedback`` / 人工反馈超时
      全部没有。
    - **幂等与重放**:activity 重试、心跳、``continue_as_new`` 一概不涉及。

    这不是缺陷,是刻意的取舍:离线要快且确定,不该起 Temporal。但**必须写明** ——
    第七轮的跨轮证据缺陷(第 2 轮综合器看不到第 1 轮证据,正确结论被降级)在
    离线评测里完全不可见,而当时没人意识到这个盲区,因为闸门是全绿的。

    已知的盲区不危险,被误认为有覆盖的盲区才危险。
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
            # 不做深度 RCA:仅初判。不发布诊断结论;打分时按「已升级 / 未定论」处理。
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
            # 先取 runbook(参考知识),再跑分析器(实时证据)。
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
            # 逐分析器:先采集本步的实时证据,再让**分析器能力**产出结构化解读。
            #
            # 此前这里只采集、不调用 provider.analyze() —— 于是四个推理能力里的
            # analyzer 完全不被评测覆盖,而 synthesize 恒收到 analyzer_results=[]。
            # 后果有两层:分析器退化不会让闸门变红(evidence_citation_rate 与
            # hallucination_rate 都算不到它);且综合器在离线看到的**入参形状**与
            # 生产不同,离线分数因此不能代表生产会发布什么。
            round_results: list[AnalyzerResult] = []
            for j, spec in enumerate(plan.analyzers):
                ev = _offline_collect(case_id, spec, round_index * 100 + j)
                evidences.append(ev)
                # 只把本分析器自己采到的证据交给它 —— 与工作流一致
                # (run_analyzer 只看 spec 对应的 evidences,不看别人的)。
                result, _ = await self._provider.analyze(context, spec, [ev])
                round_results.append(result)

            synthesis, _ = await self._provider.synthesize(
                context, evidences, round_results, round_index
            )
            # 在此重放运行时的证据优先守卫,使离线分数反映生产环境真正会发布的内容
            # (F2)。否则一次评估可能通过闸门,而运行时其实会拒绝该结论。
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
