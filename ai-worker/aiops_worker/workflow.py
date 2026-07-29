"""InvestigationWorkflow —— RCA 调查状态机。

只做确定性编排(架构 7.2/7.4)。所有 I/O 与模型调用都放在 Activity 中。时间统一取自
``workflow.now()``,因此重放(replay)结果稳定。

状态机(contracts.md / 架构 7.3):
  queued -> triaging -> (triage_published | planning) -> collecting ->
  synthesizing -> (concluded | needs_human) -> waiting_feedback -> closed
  (任一活跃阶段均可转入 cancelled)

信号:IncidentUpdated、IncidentResolved、HumanFeedback、Cancel。
"""
from __future__ import annotations

import asyncio
from datetime import timedelta
from typing import Any, Optional

from temporalio import workflow
from temporalio.common import RetryPolicy

with workflow.unsafe.imports_passed_through():
    from .activities import (
        ContextInput,
        DeepRCAInput,
        EventInput,
        InvestigationActivities,
        PhaseInput,
        PlanInput,
        PlanOutput,
        PublishDiagnosisInput,
        RunAnalyzerInput,
        RunAnalyzerOutput,
        RunbookInput,
        SynthesizeInput,
        TriageInput,
        UsageInput,
    )
    from .contracts import (
        ANALYZER_TOOLS,
        AnalyzerSpec,
        Budget,
        Evidence,
        IncidentContext,
        Phase,
        SynthesisResult,
        Usage,
        WorkflowInput,
        WorkflowResult,
    )

# Activity 执行的默认参数:有界超时 + 有限重试(架构 7.2)。
#
# 模型类 activity 给到 180s:``run_analyzer`` 是**串行**工具调用后再接一次模型
# 调用 —— 单个工具往返最长 AIOPS_HTTP_TIMEOUT_SEC(默认 15s),两个工具就 30s,
# 之后 reasoning 模型本身可能再花 60s+。原先的 90s 会把一次**正常但缓慢**的分析
# 判成超时并重试,那才是真正白烧三倍 token 的路径。
# 注意这不会让调查跑更久:Budget.max_duration_sec(默认 300s)会先兜住。
_MODEL_TIMEOUT = timedelta(seconds=180)
_IO_TIMEOUT = timedelta(seconds=30)
# 心跳窗口。activity 侧每 HEARTBEAT_INTERVAL_SEC(5s)心跳一次,这里留足 6 倍
# 余量以吸收 GC、事件循环抖动与 Temporal 的心跳节流(SDK 只按 timeout 的一定
# 比例真正上报)。作用是把「worker 被 OOM kill」的发现时间从 start_to_close
# (最长 180s)压到 30s 量级。
_HEARTBEAT_TIMEOUT = timedelta(seconds=30)
_RETRY = RetryPolicy(maximum_attempts=3, initial_interval=timedelta(seconds=1))
# 等待人工反馈的墙钟兜底:处于 WaitingFeedback 的调查不能永久阻塞。超时后自动关闭
# (业务库仍是事实源,人工可以重新打开)。
#
# 与控制面 AIOPS_WORKFLOW_RUN_TIMEOUT 的关系:那是**硬**兜底,到点由服务端直接
# 终止,不会执行 CLOSED 迁移、也不会 flush 用量 —— 库里会永久卡在
# waiting_feedback。因此它必须**远大于**本超时,只用来兜住「连本超时都没生效」
# 的异常。见 control-plane/internal/temporalx/client.go 的同名说明。
_FEEDBACK_TIMEOUT = timedelta(hours=48)


def _clip_analyzers_to_budget(
    analyzers: list["AnalyzerSpec"], remaining: int
) -> list["AnalyzerSpec"]:
    """裁剪本轮的分析器,使其工具调用总数不超过 ``remaining`` 的工具调用预算。

    纯函数且确定性:按计划顺序遍历分析器,只统计白名单内的工具(与 activity 实际会
    调用的集合一致)。

    完全放得下的分析器原样保留;正好跨越上限的那个分析器保留但截断其工具列表;
    之后的全部丢弃。这样 ``max_tool_calls`` 就成了**事前**闸门,而不是轮次之间的
    事后检查。
    """
    if remaining <= 0:
        return []
    clipped: list[AnalyzerSpec] = []
    used = 0
    for spec in analyzers:
        # 只有白名单内的工具才真正消耗一次工具调用(其余会被 activity 跳过);
        # 保持顺序地去重,以与 activity 的行为保持一致。
        allowed = ANALYZER_TOOLS.get(spec.analyzer, ())
        effective = [t for t in spec.tools if t in allowed]
        if not effective:
            continue
        room = remaining - used
        if room <= 0:
            break
        # 只保留裁剪后仍存在的工具所对应的查询参数,避免被裁剪的 spec 里
        # 残留永远不会被调用的工具的参数。
        if len(effective) <= room:
            kept = effective
            used += len(effective)
            stop = False
        else:
            kept = effective[:room]
            used += room
            stop = True
        clipped.append(
            spec.model_copy(
                update={
                    "tools": kept,
                    "queries": {t: a for t, a in spec.queries.items() if t in kept},
                }
            )
        )
        if stop:
            break
    return clipped


@workflow.defn(name="InvestigationWorkflow")
class InvestigationWorkflow:
    def __init__(self) -> None:
        self._phase: Phase = Phase.QUEUED
        self._cancel_requested: bool = False
        self._incident_resolved: bool = False
        self._feedback: Optional[dict[str, Any]] = None
        self._incident_version: int = 1
        self._start_time = None  # 在 run() 开始时由 workflow.now() 赋值
        self._usage = Usage()

    # -- 信号 -----------------------------------------------------------------

    @workflow.signal(name="IncidentUpdated")
    def incident_updated(self, payload: dict[str, Any]) -> None:
        self._incident_version = int(payload.get("version", self._incident_version))

    @workflow.signal(name="IncidentResolved")
    def incident_resolved(self, payload: dict[str, Any]) -> None:
        self._incident_resolved = True

    @workflow.signal(name="HumanFeedback")
    def human_feedback(self, payload: dict[str, Any]) -> None:
        self._feedback = payload

    @workflow.signal(name="Cancel")
    def cancel(self, payload: dict[str, Any] | None = None) -> None:
        self._cancel_requested = True

    # -- 查询 -----------------------------------------------------------------

    @workflow.query(name="phase")
    def query_phase(self) -> str:
        return self._phase.value

    @workflow.query(name="usage")
    def query_usage(self) -> dict[str, Any]:
        return self._usage.model_dump(mode="json")

    # -- 辅助方法 -------------------------------------------------------------

    def _refresh_elapsed(self) -> None:
        if self._start_time is not None:
            self._usage.elapsed_sec = (
                workflow.now() - self._start_time
            ).total_seconds()

    async def _transition(self, inp: WorkflowInput, phase: Phase, event: str = "") -> None:
        """切换到新阶段:更新本地状态,并持久化 phase 与事件。"""
        self._phase = phase
        await workflow.execute_activity_method(
            InvestigationActivities.record_phase,
            PhaseInput(
                investigation_id=inp.investigation_id,
                control_internal_url=inp.control_internal_url,
                phase=phase.value,
            ),
            start_to_close_timeout=_IO_TIMEOUT,
            retry_policy=_RETRY,
        )
        await self._emit(inp, event or f"phase_{phase.value}", {"phase": phase.value})

    async def _emit(self, inp: WorkflowInput, event_type: str, payload: dict) -> None:
        await workflow.execute_activity_method(
            InvestigationActivities.record_event,
            EventInput(
                investigation_id=inp.investigation_id,
                control_internal_url=inp.control_internal_url,
                event_type=event_type,
                payload=payload,
            ),
            start_to_close_timeout=_IO_TIMEOUT,
            retry_policy=_RETRY,
        )

    async def _flush_usage(self, inp: WorkflowInput) -> None:
        self._refresh_elapsed()
        await workflow.execute_activity_method(
            InvestigationActivities.record_usage,
            UsageInput(
                investigation_id=inp.investigation_id,
                control_internal_url=inp.control_internal_url,
                usage=self._usage,
            ),
            start_to_close_timeout=_IO_TIMEOUT,
            retry_policy=_RETRY,
        )

    def _budget_stop(self, budget: Budget) -> Optional[str]:
        """返回预算耗尽的原因,未耗尽则返回 None。确定性函数。"""
        self._refresh_elapsed()
        return self._usage.budget_exceeded(budget)

    def _should_cancel(self) -> bool:
        return self._cancel_requested or self._incident_resolved

    async def _finish_cancelled(self, inp: WorkflowInput) -> WorkflowResult:
        await self._transition(inp, Phase.CANCELLED, "cancelled")
        await self._flush_usage(inp)
        return WorkflowResult(
            investigation_id=inp.investigation_id,
            incident_id=inp.incident_id,
            final_phase=Phase.CANCELLED,
            usage=self._usage,
        )

    # -- 主状态机 -------------------------------------------------------------

    @workflow.run
    async def run(self, raw_input: WorkflowInput) -> WorkflowResult:
        # Temporal 的 Pydantic 转换器会直接给出 WorkflowInput;这里做防御性处理,
        # 以应对传入普通 dict 的情况(例如由其他 SDK 发起)。
        inp = (
            raw_input
            if isinstance(raw_input, WorkflowInput)
            else WorkflowInput.model_validate(raw_input)
        )
        budget: Budget = inp.budget
        self._start_time = workflow.now()
        self._incident_version = inp.incident_version

        # queued -> triaging
        await self._transition(inp, Phase.TRIAGING)
        if self._should_cancel():
            return await self._finish_cancelled(inp)

        context: IncidentContext = await workflow.execute_activity_method(
            InvestigationActivities.load_incident_context,
            ContextInput(
                investigation_id=inp.investigation_id,
                control_internal_url=inp.control_internal_url,
            ),
            start_to_close_timeout=_IO_TIMEOUT,
            retry_policy=_RETRY,
        )

        triage_out = await workflow.execute_activity_method(
            InvestigationActivities.run_quick_triage,
            TriageInput(context=context),
            start_to_close_timeout=_MODEL_TIMEOUT,
            heartbeat_timeout=_HEARTBEAT_TIMEOUT,
            retry_policy=_RETRY,
        )
        self._usage.add_model_usage(
            triage_out.usage.total_tokens, triage_out.usage.cost_usd
        )
        await self._flush_usage(inp)

        # 确定性的深度 RCA 闸门(**不是**交给 LLM 判断)。
        deep = await workflow.execute_activity_method(
            InvestigationActivities.evaluate_deep_rca_policy,
            DeepRCAInput(context=context, triage=triage_out.triage),
            start_to_close_timeout=_IO_TIMEOUT,
            retry_policy=_RETRY,
        )

        if not deep:
            # triaging -> triage_published -> waiting_feedback
            await self._transition(inp, Phase.TRIAGE_PUBLISHED, "triage_published")
            await self._emit(inp, "triage", triage_out.triage.model_dump(mode="json"))
            return await self._wait_for_feedback_or_close(inp)

        if self._should_cancel():
            return await self._finish_cancelled(inp)

        # triaging -> planning
        await self._transition(inp, Phase.PLANNING)
        plan_out: PlanOutput = await workflow.execute_activity_method(
            InvestigationActivities.build_investigation_plan,
            PlanInput(context=context, triage=triage_out.triage),
            start_to_close_timeout=_MODEL_TIMEOUT,
            heartbeat_timeout=_HEARTBEAT_TIMEOUT,
            retry_policy=_RETRY,
        )
        self._usage.add_model_usage(plan_out.usage.total_tokens, plan_out.usage.cost_usd)
        plan = plan_out.plan

        # 有界的 RCA 循环(架构 7.4 / 8.4)。
        escalation_reason: Optional[str] = None
        last_synthesis: Optional[SynthesisResult] = None
        round_index = 0
        # **跨轮累积**的证据。必须在循环外累积:综合器要能引用前几轮采到的证据,
        # 而 enforce_evidence_grounding 只按「本次入参里的实时证据」判定是否有据
        # (policy.py)。若只喂本轮证据,第 2 轮复述一条由第 1 轮证据支撑的结论
        # 会被判成无据并降级 —— 补充采集反而让结论更难成立。
        #
        # 载荷安全:max_tool_calls 是**全局**预算(不是每轮),因此累积总量同样
        # 受它约束;实测单条证据约 1.3 KB,默认预算 20 条合计约 37 KB,
        # 距 Temporal 2 MiB 上限两个数量级。
        cumulative_evidence: list[Evidence] = []

        # ``round_index`` 统计**已完成**的采集轮数。在 ``max_rounds=N`` 时,循环恰好
        # 跑 N 轮采集(轮次计数在一轮**结束**时才自增,因此 N=3 不会再悄悄只跑 2 轮)。
        # 下面轮次之间的预算检查只是兜底;真正权威的轮数守卫在自增之后。
        while True:
            if self._should_cancel():
                return await self._finish_cancelled(inp)
            stop = self._budget_stop(budget)
            if stop is not None:
                escalation_reason = f"budget_exhausted:{stop}"
                break

            # planning/collecting -> collecting
            await self._transition(inp, Phase.COLLECTING)
            round_evidence, analyzer_results = await self._collect(
                inp, context, plan, budget
            )
            # 证据累积;分析器结论**不**累积 —— 它们是模型对当轮证据的解读,
            # 跨轮叠加会把已被后续证据推翻的旧解读一并喂回去。综合器每轮基于
            # 全部证据 + 当轮解读重新推理。
            cumulative_evidence.extend(round_evidence)

            if self._should_cancel():
                return await self._finish_cancelled(inp)

            stop = self._budget_stop(budget)
            if stop is not None:
                escalation_reason = f"budget_exhausted:{stop}"
                break

            # collecting -> synthesizing
            await self._transition(inp, Phase.SYNTHESIZING)
            syn_out = await workflow.execute_activity_method(
                InvestigationActivities.synthesize_hypotheses,
                SynthesizeInput(
                    investigation_id=inp.investigation_id,
                    control_internal_url=inp.control_internal_url,
                    context=context,
                    evidences=cumulative_evidence,
                    analyzer_results=analyzer_results,
                    round_index=round_index,
                ),
                start_to_close_timeout=_MODEL_TIMEOUT,
                heartbeat_timeout=_HEARTBEAT_TIMEOUT,
                retry_policy=_RETRY,
            )
            self._usage.add_model_usage(
                syn_out.usage.total_tokens, syn_out.usage.cost_usd
            )
            last_synthesis = syn_out.synthesis
            # 缺乏证据支撑却标为 "supported" 的结论属于模型质量信号,而非实现细节:
            # 记录下来,让评审者看到流水线拒绝了未经证实的根因,而不是悄悄发布出去。
            if syn_out.ungrounded_downgraded:
                self._usage.ungrounded_downgrades += len(syn_out.ungrounded_downgraded)
                await self._emit(
                    inp,
                    "hypothesis_downgraded",
                    {
                        "reason": "no_realtime_evidence",
                        "hypothesis_ids": syn_out.ungrounded_downgraded,
                        "round": round_index,
                    },
                )

            # 本轮采集至此完成 —— 计入轮数。
            round_index += 1
            self._usage.rounds = round_index
            await self._flush_usage(inp)

            if syn_out.synthesis.has_supported_conclusion:
                # synthesizing -> concluded
                await self._transition(inp, Phase.CONCLUDED, "concluded")
                status = await self._do_publish_diagnosis(
                    inp, context, syn_out.synthesis, False, Phase.CONCLUDED
                )
                return await self._wait_for_feedback_or_close(
                    inp, diagnosis_status=status
                )

            if not syn_out.synthesis.has_actionable_next_query:
                escalation_reason = "no_actionable_next_query"
                break

            if round_index >= budget.max_rounds:
                escalation_reason = "budget_exhausted:max_rounds"
                break

            # synthesizing -> collecting(补充采集):重建调查计划。
            supp_out: PlanOutput = await workflow.execute_activity_method(
                InvestigationActivities.build_supplemental_plan,
                PlanInput(
                    context=context,
                    triage=triage_out.triage,
                    supplemental_from=syn_out.synthesis,
                ),
                start_to_close_timeout=_MODEL_TIMEOUT,
                heartbeat_timeout=_HEARTBEAT_TIMEOUT,
                retry_policy=_RETRY,
            )
            self._usage.add_model_usage(
                supp_out.usage.total_tokens, supp_out.usage.cost_usd
            )
            plan = supp_out.plan

        # 升级给人工(证据不足或预算耗尽)。
        return await self._escalate(inp, context, last_synthesis, escalation_reason or "unknown")

    # -- 证据采集(并行分析器 + runbook) --------------------------------------

    async def _collect(
        self, inp: WorkflowInput, context: IncidentContext, plan, budget: Budget
    ) -> tuple[list[Evidence], list]:
        """采集**本轮**的证据与分析器结论。跨轮累积由调用方负责(见 run)。"""
        scope = {"cluster_id": inp.cluster_id, "tenant_id": inp.tenant_id}

        all_evidence: list[Evidence] = []

        # 先取参考知识(runbook)—— 它只是数据,绝不作为证明。runbook 检索同样算工具
        # 调用:按剩余预算裁剪并计数,避免被用来绕过 max_tool_calls(架构 8.4)。
        runbook_budget = max(0, budget.max_tool_calls - self._usage.tool_calls)
        runbook_queries = plan.runbook_queries[:runbook_budget]
        if runbook_queries:
            runbook_ev = await workflow.execute_activity_method(
                InvestigationActivities.retrieve_runbooks,
                RunbookInput(
                    investigation_id=inp.investigation_id,
                    incident_id=inp.incident_id,
                    control_internal_url=inp.control_internal_url,
                    queries=runbook_queries,
                ),
                start_to_close_timeout=_IO_TIMEOUT,
                retry_policy=_RETRY,
            )
            # 参考知识以数据形式参与推理(架构 12.2):合并进证据集
            # (type=knowledge),而不是丢弃。
            all_evidence.extend(runbook_ev)
            self._usage.tool_calls += len(runbook_queries)

        # 事前护栏(架构 8.4):在派发任何任务**之前**,就把本轮的分析器及各分析器的
        # 工具裁剪到**剩余**的工具调用预算内,使单轮无法冲破 max_tool_calls。
        # 该过程是确定性的 —— 只依赖当前用量与(确定性的)计划顺序。
        specs = _clip_analyzers_to_budget(
            plan.analyzers, remaining=budget.max_tool_calls - self._usage.tool_calls
        )

        # 各分析器并行执行(架构 8.2)。每个分析器都是独立的 activity;Temporal 对
        # asyncio 做了改写,因此 asyncio.gather 在工作流内部依然是确定性的。裁剪后的
        # 工具数量给模型调用次数设了上限,进而限定了每轮的 token 与成本。
        futures = [
            workflow.execute_activity_method(
                InvestigationActivities.run_analyzer,
                RunAnalyzerInput(
                    investigation_id=inp.investigation_id,
                    incident_id=inp.incident_id,
                    control_internal_url=inp.control_internal_url,
                    context=context,
                    spec=spec,
                    scope=scope,
                ),
                start_to_close_timeout=_MODEL_TIMEOUT,
                heartbeat_timeout=_HEARTBEAT_TIMEOUT,
                retry_policy=_RETRY,
            )
            for spec in specs
        ]
        results: list[RunAnalyzerOutput] = list(await asyncio.gather(*futures))

        analyzer_results = []
        for out in results:
            all_evidence.extend(out.evidences)
            analyzer_results.append(out.result)
            self._usage.add_model_usage(out.usage.total_tokens, out.usage.cost_usd)
            self._usage.tool_calls += out.tool_calls
        await self._flush_usage(inp)
        return all_evidence, analyzer_results

    # -- 诊断发布 + 升级 -----------------------------------------------------

    async def _do_publish_diagnosis(
        self, inp: WorkflowInput, context, synthesis, escalated: bool, phase: Phase
    ) -> str:
        return await workflow.execute_activity_method(
            InvestigationActivities.publish_diagnosis,
            PublishDiagnosisInput(
                investigation_id=inp.investigation_id,
                incident_id=inp.incident_id,
                control_internal_url=inp.control_internal_url,
                context=context,
                synthesis=synthesis,
                escalated=escalated,
                phase=phase.value,
            ),
            start_to_close_timeout=_IO_TIMEOUT,
            retry_policy=_RETRY,
        )

    async def _escalate(
        self,
        inp: WorkflowInput,
        context: IncidentContext,
        synthesis: Optional[SynthesisResult],
        reason: str,
    ) -> WorkflowResult:
        # -> needs_human。即便如此,仍发布尽力而为的(unresolved)诊断结论。
        await self._transition(inp, Phase.NEEDS_HUMAN, "escalated")
        await self._emit(inp, "escalation", {"reason": reason})
        status = None
        if synthesis is None:
            synthesis = SynthesisResult(hypotheses=[])
        status = await self._do_publish_diagnosis(
            inp, context, synthesis, True, Phase.NEEDS_HUMAN
        )
        result = await self._wait_for_feedback_or_close(
            inp, diagnosis_status=status, escalation_reason=reason
        )
        return result

    # -- 等待人工反馈 ---------------------------------------------------------

    async def _wait_for_feedback_or_close(
        self,
        inp: WorkflowInput,
        diagnosis_status: Optional[str] = None,
        escalation_reason: Optional[str] = None,
    ) -> WorkflowResult:
        await self._transition(inp, Phase.WAITING_FEEDBACK, "waiting_feedback")

        # 阻塞等待人工反馈 / 取消 / 故障被解决,但绝不无限等待:有界等待在超时后
        # 自动关闭(墙钟兜底)。
        timed_out = False
        try:
            await workflow.wait_condition(
                lambda: self._feedback is not None or self._should_cancel(),
                timeout=_FEEDBACK_TIMEOUT,
            )
        except TimeoutError:
            timed_out = True

        if timed_out:
            await self._emit(inp, "feedback_timeout", {"after": str(_FEEDBACK_TIMEOUT)})
            final = Phase.CLOSED
        elif self._feedback is not None:
            action = str(self._feedback.get("action", "close"))
            await self._emit(inp, "human_feedback", self._feedback)
            if action in {"cancel", "reject"}:
                final = Phase.CANCELLED
            else:
                final = Phase.CLOSED
        elif self._cancel_requested:
            final = Phase.CANCELLED
        else:  # 故障已在外部被解决
            final = Phase.CLOSED

        await self._transition(inp, final, final.value)
        await self._flush_usage(inp)

        from .contracts import DiagnosisStatus

        return WorkflowResult(
            investigation_id=inp.investigation_id,
            incident_id=inp.incident_id,
            final_phase=final,
            usage=self._usage,
            diagnosis_status=(
                DiagnosisStatus(diagnosis_status) if diagnosis_status else None
            ),
            escalation_reason=escalation_reason,
        )
