"""Evaluation service: offline replay + quality gates (architecture 18).

Drives the RCA reasoning pipeline over a set of Golden Cases *without* a real
Temporal server (it calls the same deterministic policy functions + a
ModelProvider directly), then computes the first-version quality-gate metrics
(architecture 18.1):

- Top-1 / Top-3 root-cause hit rate;
- key-conclusion evidence citation rate (target 100%);
- unsupported-root-cause ratio == hallucination rate (target < 5%);
- P95 first-diagnosis latency (target < 5 min).

Run summaries are written to the ``evaluation_runs`` table.
"""
from __future__ import annotations

from .models import (
    EvaluationResult,
    EvaluationRunSummary,
    GoldenCase,
)

__all__ = [
    "EvaluationResult",
    "EvaluationRunSummary",
    "GoldenCase",
]
