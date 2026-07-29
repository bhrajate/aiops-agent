"""知识服务使用的 embedding provider(架构 12.2)。

Model Gateway 不绑定具体厂商,embedding 层同样如此。
``AIOPS_EMBEDDING_PROVIDER`` 选择具体实现;默认是 ``mock``,因此索引与语义检索
都能完全离线端到端运行(测试 / CI / 演示)。

确定性契约(与 MockProvider 的规则一致):
- 不含随机数,不读时钟,不走网络,不需要 API key。
- 同一段文本**始终**映射到同一个 1536 维单位向量。
- 相似文本(有共同 token)在余弦距离下映射到相近的向量,
  因此 ``ORDER BY embedding <=> query`` 能返回合理的近邻。

向量维度取 1536,以匹配 control-plane/internal/migrate/migrations/000001_schema.up.sql 中的
``knowledge_items.embedding vector(1536)``。
"""
from __future__ import annotations

import abc
import hashlib
import os
import re

import numpy as np

# 必须与 knowledge_items.embedding vector(1536) 保持一致。
EMBEDDING_DIM = 1536

# 分词器:匹配 ASCII 单词/数字,或单个中日韩字符。中日韩按单字切分意味着
# 没有空格的中文 runbook 依然能产出可重叠的 token。
_TOKEN_RE = re.compile(r"[a-z0-9]+|[一-鿿]")

# 特征哈希的扇出度:每个 token 会贡献到这么多个维度上。
# 取值 >1 可降低哈希碰撞噪声,使余弦相似度更好地反映 token 重叠程度。
_HASHES_PER_TOKEN = 4


def _tokenize(text: str) -> list[str]:
    return _TOKEN_RE.findall((text or "").lower())


class EmbeddingProvider(abc.ABC):
    """抽象 embedding provider。

    各实现**必须**返回长度为 ``EMBEDDING_DIM`` 的 list[float]。批量 embedding 默认
    只是对 :meth:`embed` 做循环,但基于真实 API 的 provider 应重写它以合并网络调用。
    """

    name: str = "abstract"
    dim: int = EMBEDDING_DIM

    @abc.abstractmethod
    def embed(self, text: str) -> list[float]:
        ...

    def embed_batch(self, texts: list[str]) -> list[list[float]]:
        return [self.embed(t) for t in texts]


class MockEmbeddingProvider(EmbeddingProvider):
    """基于带符号特征哈希的确定性、零依赖 embedding。

    每个 token 经 SHA-256 哈希后落到 ``dim`` 维向量的若干个 (下标, 符号) 槽位上。
    向量做 L2 归一化,于是点积即等于余弦相似度 —— 这正是 pgvector 的 ``<=>``
    (余弦距离)排序所依据的量。
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
                # 4 字节 -> 维度下标,1 字节 -> 符号。每次哈希取不同的字节切片,
                # 使扇出的各分量彼此不相关。
                off = h * 5
                idx = int.from_bytes(digest[off : off + 4], "big") % self.dim
                sign = 1.0 if digest[off + 4] & 1 else -1.0
                vec[idx] += sign
        norm = float(np.linalg.norm(vec))
        if norm > 0.0:
            vec /= norm
        return vec.tolist()


class _StubEmbeddingProvider(EmbeddingProvider):
    """真实托管 / 本地 embedding 模型的占位实现。

    TODO:对接 Anthropic / OpenAI / 本地模型。这里保留为显式的桩,使
    ``build_embedding_provider`` 在真正接上之后可以直接路由过来,
    同时不假装它现在已经可用。
    """

    def __init__(self, name: str):
        self.name = name

    def embed(self, text: str) -> list[float]:  # pragma: no cover - 桩实现
        raise NotImplementedError(
            f"embedding provider {self.name!r} is not implemented yet; "
            "set AIOPS_EMBEDDING_PROVIDER=mock"
        )


def build_embedding_provider(name: str | None = None) -> EmbeddingProvider:
    """工厂函数。读取 ``AIOPS_EMBEDDING_PROVIDER``(默认 ``mock``)。"""
    provider = (name or os.environ.get("AIOPS_EMBEDDING_PROVIDER", "mock")).lower()
    if provider == "mock":
        return MockEmbeddingProvider()
    if provider in {"anthropic", "openai", "local"}:
        return _StubEmbeddingProvider(provider)
    raise ValueError(f"unknown AIOPS_EMBEDDING_PROVIDER: {provider!r}")


def to_pgvector_literal(vec: list[float]) -> str:
    """把向量渲染成 pgvector 的文本字面量:``[0.1,0.2,...]``。

    在 ``pgvector`` 的 psycopg 适配器不可用时使用;pgvector 支持从文本做此类型转换。
    """
    return "[" + ",".join(repr(float(x)) for x in vec) + "]"
