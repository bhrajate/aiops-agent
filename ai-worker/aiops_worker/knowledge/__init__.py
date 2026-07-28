"""知识服务:基于 pgvector 的语义 RAG(架构 12.2)。

知识库中存放的是**参考知识**(runbook、架构文档、服务目录、经评审的历史故障、
复盘报告)。参考知识只能用于启发假设 / 建议查询方向 —— 永远不能用来**证明**根因。
这条边界由上游按证据类型(``type=knowledge``)强制;本包只负责索引与相似度检索。

包含两部分:
- :mod:`aiops_worker.knowledge.embeddings` —— ``EmbeddingProvider`` 抽象,
  以及确定性的 ``MockEmbeddingProvider``(无需 API key)。
- :mod:`aiops_worker.knowledge.store` —— 通过 pgvector 余弦距离
  (``embedding <=> query``)读写 ``knowledge_items`` 的 embedding。
"""
from __future__ import annotations

from .embeddings import (
    EMBEDDING_DIM,
    EmbeddingProvider,
    MockEmbeddingProvider,
    build_embedding_provider,
)

__all__ = [
    "EMBEDDING_DIM",
    "EmbeddingProvider",
    "MockEmbeddingProvider",
    "build_embedding_provider",
]
