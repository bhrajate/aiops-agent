"""Batch runner: replay a set of golden cases and aggregate metrics.

Kept separate from the CLI so it is importable/testable without argparse.
"""
from __future__ import annotations

from typing import Optional

from ..model_gateway.base import ModelProvider
from .metrics import aggregate, score_outcome
from .models import EvaluationRunSummary, GoldenCase
from .pipeline import OfflineReplayPipeline


async def run_evaluation(
    cases: list[GoldenCase],
    provider: Optional[ModelProvider] = None,
    *,
    model_version: str = "mock",
    prompt_version: str = "v1",
    policy_version: str = "v1",
    tenant_id: str = "default",
) -> EvaluationRunSummary:
    """Replay every case and return an aggregated run summary."""
    pipeline = OfflineReplayPipeline(provider=provider)
    results = []
    for case in cases:
        context = case.signal_fixture.to_context()
        outcome = await pipeline.replay(case.case_id, context)
        results.append(
            score_outcome(
                case_id=case.case_id,
                fault_category=case.fault_category,
                expected_top_causes=case.expected_top_causes,
                outcome=outcome,
            )
        )
    return aggregate(
        results,
        tenant_id=tenant_id,
        model_version=model_version,
        prompt_version=prompt_version,
        policy_version=policy_version,
    )
