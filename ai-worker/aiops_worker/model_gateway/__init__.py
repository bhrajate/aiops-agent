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
    raise ValueError(f"unknown AIOPS_MODEL_PROVIDER: {provider!r}")


__all__ = ["ModelProvider", "MockProvider", "build_provider"]
