"""Embedding providers for the Knowledge Service (architecture 12.2).

The Model Gateway is not bound to a vendor, and neither is the embedding layer.
``AIOPS_EMBEDDING_PROVIDER`` selects the implementation; ``mock`` is the default
so indexing and semantic retrieval run end-to-end offline (tests / CI / demos).

Determinism contract (mirrors the MockProvider rules):
- No randomness, no clock reads, no network, no API key.
- The same text ALWAYS maps to the same 1536-d unit vector.
- Similar text (shared tokens) maps to nearby vectors under cosine distance,
  so ``ORDER BY embedding <=> query`` returns sensible neighbours.

Vector dimension is 1536 to match ``knowledge_items.embedding vector(1536)`` in
shared/sql/001_schema.sql.
"""
from __future__ import annotations

import abc
import hashlib
import os
import re

import numpy as np

# Must match knowledge_items.embedding vector(1536).
EMBEDDING_DIM = 1536

# Tokeniser: ASCII words/numbers OR single CJK characters. Keeping CJK per-char
# means Chinese runbooks (no whitespace) still yield overlapping tokens.
_TOKEN_RE = re.compile(r"[a-z0-9]+|[一-鿿]")

# Feature-hashing fan-out: each token contributes to this many dimensions.
# >1 reduces collision noise so cosine similarity tracks token overlap well.
_HASHES_PER_TOKEN = 4


def _tokenize(text: str) -> list[str]:
    return _TOKEN_RE.findall((text or "").lower())


class EmbeddingProvider(abc.ABC):
    """Abstract embedding provider.

    Implementations MUST return an ``EMBEDDING_DIM``-length list[float]. Batch
    embedding defaults to a loop over :meth:`embed` but providers backed by a
    real API should override it to batch network calls.
    """

    name: str = "abstract"
    dim: int = EMBEDDING_DIM

    @abc.abstractmethod
    def embed(self, text: str) -> list[float]:
        ...

    def embed_batch(self, texts: list[str]) -> list[list[float]]:
        return [self.embed(t) for t in texts]


class MockEmbeddingProvider(EmbeddingProvider):
    """Deterministic, dependency-free embeddings via signed feature hashing.

    Each token is hashed (SHA-256) into several (index, sign) slots of a
    ``dim``-length vector. The vector is L2-normalised so dot product == cosine
    similarity, which is what pgvector ``<=>`` (cosine distance) ranks on.
    """

    name = "mock"

    def __init__(self, dim: int = EMBEDDING_DIM):
        self.dim = dim

    def embed(self, text: str) -> list[float]:
        vec = np.zeros(self.dim, dtype=np.float64)
        tokens = _tokenize(text)
        for tok in tokens:
            digest = hashlib.sha256(tok.encode("utf-8")).digest()
            for h in range(_HASHES_PER_TOKEN):
                # 4 bytes -> dimension index, 1 byte -> sign. Distinct slices
                # per hash keep the fan-out decorrelated.
                off = h * 5
                idx = int.from_bytes(digest[off : off + 4], "big") % self.dim
                sign = 1.0 if digest[off + 4] & 1 else -1.0
                vec[idx] += sign
        norm = float(np.linalg.norm(vec))
        if norm > 0.0:
            vec /= norm
        return vec.tolist()


class _StubEmbeddingProvider(EmbeddingProvider):
    """Placeholder for a real hosted/local embedding model.

    TODO: implement against Anthropic / OpenAI / a local model. Kept as an
    explicit stub so ``build_embedding_provider`` can route to it once wired,
    without pretending it works today.
    """

    def __init__(self, name: str):
        self.name = name

    def embed(self, text: str) -> list[float]:  # pragma: no cover - stub
        raise NotImplementedError(
            f"embedding provider {self.name!r} is not implemented yet; "
            "set AIOPS_EMBEDDING_PROVIDER=mock"
        )


def build_embedding_provider(name: str | None = None) -> EmbeddingProvider:
    """Factory. Reads ``AIOPS_EMBEDDING_PROVIDER`` (default ``mock``)."""
    provider = (name or os.environ.get("AIOPS_EMBEDDING_PROVIDER", "mock")).lower()
    if provider == "mock":
        return MockEmbeddingProvider()
    if provider in {"anthropic", "openai", "local"}:
        return _StubEmbeddingProvider(provider)
    raise ValueError(f"unknown AIOPS_EMBEDDING_PROVIDER: {provider!r}")


def to_pgvector_literal(vec: list[float]) -> str:
    """Render a vector as a pgvector text literal: ``[0.1,0.2,...]``.

    Used when the ``pgvector`` psycopg adapter is unavailable; pgvector accepts
    this cast from text.
    """
    return "[" + ",".join(repr(float(x)) for x in vec) + "]"
