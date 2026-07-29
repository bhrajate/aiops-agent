"""评估服务的数据库访问层(依赖可选的 ``db`` extra)。

读取 ``golden_cases``,写入 ``evaluation_runs``(migrations/000002_hardening)。AI worker 平时
只通过控制面访问数据库;而评估服务属于离线/运维作业,因此第一版允许它直连写库
(任务书明确允许在此处使用 asyncpg/psycopg)。``psycopg`` 采用惰性导入,
使导入本模块时并不需要它。
"""
from __future__ import annotations

import json
import os
from typing import Optional

from .models import EvaluationRunSummary, GoldenCase, SignalFixture

DEFAULT_DSN_ENV = "AIOPS_DB_DSN"


def _connect(dsn: str):
    try:
        import psycopg  # noqa: WPS433 (惰性导入:可选依赖)
    except ImportError as exc:  # pragma: no cover - 环境守卫
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
    """从 ``golden_cases`` 表加载黄金用例。"""
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
    """向 ``evaluation_runs`` 插入一行,并返回其 run_id。

    ``detail`` 保存按类别的拆分统计与逐用例结果,使一次运行完全可复现、可审计。
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
