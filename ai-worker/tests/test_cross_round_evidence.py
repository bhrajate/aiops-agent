"""跨轮证据可见性的回归测试。

这条行为**只在多轮时可见**:第 1 轮无论累积与否表现都一样。原先
``all_evidence`` 在 ``_collect`` 内部每轮重新初始化,导致第 2 轮的综合器看不到
第 1 轮采到的证据;而 ``enforce_evidence_grounding`` 只按「本次入参里的实时证据」
判定是否有据(policy.py),于是一条由第 1 轮证据支撑的结论在第 2 轮会被判成
无据并降级 —— 补充采集反而让结论更难成立。
"""
from __future__ import annotations

import asyncio
import uuid

import pytest
from temporalio.contrib.pydantic import pydantic_data_converter
from temporalio.worker import Worker

from aiops_worker.activities import InvestigationActivities
from aiops_worker.contracts import (
    AnalyzerResult,
    AnalyzerSpec,
    AnalyzerType,
    Budget,
    Evidence,
    Hypothesis,
    HypothesisStatus,
    InvestigationPlan,
    ModelUsage,
    Phase,
    SynthesisResult,
    TriageResult,
    WorkflowInput,
)
from aiops_worker.workflow import InvestigationWorkflow
from tests.conftest import make_context

try:  # pragma: no cover - 导入守卫
    from temporalio.testing import WorkflowEnvironment
except Exception:  # pragma: no cover
    WorkflowEnvironment = None  # type: ignore

TASK_QUEUE = "investigation-ai-xround"


class _CountingClient:
    """内存版内部 API。每次工具调用返回**唯一** evidence_id,
    这样才能分辨证据来自哪一轮。"""

    def __init__(self, context):
        self._context = context
        self._n = 0
        self.hypotheses_written: list = []

    async def load_context(self, investigation_id):
        return self._context

    async def invoke_tool(self, investigation_id, incident_id, tool, arguments, scope=None):
        self._n += 1
        etype = {
            "query_metrics": "metric",
            "search_logs": "log",
            "get_traces": "trace",
            "inspect_dependencies": "trace",
            "get_workload_state": "kubernetes",
            "get_kubernetes_events": "kubernetes",
            "list_recent_changes": "change",
            "retrieve_runbook": "knowledge",
        }.get(tool, "metric")
        return Evidence(
            evidence_id=f"ev-{self._n:03d}-{tool}",
            type=etype,
            source="fake",
            tool_name=tool,
            summary=f"{tool} 第 {self._n} 次调用的证据",
        )

    async def set_phase(self, investigation_id, phase):
        pass

    async def emit_event(self, investigation_id, event_type, payload, idempotency_key=""):
        pass

    async def put_hypotheses(self, investigation_id, hypotheses):
        self.hypotheses_written = list(hypotheses)

    async def put_diagnosis(self, investigation_id, diagnosis, phase):
        pass

    async def put_usage(self, investigation_id, usage):
        pass


class _TwoRoundProvider:
    """强制走满 2 轮,并记录每轮综合器**实际收到**的证据 id。

    第 0 轮:不给结论,但给出 missing_evidence(触发补充采集)。
    第 1 轮:给出 SUPPORTED 结论,且**只引用第 0 轮的证据 id** —— 这正是原实现
             会误降级的情形。
    """

    name = "two-round-stub"

    def __init__(self) -> None:
        self.synth_calls: list[dict] = []
        self._round0_ids: list[str] = []

    @staticmethod
    def _usage() -> ModelUsage:
        return ModelUsage(provider="stub", model="stub", input_tokens=10, output_tokens=5)

    async def quick_triage(self, context):
        return (
            TriageResult(
                summary="需要深度 RCA",
                suspected_fault_category="release_regression",
                severity_assessment="P2",
                recommend_deep_rca=True,
                rationale="stub",
            ),
            self._usage(),
        )

    async def build_plan(self, context, triage, supplemental_from=None):
        if supplemental_from is None:
            plan = InvestigationPlan(
                analyzers=[
                    AnalyzerSpec(analyzer=AnalyzerType.METRICS, tools=["query_metrics"]),
                    AnalyzerSpec(analyzer=AnalyzerType.LOGS, tools=["search_logs"]),
                ],
                runbook_queries=[],
            )
        else:
            # 补充轮采不同的工具,确保两轮的证据 id 集合不相交。
            plan = InvestigationPlan(
                analyzers=[
                    AnalyzerSpec(analyzer=AnalyzerType.CHANGE, tools=["list_recent_changes"]),
                ],
                runbook_queries=[],
            )
        return plan, self._usage()

    async def analyze(self, context, spec, evidences):
        return (
            AnalyzerResult(
                analyzer=spec.analyzer,
                findings=[f"{spec.analyzer.value} 观察到异常"],
                evidence_ids=[e.evidence_id for e in evidences],
            ),
            self._usage(),
        )

    async def synthesize(self, context, evidences, analyzer_results, round_index):
        ids = [e.evidence_id for e in evidences if not e.is_reference_knowledge]
        self.synth_calls.append({"round": round_index, "evidence_ids": list(ids)})

        if round_index == 0:
            self._round0_ids = list(ids)
            hyp = Hypothesis(
                hypothesis_id="hyp-1",
                rank=1,
                statement="疑似发布回归,但还缺变更证据。",
                confidence=0.4,
                supporting_evidence_ids=ids,
                missing_evidence=["最近的变更记录"],
                status=HypothesisStatus.UNRESOLVED,
            )
            return SynthesisResult(hypotheses=[hyp]), self._usage()

        # 第 1 轮:**只**引用第 0 轮的证据。若综合器拿不到累积证据,
        # enforce_evidence_grounding 会把这条判成无据并降级。
        hyp = Hypothesis(
            hypothesis_id="hyp-1",
            rank=1,
            statement="根因:上一次发布引入的回归。",
            confidence=0.85,
            supporting_evidence_ids=self._round0_ids,
            missing_evidence=[],
            status=HypothesisStatus.SUPPORTED,
        )
        return SynthesisResult(hypotheses=[hyp]), self._usage()


async def _start_env():
    if WorkflowEnvironment is None:
        pytest.skip("temporalio testing environment unavailable")
    try:
        return await WorkflowEnvironment.start_time_skipping(
            data_converter=pydantic_data_converter
        )
    except Exception as exc:  # pragma: no cover - 无缓存二进制的离线 CI
        pytest.skip(f"cannot start time-skipping test server: {exc}")


@pytest.mark.asyncio
async def test_synthesizer_sees_evidence_from_earlier_rounds():
    env = await _start_env()
    provider = _TwoRoundProvider()
    ctx = make_context(fault_category="release_regression", severity="P2")
    fake = _CountingClient(ctx)
    acts = InvestigationActivities(provider)
    acts._client = lambda base_url: fake  # type: ignore[assignment]

    try:
        async with Worker(
            env.client,
            task_queue=TASK_QUEUE,
            workflows=[InvestigationWorkflow],
            activities=[
                acts.load_incident_context,
                acts.run_quick_triage,
                acts.evaluate_deep_rca_policy,
                acts.build_investigation_plan,
                acts.build_supplemental_plan,
                acts.retrieve_runbooks,
                acts.run_analyzer,
                acts.synthesize_hypotheses,
                acts.publish_diagnosis,
                acts.record_phase,
                acts.record_event,
                acts.record_usage,
            ],
        ):
            handle = await env.client.start_workflow(
                InvestigationWorkflow.run,
                WorkflowInput(
                    investigation_id="i-xround",
                    incident_id="inc-123",
                    cluster_id="prod-cn-1",
                    budget=Budget(max_rounds=2, max_tool_calls=10),
                ),
                id=f"wf-{uuid.uuid4()}",
                task_queue=TASK_QUEUE,
            )
            for _ in range(400):
                if await handle.query("phase") == Phase.WAITING_FEEDBACK.value:
                    break
                await asyncio.sleep(0.05)
            await handle.signal("HumanFeedback", {"action": "close"})
            result = await handle.result()
    finally:
        await env.shutdown()

    assert len(provider.synth_calls) == 2, f"应走满 2 轮,实际 {provider.synth_calls}"
    r0 = set(provider.synth_calls[0]["evidence_ids"])
    r1 = set(provider.synth_calls[1]["evidence_ids"])
    assert r0, "第 0 轮应采到证据"

    # 核心断言:第 1 轮必须**包含**第 0 轮的全部证据,且有新增。
    assert r0 <= r1, f"第 1 轮丢了第 0 轮的证据: 少了 {r0 - r1}"
    assert r1 - r0, "第 1 轮应采到新证据(否则测不出累积)"

    # 后果断言:只引用第 0 轮证据的结论必须仍然成立。
    # 累积前这里会被 enforce_evidence_grounding 降级成 UNRESOLVED。
    assert result.usage.ungrounded_downgrades == 0, (
        "由早期轮次证据支撑的结论被误降级 —— 说明综合器没拿到累积证据"
    )
    assert result.final_phase == Phase.CLOSED
    assert fake.hypotheses_written, "应落库假设"
    assert fake.hypotheses_written[0].status == HypothesisStatus.SUPPORTED


@pytest.mark.asyncio
async def test_accumulated_evidence_payload_stays_far_below_grpc_limit():
    """累积证据后仍需远离 Temporal 载荷上限。

    这条不是复述已知结论,而是**回归护栏**:若日后放宽 max_tool_calls 或给
    Evidence 加字段,它会先失败。上界取 max_tool_calls 而非 max_rounds ——
    工具调用预算是**全局**的,所以它同时也是累积证据条数的上界。
    """
    from aiops_worker.activities import SynthesizeInput
    from aiops_worker.contracts import MAX_TOOL_ARG_LEN

    n = Budget().max_tool_calls
    evidences = [
        Evidence(
            evidence_id=f"ev-{i:010x}",
            type="log",
            source="loki",
            tool_name="search_logs",
            query={"arguments": {"query": "q" * MAX_TOOL_ARG_LEN}},
            time_range={"from": "2026-07-29T00:00:00Z", "to": "2026-07-29T00:15:00Z"},
            # Go 侧 summary 是定长聚合串(唯一变长部分被 truncate 到 80 字符),
            # 这里按最长形态取值。
            summary="production-payment/checkout-api 日志命中 12847 行"
            "(ERROR 3921、WARN 8102),查询:" + "x" * 80 + "…。",
            raw_ref="s3://aiops-evidence/inv-0123456789abcdef/ev-0123456789.json",
            content_hash="sha256:" + "a" * 64,
            freshness="live",
            redaction_status="redacted",
        )
        for i in range(n)
    ]
    inp = SynthesizeInput(
        investigation_id="inv-0123456789abcdef",
        control_internal_url="http://aiops-control-plane.aiops.svc.cluster.local:8090",
        context=make_context(),
        evidences=evidences,
        analyzer_results=[
            AnalyzerResult(analyzer=a, findings=["f" * 300] * 5,
                           evidence_ids=[e.evidence_id for e in evidences])
            for a in AnalyzerType
        ],
        round_index=Budget().max_rounds - 1,
    )
    payloads = await pydantic_data_converter.encode([inp])
    size = sum(len(p.SerializeToString()) for p in payloads)

    # 512 KiB 是服务端开始告警的阈值(硬上限 2 MiB),留 4 倍余量。
    warn_threshold = 512 * 1024
    assert size < warn_threshold, (
        f"累积证据载荷 {size / 1024:.1f} KiB 已触及 512 KiB 告警阈值"
    )
