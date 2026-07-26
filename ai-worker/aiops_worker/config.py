"""Runtime configuration read from ``AIOPS_`` prefixed environment variables.

Kept dependency-free (plain ``os.environ``) so importing the package never
requires optional SDKs. See docs/INTEGRATION.md "环境变量".
"""
from __future__ import annotations

import os
from dataclasses import dataclass

DEFAULT_ANTHROPIC_MODEL = "claude-opus-4-8[1M]"


@dataclass(frozen=True)
class Settings:
    temporal_hostport: str = os.environ.get("AIOPS_TEMPORAL_HOSTPORT", "localhost:7233")
    temporal_namespace: str = os.environ.get("AIOPS_TEMPORAL_NAMESPACE", "default")
    task_queue: str = os.environ.get("AIOPS_TASK_QUEUE", "investigation-ai")

    # control-plane internal API (single DB writer). AI worker never touches DB.
    control_internal_url: str = os.environ.get(
        "AIOPS_CONTROL_INTERNAL_URL", "http://localhost:8090"
    )

    model_provider: str = os.environ.get("AIOPS_MODEL_PROVIDER", "mock").lower()
    anthropic_api_key: str = os.environ.get("AIOPS_ANTHROPIC_API_KEY", "")
    anthropic_model: str = os.environ.get("AIOPS_ANTHROPIC_MODEL", DEFAULT_ANTHROPIC_MODEL)

    # HTTP timeouts (seconds) for the internal API client.
    http_timeout_sec: float = float(os.environ.get("AIOPS_HTTP_TIMEOUT_SEC", "15"))

    # Analyzer concurrency ceiling (architecture doc 8.4).
    max_analyzer_concurrency: int = int(os.environ.get("AIOPS_MAX_ANALYZER_CONCURRENCY", "3"))


def load_settings() -> Settings:
    """Build a fresh Settings snapshot from the current environment."""
    return Settings()
