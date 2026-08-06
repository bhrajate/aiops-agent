"""ModelProvider 抽象:四种推理能力的接口。

四种能力对应 Agent 拓扑中的角色(架构 8):初判(triage)、规划(planner)、
分析(analyzer)、综合(synthesizer)。每个 provider 都返回经过校验的 Pydantic
契约对象,**并附带**一个 :class:`ModelUsage` 信封 —— 预算闸门与成本指标都读它。

提示注入防御与共用提示词构造在 :mod:`.prompt_safety`。本模块从那里 re-export,
使 ``from .base import sanitize_untrusted_text`` 这类既有写法继续可用:
那些函数是**跨 provider 的共用契约**,从哪个模块取到不该影响调用方。
新代码建议直接从 ``prompt_safety`` 导入,语义更明确。
"""
from __future__ import annotations

import abc

from ..contracts import (
    AnalyzerResult,
    AnalyzerSpec,
    Evidence,
    IncidentContext,
    InvestigationPlan,
    ModelUsage,
    SynthesisResult,
    TriageResult,
)
from .prompt_safety import (
    fence_context_as_data,
    fence_evidence_as_data,
    query_args_help,
    sanitize_analyzer_results,
    sanitize_untrusted_text,
    tool_catalog_text,
)

__all__ = [
    "ModelProvider",
    # 从 prompt_safety re-export(见模块 docstring)。
    "fence_context_as_data",
    "fence_evidence_as_data",
    "query_args_help",
    "sanitize_analyzer_results",
    "sanitize_untrusted_text",
    "tool_catalog_text",
]
class ModelProvider(abc.ABC):
    """抽象 provider。各实现在交回结果之前,**必须**用所声明的 Pydantic 类型
    校验自己的输出。"""

    name: str = "abstract"

    @abc.abstractmethod
    async def quick_triage(
        self, context: IncidentContext
    ) -> tuple[TriageResult, ModelUsage]:
        ...

    @abc.abstractmethod
    async def build_plan(
        self,
        context: IncidentContext,
        triage: TriageResult,
        supplemental_from: SynthesisResult | None = None,
    ) -> tuple[InvestigationPlan, ModelUsage]:
        ...

    @abc.abstractmethod
    async def analyze(
        self,
        context: IncidentContext,
        spec: AnalyzerSpec,
        evidences: list[Evidence],
    ) -> tuple[AnalyzerResult, ModelUsage]:
        ...

    @abc.abstractmethod
    async def synthesize(
        self,
        context: IncidentContext,
        evidences: list[Evidence],
        analyzer_results: list[AnalyzerResult],
        round_index: int,
    ) -> tuple[SynthesisResult, ModelUsage]:
        ...
