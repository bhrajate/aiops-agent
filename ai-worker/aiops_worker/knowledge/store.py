"""pgvector-backed knowledge index + semantic retrieval.

Writes ``knowledge_items.embedding`` and queries by cosine distance
(``ORDER BY embedding <=> query``). ``psycopg`` (v3) is an optional dependency
(the ``db`` extra); importing this module never requires it -- the connection
is only opened when a store method runs.

The AI *worker* in production reaches knowledge via ``retrieve_runbook`` through
the control-plane internal API. This module is the offline/admin path used by
the ``reindex`` CLI and by control-plane-side indexing, and it is what proves
the pgvector retrieval works end-to-end.
"""
from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Any, Optional

from .embeddings import EMBEDDING_DIM, EmbeddingProvider, to_pgvector_literal

DEFAULT_DSN_ENV = "AIOPS_DB_DSN"


@dataclass(frozen=True)
class KnowledgeHit:
    """One semantic-search result. ``distance`` is cosine distance in [0,2];
    ``score = 1 - distance`` is cosine similarity for convenience."""

    knowledge_id: str
    kind: str
    title: str
    content: str
    distance: float

    @property
    def score(self) -> float:
        return 1.0 - self.distance


def _normalize_dsn(dsn: str) -> str:
    """psycopg does not understand ``?sslmode=disable`` on all URL forms the
    same way libpq does, but it does accept standard libpq URIs. The DSN in
    docs/INTEGRATION.md is a libpq URI, so pass it through unchanged."""
    return dsn


class KnowledgeStore:
    """Synchronous pgvector store. Kept sync (psycopg default) because the
    reindex path is a CLI/admin job, not a hot request path."""

    def __init__(self, provider: EmbeddingProvider, dsn: Optional[str] = None):
        self._provider = provider
        self._dsn = _normalize_dsn(dsn or os.environ.get(DEFAULT_DSN_ENV, ""))
        if not self._dsn:
            raise ValueError(
                f"no database DSN: set {DEFAULT_DSN_ENV} or pass dsn="
            )
        if provider.dim != EMBEDDING_DIM:
            raise ValueError(
                f"embedding provider dim {provider.dim} != column dim {EMBEDDING_DIM}"
            )

    def _connect(self):
        try:
            import psycopg  # noqa: WPS433 (lazy: optional dependency)
        except ImportError as exc:  # pragma: no cover - env guard
            raise RuntimeError(
                "psycopg is required for KnowledgeStore; install the 'db' extra: "
                "uv sync --extra db"
            ) from exc
        return psycopg.connect(self._dsn)

    # -- indexing ------------------------------------------------------------

    def reindex_all(self, tenant_id: Optional[str] = None) -> int:
        """(Re)compute embeddings for all knowledge_items and UPDATE the column.

        Embeds ``title + '\\n' + content`` so titles carry weight. Returns the
        number of rows updated. Idempotent: safe to run repeatedly.
        """
        rows = self._load_items(tenant_id)
        if not rows:
            return 0
        updated = 0
        with self._connect() as conn:
            with conn.cursor() as cur:
                for kid, title, content in rows:
                    vec = self._provider.embed(f"{title}\n{content}")
                    cur.execute(
                        "UPDATE knowledge_items SET embedding = %s::vector "
                        "WHERE knowledge_id = %s",
                        (to_pgvector_literal(vec), kid),
                    )
                    updated += cur.rowcount
            conn.commit()
        return updated

    def _load_items(self, tenant_id: Optional[str]):
        sql = "SELECT knowledge_id, title, content FROM knowledge_items"
        params: list[Any] = []
        if tenant_id:
            sql += " WHERE tenant_id = %s"
            params.append(tenant_id)
        sql += " ORDER BY created_at"
        with self._connect() as conn:
            with conn.cursor() as cur:
                cur.execute(sql, params)
                return cur.fetchall()

    # -- retrieval -----------------------------------------------------------

    def search(
        self,
        query: str,
        top_k: int = 5,
        kind: Optional[str] = None,
        tenant_id: Optional[str] = None,
    ) -> list[KnowledgeHit]:
        """Semantic search by cosine distance. Only rows with an embedding are
        considered; rows without one must be reindexed first."""
        qvec = to_pgvector_literal(self._provider.embed(query))
        sql = (
            "SELECT knowledge_id, kind, title, content, "
            "       (embedding <=> %s::vector) AS distance "
            "FROM knowledge_items "
            "WHERE embedding IS NOT NULL"
        )
        params: list[Any] = [qvec]
        if kind:
            sql += " AND kind = %s"
            params.append(kind)
        if tenant_id:
            sql += " AND tenant_id = %s"
            params.append(tenant_id)
        sql += " ORDER BY embedding <=> %s::vector LIMIT %s"
        params.extend([qvec, top_k])

        with self._connect() as conn:
            with conn.cursor() as cur:
                cur.execute(sql, params)
                out = [
                    KnowledgeHit(
                        knowledge_id=r[0],
                        kind=r[1],
                        title=r[2],
                        content=r[3],
                        distance=float(r[4]),
                    )
                    for r in cur.fetchall()
                ]
        return out

    def count_indexed(self, tenant_id: Optional[str] = None) -> tuple[int, int]:
        """Return (total_items, items_with_embedding)."""
        sql = (
            "SELECT count(*), count(embedding) FROM knowledge_items"
        )
        params: list[Any] = []
        if tenant_id:
            sql += " WHERE tenant_id = %s"
            params.append(tenant_id)
        with self._connect() as conn:
            with conn.cursor() as cur:
                cur.execute(sql, params)
                row = cur.fetchone()
        return (int(row[0]), int(row[1]))
