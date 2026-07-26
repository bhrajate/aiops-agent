"""Model Gateway: pluggable model providers (architecture 12.1).

The Agent is not bound to a vendor. ``AIOPS_MODEL_PROVIDER`` selects the
implementation. ``MockProvider`` is fully deterministic and needs no API key,
so the whole InvestigationWorkflow runs end-to-end offline.
"""
from __future__ import annotations

from ..config import Settings
from .base import ModelProvider
from .mock import MockProvider


def build_provider(settings: Settings) -> ModelProvider:
    """Factory selecting a provider from settings. Imports the Anthropic
    provider lazily so the ``anthropic`` SDK stays an optional dependency."""
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
