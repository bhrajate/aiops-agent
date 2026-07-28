"""确定性的 mock 模型 provider。

不需要 API key,产出稳定且贴合场景的中文输出,因此整条 InvestigationWorkflow
可以完全离线端到端跑通(用于测试、演示与 CI)。

确定性规则:
- 不含随机数,不读时钟。
- token / 成本估算是输入文本长度的纯函数。
- 输出从以故障类别为键的固定场景表中选取,而故障类别由故障单 + 信号确定性地推断。

覆盖四类故障场景(架构 6.2 的 fault_category):
release_regression、resource_saturation、dependency_failure、config_error。
"""
from __future__ import annotations

from ..contracts import (
    AnalyzerResult,
    AnalyzerSpec,
    AnalyzerType,
    Evidence,
    Hypothesis,
    HypothesisStatus,
    IncidentContext,
    InvestigationPlan,
    AnalyzerSpec as _Spec,  # noqa: F401  (保留以提升下方场景表的可读性)
    ModelUsage,
    SynthesisResult,
    TriageResult,
)
from .base import ModelProvider, fence_evidence_as_data

# 按故障类别组织的场景知识。每个条目同时驱动初判、规划与综合三个阶段,
# 使三者讲出前后一致的故事。
_SCENARIOS: dict[str, dict] = {
    "release_regression": {
        "triage": "近期发布后 checkout 服务 5xx 错误率显著上升,疑似新版本回归。",
        "statement": "新版本引入的连接池/依赖调用变更导致请求排队与错误率飙升。",
        "analyzers": [
            (AnalyzerType.CHANGE, "定位最近一次发布/配置变更及其时间点", ["list_recent_changes"], {}),
            (AnalyzerType.METRICS, "对比新旧版本实例的错误率与延迟", ["query_metrics"], {
                "query_metrics": {
                    "expr": 'sum by (version) (rate(http_requests_total{status=~"5.."}[5m]))'
                }
            }),
            (AnalyzerType.LOGS, "检索新版本实例的错误日志与异常栈", ["search_logs"], {
                "search_logs": {"query": '{} |~ "(?i)(exception|stack trace|5[0-9][0-9])"'}
            }),
            (AnalyzerType.KUBERNETES, "确认新版本 ReplicaSet 与滚动发布状态", ["get_workload_state", "get_kubernetes_events"], {}),
        ],
        "runbooks": ["checkout 发布回滚手册", "5xx 错误率突增排查手册"],
        "missing": ["新旧版本实例级连接池等待时间指标"],
        "next_actions": ["按版本维度查询连接池等待时间", "评估回滚到上一个稳定版本"],
    },
    "resource_saturation": {
        "triage": "工作负载出现 CPU/内存饱和与限流,疑似资源不足或泄漏导致性能劣化。",
        "statement": "Pod 资源达到 limit 触发 CPU throttling / OOMKill,导致延迟升高与重启。",
        "analyzers": [
            (AnalyzerType.KUBERNETES, "检查 Pod 重启、OOMKilled 与资源 limit", ["get_workload_state", "get_kubernetes_events"], {}),
            (AnalyzerType.METRICS, "查看 CPU/内存使用率与 throttling 指标", ["query_metrics"], {
                "query_metrics": {
                    "expr": "sum by (pod) (rate(container_cpu_cfs_throttled_seconds_total[5m]))"
                }
            }),
            (AnalyzerType.LOGS, "检索 OOM 与 GC/内存告警日志", ["search_logs"], {
                "search_logs": {"query": '{} |~ "(?i)(oom|out of memory|gc pause)"'}
            }),
        ],
        "runbooks": ["资源饱和与扩容手册", "OOMKilled 排查手册"],
        "missing": ["历史内存增长曲线以区分泄漏与突增流量"],
        "next_actions": ["临时上调资源 limit 或水平扩容", "对内存增长做堆分析确认是否泄漏"],
    },
    "dependency_failure": {
        "triage": "下游依赖(数据库/中间件/外部服务)异常,错误沿调用链向上传播。",
        "statement": "关键下游依赖不可用或超时,导致上游服务级联失败。",
        "analyzers": [
            (AnalyzerType.TRACES, "沿调用链定位首个高延迟/报错的依赖", ["get_traces", "inspect_dependencies"], {
                "get_traces": {"service": "auth-service"}
            }),
            (AnalyzerType.METRICS, "查看依赖的错误率、超时与饱和度", ["query_metrics"], {
                "query_metrics": {
                    "expr": 'histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket[5m])))'
                }
            }),
            (AnalyzerType.LOGS, "检索连接超时/拒绝连接类错误", ["search_logs"], {
                "search_logs": {"query": '{} |~ "(?i)(timeout|connection refused|upstream)"'}
            }),
        ],
        "runbooks": ["依赖级联故障处置手册", "数据库连接超时排查手册"],
        "missing": ["下游依赖侧的服务端指标与容量水位"],
        "next_actions": ["对故障依赖启用熔断/降级", "联系依赖 owner 确认其可用性"],
    },
    "config_error": {
        "triage": "疑似配置/环境变量错误导致服务启动或运行异常。",
        "statement": "错误的配置项(如连接串、开关、密钥引用)导致服务行为异常。",
        "analyzers": [
            (AnalyzerType.CHANGE, "定位最近的配置(ConfigMap/Secret)变更", ["list_recent_changes"], {}),
            (AnalyzerType.KUBERNETES, "检查 CrashLoop、配置挂载与启动事件", ["get_workload_state", "get_kubernetes_events"], {}),
            (AnalyzerType.LOGS, "检索配置解析失败/校验错误日志", ["search_logs"], {
                "search_logs": {"query": '{} |~ "(?i)(invalid config|unmarshal|missing (env|key)|parse error)"'}
            }),
        ],
        "runbooks": ["配置变更回滚手册", "CrashLoopBackOff 排查手册"],
        "missing": ["变更前后配置项 diff"],
        "next_actions": ["回滚最近一次配置变更", "补充配置项 schema 校验"],
    },
}

# 未知故障类别的兜底:产出低置信度、非结论性的结果,让工作流走升级路径,
# 而不是编造答案。
_UNKNOWN_KEY = "__unknown__"

# 当故障单本身没有类别时,用于推断故障类别的关键词线索。
_KEYWORD_HINTS: list[tuple[str, tuple[str, ...]]] = [
    ("release_regression", ("release", "deploy", "版本", "发布", "rollout", "canary")),
    ("resource_saturation", ("oom", "cpu", "memory", "throttl", "内存", "饱和", "resource")),
    ("dependency_failure", ("timeout", "dependency", "database", "downstream", "依赖", "超时", "连接")),
    ("config_error", ("config", "configmap", "secret", "crashloop", "配置", "env")),
]


def _deepcopy_queries(queries: dict) -> dict:
    """复制场景表中的查询参数,使调用方永远无法改动这张共享表
    (mock 必须在多次调查之间保持确定性)。"""
    return {tool: dict(args) for tool, args in (queries or {}).items()}


def _estimate_usage(*texts: str, out_len: int = 300) -> ModelUsage:
    """确定性的 token / 成本估算:约每 4 个字符算 1 个 token。"""
    in_chars = sum(len(t) for t in texts)
    input_tokens = max(1, in_chars // 4)
    output_tokens = max(1, out_len // 4)
    # mock 用的简单固定费率(美元 / 千 token)。
    cost = round((input_tokens + output_tokens) / 1000.0 * 0.003, 6)
    return ModelUsage(
        provider="mock",
        model="mock",
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        cost_usd=cost,
    )


def infer_fault_category(context: IncidentContext) -> str:
    """由故障单 + 信号确定性地推断故障类别。

    优先级:显式的 incident.fault_category(若为已知类别)-> 信号标签 / 告警名中的
    关键词线索 -> unknown。
    """
    fc = (context.incident.fault_category or "").strip().lower()
    if fc in _SCENARIOS:
        return fc

    haystack_parts: list[str] = [context.incident.fault_category or ""]
    for sig in context.signals:
        haystack_parts.append(sig.signal_type or "")
        haystack_parts.append(sig.source or "")
        for k, v in (sig.labels or {}).items():
            haystack_parts.append(str(k))
            haystack_parts.append(str(v))
    haystack = " ".join(haystack_parts).lower()

    for category, keywords in _KEYWORD_HINTS:
        if any(kw in haystack for kw in keywords):
            return category
    return _UNKNOWN_KEY


class MockProvider(ModelProvider):
    name = "mock"

    async def quick_triage(self, context: IncidentContext):
        key = infer_fault_category(context)
        sev = context.incident.severity
        deep = sev in {"P1", "P2"}
        if key == _UNKNOWN_KEY:
            summary = "已完成快速分诊,但暂无法确定明确的故障类别,建议人工确认。"
            triage = TriageResult(
                summary=summary,
                suspected_fault_category=None,
                severity_assessment=sev,
                recommend_deep_rca=deep,
                rationale="未匹配到已知故障模式,依据严重级别决定是否深挖。",
            )
        else:
            sc = _SCENARIOS[key]
            triage = TriageResult(
                summary=sc["triage"],
                suspected_fault_category=key,
                severity_assessment=sev,
                recommend_deep_rca=deep or True,
                rationale=f"信号与 {key} 故障模式高度匹配,建议进入深度 RCA。",
            )
        usage = _estimate_usage(context.model_dump_json())
        return TriageResult.model_validate(triage.model_dump()), usage

    async def build_plan(self, context, triage, supplemental_from=None):
        key = infer_fault_category(context)
        sc = _SCENARIOS.get(key)
        if sc is None:
            # 未知类别:只做最小化的通用探查。
            plan = InvestigationPlan(
                analyzers=[
                    AnalyzerSpec(
                        analyzer=AnalyzerType.KUBERNETES,
                        objective="收集工作负载状态与近期事件以缩小范围",
                        tools=["get_workload_state", "get_kubernetes_events"],
                    ),
                    AnalyzerSpec(
                        analyzer=AnalyzerType.METRICS,
                        objective="查看核心指标是否存在异常",
                        tools=["query_metrics"],
                    ),
                ],
                runbook_queries=["通用故障排查手册"],
            )
        elif supplemental_from is not None:
            # 补充轮次:用 1 个分析器定向补齐缺失的证据。
            a, _obj, tools, queries = sc["analyzers"][1]
            plan = InvestigationPlan(
                analyzers=[
                    AnalyzerSpec(
                        analyzer=a,
                        objective="补充采集缺失的关键指标以确认/排除假设",
                        tools=list(tools),
                        queries=_deepcopy_queries(queries),
                    )
                ],
                runbook_queries=[],
            )
        else:
            plan = InvestigationPlan(
                analyzers=[
                    AnalyzerSpec(
                        analyzer=a,
                        objective=obj,
                        tools=list(tools),
                        queries=_deepcopy_queries(queries),
                    )
                    for (a, obj, tools, queries) in sc["analyzers"]
                ],
                runbook_queries=list(sc["runbooks"]),
            )
        usage = _estimate_usage(context.model_dump_json(), triage.model_dump_json())
        return InvestigationPlan.model_validate(plan.model_dump()), usage

    async def analyze(self, context, spec: AnalyzerSpec, evidences: list[Evidence]):
        # 把不可信证据围栏标记为**数据**(即便在 mock 中也照样走这条防线)。
        _ = fence_evidence_as_data(evidences)
        realtime = [e for e in evidences if not e.is_reference_knowledge]
        findings = [
            f"[{spec.analyzer.value}] 结合 {len(realtime)} 条实时证据: {e.summary}"
            for e in realtime
        ] or [f"[{spec.analyzer.value}] 未采集到实时证据。"]
        result = AnalyzerResult(
            analyzer=spec.analyzer,
            findings=findings,
            evidence_ids=[e.evidence_id for e in realtime],
        )
        usage = _estimate_usage(spec.model_dump_json(), *(e.summary for e in evidences))
        return AnalyzerResult.model_validate(result.model_dump()), usage

    async def synthesize(self, context, evidences, analyzer_results, round_index):
        key = infer_fault_category(context)
        realtime_ids = [e.evidence_id for e in evidences if not e.is_reference_knowledge]
        comp = None
        if context.incident.affected_resources:
            comp = context.incident.affected_resources[0]

        if key == _UNKNOWN_KEY or not realtime_ids:
            # 低置信度且未定论 -> 工作流升级给人工。
            hyp = Hypothesis(
                hypothesis_id="hyp-1",
                rank=1,
                statement="现有证据不足以确定单一根因,存在多种可能。",
                component_ref=comp,
                confidence=0.3,
                supporting_evidence_ids=realtime_ids,
                contradicting_evidence_ids=[],
                missing_evidence=["更细粒度的组件级指标与关联变更"],
                status=HypothesisStatus.UNRESOLVED,
            )
            result = SynthesisResult(hypotheses=[hyp])
        else:
            sc = _SCENARIOS[key]
            # 实时证据充足 -> 给出被支撑的结论。
            supported = Hypothesis(
                hypothesis_id="hyp-1",
                rank=1,
                statement=sc["statement"],
                component_ref=comp,
                confidence=0.78,
                supporting_evidence_ids=realtime_ids,
                contradicting_evidence_ids=[],
                missing_evidence=[],
                status=HypothesisStatus.SUPPORTED,
            )
            alt = Hypothesis(
                hypothesis_id="hyp-2",
                rank=2,
                statement="次要可能:偶发流量尖峰导致的瞬时抖动。",
                component_ref=comp,
                confidence=0.22,
                supporting_evidence_ids=[],
                contradicting_evidence_ids=realtime_ids[:1],
                missing_evidence=sc["missing"],
                status=HypothesisStatus.REJECTED,
            )
            result = SynthesisResult(hypotheses=[supported, alt])
        usage = _estimate_usage(
            context.model_dump_json(),
            *(e.summary for e in evidences),
        )
        return SynthesisResult.model_validate(result.model_dump()), usage
