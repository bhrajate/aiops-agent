"""基于 pgvector 的知识索引与语义检索。

写入 ``knowledge_items.embedding``,并按余弦距离查询
(``ORDER BY embedding <=> query``)。``psycopg``(v3)是可选依赖(``db`` extra);
导入本模块时并不需要它 —— 只有真正执行 store 方法时才会建立连接。

生产环境中 AI **worker** 是通过控制面内部 API 的 ``retrieve_runbook`` 获取知识。
本模块是供 ``reindex`` CLI 与控制面侧索引使用的离线/运维路径,
同时也是证明 pgvector 检索端到端可用的地方。
"""
from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Any, Optional

from .embeddings import EMBEDDING_DIM, EmbeddingProvider, to_pgvector_literal

DEFAULT_DSN_ENV = "AIOPS_DB_DSN"


@dataclass(frozen=True)
class KnowledgeHit:
    """一条语义检索结果。``distance`` 是取值区间 [0,2] 的余弦距离;
    ``score = 1 - distance`` 即余弦相似度,便于直接使用。"""

    knowledge_id: str
    kind: str
    title: str
    content: str
    distance: float

    @property
    def score(self) -> float:
        return 1.0 - self.distance


def _normalize_dsn(dsn: str) -> str:
    """psycopg 对 ``?sslmode=disable`` 在各种 URL 形式下的理解方式与 libpq 并不完全
    一致,但它接受标准的 libpq URI。docs/INTEGRATION.md 中给出的 DSN 就是 libpq URI,
    因此原样透传即可。"""
    return dsn


class KnowledgeStore:
    """同步的 pgvector 存储层。之所以保持同步(psycopg 的默认模式),是因为 reindex
    属于 CLI / 运维作业,并不在高频请求路径上。"""

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
            import psycopg  # noqa: WPS433 (惰性导入:可选依赖)
        except ImportError as exc:  # pragma: no cover - 环境守卫
            raise RuntimeError(
                "psycopg is required for KnowledgeStore; install the 'db' extra: "
                "uv sync --extra db"
            ) from exc
        return psycopg.connect(self._dsn)

    # -- indexing ------------------------------------------------------------

    def reindex_all(self, tenant_id: Optional[str] = None) -> int:
        """为所有 knowledge_items 重新计算 embedding 并 UPDATE 该列。

        对 ``title + '\\n' + content`` 做 embedding,使标题也占一定权重。返回被更新的
        行数。该操作幂等:可以反复执行。
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
        """按余弦距离做语义检索。只考虑已有 embedding 的行;没有 embedding 的行
        必须先重建索引。"""
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
        """返回 (总条目数, 已有 embedding 的条目数)。"""
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
