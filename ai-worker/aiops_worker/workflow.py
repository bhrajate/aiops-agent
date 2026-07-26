"""InvestigationWorkflow -- the RCA investigation state machine.

Deterministic orchestration only (architecture 7.2/7.4). All I/O and model
calls are Activities. Time comes from ``workflow.now()`` so replays are stable.

State machine (contracts.md / architecture 7.3):
  queued -> triaging -> (triage_published | planning) -> collecting ->
  synthesizing -> (concluded | needs_human) -> waiting_feedback -> closed
  (+ cancelled from any active phase)

Signals: IncidentUpdated, IncidentResolved, HumanFeedback, Cancel.
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
        Budget,
        Evidence,
        IncidentContext,
        Phase,
        SynthesisResult,
        Usage,
        WorkflowInput,
        WorkflowResult,
    )

# Activity execution defaults: bounded time + limited retries (architecture 7.2).
_MODEL_TIMEOUT = timedelta(seconds=90)
_IO_TIMEOUT = timedelta(seconds=30)
_RETRY = RetryPolicy(maximum_attempts=3, initial_interval=timedelta(seconds=1))


@workflow.defn(name="InvestigationWorkflow")
class InvestigationWorkflow:
    def __init__(self) -> None:
        self._phase: Phase = Phase.QUEUED
        self._cancel_requested: bool = False
        self._incident_resolved: bool = False
        self._feedback: Optional[dict[str, Any]] = None
        self._incident_version: int = 1
        self._start_time = None  # set at run() start via workflow.now()
        self._usage = Usage()

    # -- signals -------------------------------------------------------------

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

    # -- queries -------------------------------------------------------------

    @workflow.query(name="phase")
    def query_phase(self) -> str:
        return self._phase.value

    @workflow.query(name="usage")
    def query_usage(self) -> dict[str, Any]:
        return self._usage.model_dump(mode="json")

    # -- helpers -------------------------------------------------------------

    def _refresh_elapsed(self) -> None:
        if self._start_time is not None:
            self._usage.elapsed_sec = (
                workflow.now() - self._start_time
            ).total_seconds()

    async def _transition(self, inp: WorkflowInput, phase: Phase, event: str = "") -> None:
        """Move to a new phase: update local state + persist phase + event."""
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
        """Return exhausted-budget reason, or None. Deterministic."""
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

    # -- main state machine --------------------------------------------------

    @workflow.run
    async def run(self, raw_input: WorkflowInput) -> WorkflowResult:
        # Temporal's Pydantic converter delivers a WorkflowInput; be defensive
        # if a plain dict slips through (e.g. started by another SDK).
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
            retry_policy=_RETRY,
        )
        self._usage.add_model_usage(
            triage_out.usage.total_tokens, triage_out.usage.cost_usd
        )
        await self._flush_usage(inp)

        # Deterministic deep-RCA gate (NOT the LLM).
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
            retry_policy=_RETRY,
        )
        self._usage.add_model_usage(plan_out.usage.total_tokens, plan_out.usage.cost_usd)
        plan = plan_out.plan

        # Bounded RCA loop (architecture 7.4 / 8.4).
        escalation_reason: Optional[str] = None
        last_synthesis: Optional[SynthesisResult] = None
        round_index = 0

        while True:
            self._usage.rounds = round_index + 1
            stop = self._budget_stop(budget)
            if stop is not None:
                escalation_reason = f"budget_exhausted:{stop}"
                break
            if self._should_cancel():
                return await self._finish_cancelled(inp)

            # planning/collecting -> collecting
            await self._transition(inp, Phase.COLLECTING)
            all_evidence, analyzer_results = await self._collect(inp, context, plan)

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
                    evidences=all_evidence,
                    analyzer_results=analyzer_results,
                    round_index=round_index,
                ),
                start_to_close_timeout=_MODEL_TIMEOUT,
                retry_policy=_RETRY,
            )
            self._usage.add_model_usage(
                syn_out.usage.total_tokens, syn_out.usage.cost_usd
            )
            last_synthesis = syn_out.synthesis
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

            round_index += 1
            if round_index >= budget.max_rounds:
                escalation_reason = "budget_exhausted:max_rounds"
                break

            # synthesizing -> collecting (supplemental): rebuild the plan.
            supp_out: PlanOutput = await workflow.execute_activity_method(
                InvestigationActivities.build_supplemental_plan,
                PlanInput(
                    context=context,
                    triage=triage_out.triage,
                    supplemental_from=syn_out.synthesis,
                ),
                start_to_close_timeout=_MODEL_TIMEOUT,
                retry_policy=_RETRY,
            )
            self._usage.add_model_usage(
                supp_out.usage.total_tokens, supp_out.usage.cost_usd
            )
            plan = supp_out.plan

        # Escalate to human (insufficient evidence or budget exhausted).
        return await self._escalate(inp, context, last_synthesis, escalation_reason or "unknown")

    # -- collection (parallel analyzers + runbooks) --------------------------

    async def _collect(
        self, inp: WorkflowInput, context: IncidentContext, plan
    ) -> tuple[list[Evidence], list]:
        scope = {"cluster_id": inp.cluster_id, "tenant_id": inp.tenant_id}

        # Reference knowledge (runbooks) first -- data only, never proof.
        if plan.runbook_queries:
            await workflow.execute_activity_method(
                InvestigationActivities.retrieve_runbooks,
                RunbookInput(
                    investigation_id=inp.investigation_id,
                    incident_id=inp.incident_id,
                    control_internal_url=inp.control_internal_url,
                    queries=plan.runbook_queries,
                ),
                start_to_close_timeout=_IO_TIMEOUT,
                retry_policy=_RETRY,
            )

        # Analyzers run in parallel (architecture 8.2). Each is its own
        # activity; Temporal patches asyncio so asyncio.gather stays
        # deterministic inside a workflow.
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
                retry_policy=_RETRY,
            )
            for spec in plan.analyzers
        ]
        results: list[RunAnalyzerOutput] = list(await asyncio.gather(*futures))

        all_evidence: list[Evidence] = []
        analyzer_results = []
        for out in results:
            all_evidence.extend(out.evidences)
            analyzer_results.append(out.result)
            self._usage.add_model_usage(out.usage.total_tokens, out.usage.cost_usd)
            self._usage.tool_calls += out.tool_calls
        await self._flush_usage(inp)
        return all_evidence, analyzer_results

    # -- diagnosis + escalation ---------------------------------------------

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
        # -> needs_human. Still publish the best-effort (unresolved) diagnosis.
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

    # -- waiting for feedback ------------------------------------------------

    async def _wait_for_feedback_or_close(
        self,
        inp: WorkflowInput,
        diagnosis_status: Optional[str] = None,
        escalation_reason: Optional[str] = None,
    ) -> WorkflowResult:
        await self._transition(inp, Phase.WAITING_FEEDBACK, "waiting_feedback")

        # Block until human feedback / cancel / incident resolved.
        await workflow.wait_condition(
            lambda: self._feedback is not None or self._should_cancel()
        )

        if self._feedback is not None:
            action = str(self._feedback.get("action", "close"))
            await self._emit(inp, "human_feedback", self._feedback)
            if action in {"cancel", "reject"}:
                final = Phase.CANCELLED
            else:
                final = Phase.CLOSED
        elif self._cancel_requested:
            final = Phase.CANCELLED
        else:  # incident resolved externally
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
