"""Pydantic models for the Evaluation service.

Aligned with the ``golden_cases`` and ``evaluation_runs`` tables in
shared/sql/003_hardening.sql. ``signal_fixture`` carries a reproducible input
(incident stub + signals) so a case replays identically every time.
"""
from __future__ import annotations

from typing import Any, Optional

from pydantic import BaseModel, ConfigDict, Field, field_validator

from ..contracts import IncidentContext


class SignalFixture(BaseModel):
    """Reproducible replay input for one golden case.

    Mirrors what the control plane would have stored on the closed incident:
    the incident stub + its normalised signals. Converted to an
    :class:`IncidentContext` for replay.
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
    """A labelled, reviewed case for offline replay (architecture 18.2).

    Only ``review_status == 'approved'`` cases should gate a release; the field
    is kept so the loader can filter.
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
        # A golden case must assert what "hitting the root cause" means, else
        # hit-rate is undefined. Empty expected_top_causes is a contract error.
        cleaned = [s for s in (x.strip() for x in v) if s]
        if not cleaned:
            raise ValueError("expected_top_causes must contain >=1 keyword")
        return cleaned


class EvaluationResult(BaseModel):
    """Per-case replay outcome + scoring."""

    model_config = ConfigDict(extra="allow")
    case_id: str
    fault_category: str
    diagnosis_status: str
    predicted_causes: list[str] = Field(default_factory=list)  # ranked statements
    matched_keyword: Optional[str] = None
    top1_hit: bool = False
    top3_hit: bool = False
    # Key-conclusion (supported hypothesis) evidence bookkeeping.
    supported_conclusions: int = 0
    supported_with_evidence: int = 0
    unsupported_root_causes: int = 0
    first_diag_sec: float = 0.0
    notes: str = ""


class EvaluationRunSummary(BaseModel):
    """Aggregate metrics for one evaluation run -> ``evaluation_runs`` row."""

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
