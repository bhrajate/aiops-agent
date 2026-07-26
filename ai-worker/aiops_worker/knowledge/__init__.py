"""Knowledge Service: pgvector semantic RAG (architecture 12.2).

The knowledge base holds *reference knowledge* (runbooks, architecture docs,
service catalog, reviewed historical incidents, postmortems). Reference
knowledge may only seed hypotheses / suggested queries -- it can never *prove* a
root cause. That boundary is enforced upstream by evidence type
(``type=knowledge``); this package only handles indexing + similarity search.

Two pieces:
- :mod:`aiops_worker.knowledge.embeddings` -- the ``EmbeddingProvider``
  abstraction with a deterministic ``MockEmbeddingProvider`` (no API key).
- :mod:`aiops_worker.knowledge.store` -- write/query ``knowledge_items``
  embeddings via pgvector cosine distance (``embedding <=> query``).
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
