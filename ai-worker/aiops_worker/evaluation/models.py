"""评估服务使用的 Pydantic 模型。

与 shared/sql/003_hardening.sql 中的 ``golden_cases`` 和 ``evaluation_runs``
两张表对齐。``signal_fixture`` 承载可复现的输入(incident 桩 + 信号),
以保证同一用例每次重放结果完全一致。
"""
from __future__ import annotations

from typing import Any, Optional

from pydantic import BaseModel, ConfigDict, Field, field_validator

from ..contracts import IncidentContext


class SignalFixture(BaseModel):
    """单个黄金用例的可复现重放输入。

    对应控制面在已关闭故障单上会保存的内容:incident 桩 + 其归一化后的信号。
    重放时会被转换为 :class:`IncidentContext`。
    """

    model_config = ConfigDict(extra="allow")
    incident: dict[str, Any] = Field(default_factory=dict)
    signals: list[dict[str, Any]] = Field(default_factory=list)
    topology: list[Any] = Field(default_factory=list)
    changes: list[Any] = Field(default_factory=list)

    def to_context(self) -> IncidentContext:
        return IncidentContext.model_validate(
            {
                "incident": self.incident,
                "signals": self.signals,
                "topology": self.topology,
                "changes": self.changes,
            }
        )


class GoldenCase(BaseModel):
    """已标注并经评审的离线重放用例(架构 18.2)。

    只有 ``review_status == 'approved'`` 的用例才应参与发布卡点;保留该字段
    以便加载器过滤。
    """

    model_config = ConfigDict(extra="allow")
    case_id: str
    tenant_id: str = "default"
    incident_id: Optional[str] = None
    fault_category: str
    root_cause: str
    affected_component: Optional[str] = None
    signal_fixture: SignalFixture
    expected_top_causes: list[str] = Field(default_factory=list)
    review_status: str = "approved"

    @field_validator("expected_top_causes")
    @classmethod
    def _at_least_one_expected(cls, v: list[str]) -> list[str]:
        # 黄金用例必须明确「命中根因」的判定标准,否则命中率无从定义。
        # expected_top_causes 为空属于契约错误。
        cleaned = [s for s in (x.strip() for x in v) if s]
        if not cleaned:
            raise ValueError("expected_top_causes must contain >=1 keyword")
        return cleaned


class EvaluationResult(BaseModel):
    """单个用例的重放结果与评分。"""

    model_config = ConfigDict(extra="allow")
    case_id: str
    fault_category: str
    diagnosis_status: str
    predicted_causes: list[str] = Field(default_factory=list)  # 按排名排列的结论陈述
    matched_keyword: Optional[str] = None
    top1_hit: bool = False
    top3_hit: bool = False
    # 关键结论(即 supported 假设)的证据记账。
    supported_conclusions: int = 0
    supported_with_evidence: int = 0
    unsupported_root_causes: int = 0
    first_diag_sec: float = 0.0
    notes: str = ""


class EvaluationRunSummary(BaseModel):
    """一次评估运行的聚合指标 -> 对应 ``evaluation_runs`` 表的一行。"""

    model_config = ConfigDict(extra="allow")
    run_id: Optional[str] = None
    tenant_id: str = "default"
    model_version: str = "mock"
    prompt_version: str = "v1"
    policy_version: str = "v1"
    total_cases: int = 0
    top1_hits: int = 0
    top3_hits: int = 0
    evidence_citation_rate: float = 0.0
    hallucination_rate: float = 0.0
    p95_first_diag_sec: float = 0.0
    detail: dict[str, Any] = Field(default_factory=dict)
    results: list[EvaluationResult] = Field(default_factory=list)

    @property
    def top1_rate(self) -> float:
        return self.top1_hits / self.total_cases if self.total_cases else 0.0

    @property
    def top3_rate(self) -> float:
        return self.top3_hits / self.total_cases if self.total_cases else 0.0
