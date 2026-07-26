"""MockEmbeddingProvider: determinism, dimension, normalization, semantics."""
from __future__ import annotations

import math

from aiops_worker.knowledge.embeddings import (
    EMBEDDING_DIM,
    MockEmbeddingProvider,
    build_embedding_provider,
    to_pgvector_literal,
)


def _cos(a: list[float], b: list[float]) -> float:
    return sum(x * y for x, y in zip(a, b))


def test_dimension_is_1536():
    p = MockEmbeddingProvider()
    assert p.dim == EMBEDDING_DIM == 1536
    assert len(p.embed("发布回归导致依赖超时")) == 1536


def test_deterministic_same_text_same_vector():
    p = MockEmbeddingProvider()
    v1 = p.embed("checkout 5xx error rate spike")
    v2 = p.embed("checkout 5xx error rate spike")
    assert v1 == v2  # exact equality, no randomness/clock


def test_two_instances_agree():
    a = MockEmbeddingProvider().embed("OOMKilled pod restart")
    b = MockEmbeddingProvider().embed("OOMKilled pod restart")
    assert a == b


def test_vector_is_unit_normalized():
    p = MockEmbeddingProvider()
    v = p.embed("资源瓶颈 CPU throttling 内存 limit")
    norm = math.sqrt(sum(x * x for x in v))
    assert abs(norm - 1.0) < 1e-9


def test_empty_text_is_zero_vector():
    v = MockEmbeddingProvider().embed("")
    assert len(v) == 1536
    assert all(x == 0.0 for x in v)


def test_similar_text_ranks_above_unrelated():
    p = MockEmbeddingProvider()
    query = p.embed("新版本 发布 回归 依赖 超时")
    related = p.embed("发布回归导致依赖超时的排查手册")
    unrelated = p.embed("Pod CrashLoopBackOff 处置手册 探针 镜像")
    assert _cos(query, related) > _cos(query, unrelated)


def test_batch_matches_single():
    p = MockEmbeddingProvider()
    texts = ["a b c", "依赖 超时"]
    batch = p.embed_batch(texts)
    assert batch == [p.embed(t) for t in texts]


def test_build_provider_default_is_mock():
    p = build_embedding_provider()
    assert p.name == "mock"


def test_build_provider_unknown_raises():
    import pytest

    with pytest.raises(ValueError):
        build_embedding_provider("does-not-exist")


def test_stub_providers_route_but_not_implemented():
    import pytest

    for name in ("anthropic", "openai", "local"):
        p = build_embedding_provider(name)
        assert p.name == name
        with pytest.raises(NotImplementedError):
            p.embed("x")


def test_pgvector_literal_format():
    lit = to_pgvector_literal([0.5, -0.25, 0.0])
    assert lit.startswith("[") and lit.endswith("]")
    assert lit.count(",") == 2
