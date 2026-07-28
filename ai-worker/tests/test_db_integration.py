"""可选的数据库集成测试(pgvector RAG + evaluation_runs 写入)。

除非通过 ``AIOPS_DB_DSN`` 配置了可访问的 Postgres 且安装了 ``db`` extra(psycopg),
否则这些测试会被跳过。本地开发用 DSN(docs/INTEGRATION.md):
    AIOPS_DB_DSN=postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable
"""
from __future__ import annotations

import os

import pytest

DSN = os.environ.get("AIOPS_DB_DSN", "")


def _db_available() -> bool:
    if not DSN:
        return False
    try:
        import psycopg  # noqa: WPS433
    except ImportError:
        return False
    try:
        with psycopg.connect(DSN, connect_timeout=2) as conn:
            with conn.cursor() as cur:
                cur.execute("select 1")
                cur.fetchone()
        return True
    except Exception:
        return False


pytestmark = pytest.mark.skipif(
    not _db_available(), reason="no reachable AIOPS_DB_DSN / psycopg"
)


def test_reindex_and_search_roundtrip():
    from aiops_worker.knowledge.embeddings import MockEmbeddingProvider
    from aiops_worker.knowledge.store import KnowledgeStore

    store = KnowledgeStore(MockEmbeddingProvider(), dsn=DSN)
    total, _ = store.count_indexed()
    if total == 0:
        pytest.skip("no knowledge_items seeded")

    updated = store.reindex_all()
    assert updated >= 1
    _, indexed = store.count_indexed()
    assert indexed >= 1

    hits = store.search("发布回归 依赖 超时", top_k=3)
    assert hits
    # 最佳命中应当是发布/依赖类 runbook,而不是某个无关条目。
    assert "发布" in hits[0].title or "依赖" in hits[0].title
    # 余弦距离按升序排列。
    dists = [h.distance for h in hits]
    assert dists == sorted(dists)


def test_write_evaluation_run_roundtrip():
    import psycopg

    from aiops_worker.evaluation.runner import run_evaluation
    from aiops_worker.evaluation.seed_cases import load_seed_cases
    from aiops_worker.evaluation.store import write_evaluation_run
    import asyncio

    summary = asyncio.run(run_evaluation(load_seed_cases()))
    run_id = write_evaluation_run(summary, dsn=DSN)
    assert run_id

    with psycopg.connect(DSN) as conn:
        with conn.cursor() as cur:
            cur.execute(
                "select total_cases, top1_hits, top3_hits, evidence_citation_rate "
                "from evaluation_runs where run_id = %s",
                (run_id,),
            )
            row = cur.fetchone()
    assert row is not None
    assert row[0] == summary.total_cases
    assert row[1] == summary.top1_hits


def test_load_golden_cases_from_db():
    from aiops_worker.evaluation.store import load_golden_cases_from_db

    cases = load_golden_cases_from_db(dsn=DSN)
    # 只有应用了 004 seed 数据才有意义;否则跳过。
    if not cases:
        pytest.skip("golden_cases table empty (apply 004_seed_golden_cases.sql)")
    for c in cases:
        assert c.expected_top_causes
        ctx = c.signal_fixture.to_context()
        assert ctx.incident.incident_id
