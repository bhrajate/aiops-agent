"""DB access for the Evaluation service (optional ``db`` extra).

Reads ``golden_cases`` and writes ``evaluation_runs`` (shared/sql/003). The AI
worker normally reaches the DB only through the control plane; the evaluation
service is an offline/admin job, so a direct writer is acceptable for the first
version (the task explicitly allows asyncpg/psycopg here). ``psycopg`` is
imported lazily so importing this module never requires it.
"""
from __future__ import annotations

import json
import os
from typing import Optional

from .models import EvaluationRunSummary, GoldenCase, SignalFixture

DEFAULT_DSN_ENV = "AIOPS_DB_DSN"


def _connect(dsn: str):
    try:
        import psycopg  # noqa: WPS433 (lazy: optional dependency)
    except ImportError as exc:  # pragma: no cover - env guard
        raise RuntimeError(
            "psycopg is required for evaluation DB access; install the 'db' "
            "extra: uv sync --extra db"
        ) from exc
    return psycopg.connect(dsn)


def load_golden_cases_from_db(
    dsn: Optional[str] = None,
    tenant_id: Optional[str] = None,
    only_approved: bool = True,
) -> list[GoldenCase]:
    """Load golden cases from the ``golden_cases`` table."""
    dsn = dsn or os.environ.get(DEFAULT_DSN_ENV, "")
    if not dsn:
        raise ValueError(f"no DSN: set {DEFAULT_DSN_ENV} or pass dsn=")
    sql = (
        "SELECT case_id, tenant_id, incident_id, fault_category, root_cause, "
        "       affected_component, signal_fixture, expected_top_causes, review_status "
        "FROM golden_cases"
    )
    conds: list[str] = []
    params: list[object] = []
    if only_approved:
        conds.append("review_status = 'approved'")
    if tenant_id:
        conds.append("tenant_id = %s")
        params.append(tenant_id)
    if conds:
        sql += " WHERE " + " AND ".join(conds)
    sql += " ORDER BY created_at"

    out: list[GoldenCase] = []
    with _connect(dsn) as conn:
        with conn.cursor() as cur:
            cur.execute(sql, params)
            for row in cur.fetchall():
                fixture = row[6] if isinstance(row[6], dict) else json.loads(row[6])
                expected = row[7] if isinstance(row[7], list) else json.loads(row[7])
                out.append(
                    GoldenCase(
                        case_id=row[0],
                        tenant_id=row[1],
                        incident_id=row[2],
                        fault_category=row[3],
                        root_cause=row[4],
                        affected_component=row[5],
                        signal_fixture=SignalFixture.model_validate(fixture),
                        expected_top_causes=expected,
                        review_status=row[8],
                    )
                )
    return out


def write_evaluation_run(
    summary: EvaluationRunSummary, dsn: Optional[str] = None
) -> str:
    """Insert one ``evaluation_runs`` row; return its run_id.

    ``detail`` stores the by-category breakdown and per-case results so a run is
    fully reproducible/auditable.
    """
    dsn = dsn or os.environ.get(DEFAULT_DSN_ENV, "")
    if not dsn:
        raise ValueError(f"no DSN: set {DEFAULT_DSN_ENV} or pass dsn=")

    detail = dict(summary.detail)
    detail["results"] = [r.model_dump() for r in summary.results]

    sql = (
        "INSERT INTO evaluation_runs "
        "(tenant_id, model_version, prompt_version, policy_version, total_cases, "
        " top1_hits, top3_hits, evidence_citation_rate, hallucination_rate, "
        " p95_first_diag_sec, detail) "
        "VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s) RETURNING run_id"
    )
    params = (
        summary.tenant_id,
        summary.model_version,
        summary.prompt_version,
        summary.policy_version,
        summary.total_cases,
        summary.top1_hits,
        summary.top3_hits,
        summary.evidence_citation_rate,
        summary.hallucination_rate,
        summary.p95_first_diag_sec,
        json.dumps(detail, ensure_ascii=False),
    )
    with _connect(dsn) as conn:
        with conn.cursor() as cur:
            cur.execute(sql, params)
            run_id = cur.fetchone()[0]
        conn.commit()
    return str(run_id)
