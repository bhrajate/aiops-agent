"""Model Gateway:可插拔的模型 provider(架构 12.1)。

Agent 不绑定具体厂商,由 ``AIOPS_MODEL_PROVIDER`` 选择具体实现。
``MockProvider`` 完全确定性且不需要 API key,因此整条 InvestigationWorkflow
可以完全离线端到端运行。
"""
from __future__ import annotations

from ..config import Settings
from .base import ModelProvider
from .mock import MockProvider


def build_provider(settings: Settings) -> ModelProvider:
    """按配置选择 provider 的工厂函数。对 Anthropic provider 采用惰性导入,
    使 ``anthropic`` SDK 保持为可选依赖。"""
    provider = settings.model_provider
    if provider == "mock":
        return MockProvider()
    if provider == "anthropic":
        from .anthropic_provider import AnthropicProvider

        return AnthropicProvider(
            api_key=settings.anthropic_api_key, model=settings.anthropic_model
        )
    if provider == "pydantic-ai":
        # 与 anthropic 走同一个模型与密钥,只有结构化输出管线不同 ——
        # 这样两者可以在真实流量上对比,而变量只有那一处。
        from .pydantic_ai_provider import PydanticAIProvider

        return PydanticAIProvider(
            api_key=settings.anthropic_api_key, model=settings.anthropic_model
        )
    raise ValueError(f"unknown AIOPS_MODEL_PROVIDER: {provider!r}")


# 会产出**编造内容**的 provider。生产护栏拒绝它们(见 config.Settings.validate)。
# 单独列出而不是写死 "mock":新增 provider 时应显式判断它属于哪一类,
# 而不是靠"不叫 mock 就安全"这个默认。
FABRICATING_PROVIDERS = frozenset({"mock"})

# 会真的调用模型的 provider。生产必须是其中之一。
REAL_PROVIDERS = frozenset({"anthropic", "pydantic-ai"})


__all__ = [
    "FABRICATING_PROVIDERS",
    "REAL_PROVIDERS",
    "ModelProvider",
    "MockProvider",
    "build_provider",
]
