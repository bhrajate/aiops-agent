"""从 ``AIOPS_`` 前缀的环境变量读取运行时配置。

刻意保持零依赖(只用 ``os.environ``),使导入本包时永远不需要可选 SDK。
参见 docs/INTEGRATION.md「环境变量」。
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

    # 控制面内部 API(唯一的数据库写入方)。AI worker 绝不直连数据库。
    control_internal_url: str = os.environ.get(
        "AIOPS_CONTROL_INTERNAL_URL", "http://localhost:8090"
    )

    model_provider: str = os.environ.get("AIOPS_MODEL_PROVIDER", "mock").lower()
    anthropic_api_key: str = os.environ.get("AIOPS_ANTHROPIC_API_KEY", "")
    anthropic_model: str = os.environ.get("AIOPS_ANTHROPIC_MODEL", DEFAULT_ANTHROPIC_MODEL)

    # 内部 API 客户端的 HTTP 超时(秒)。
    http_timeout_sec: float = float(os.environ.get("AIOPS_HTTP_TIMEOUT_SEC", "15"))

    # 分析器并发上限(架构文档 8.4)。
    max_analyzer_concurrency: int = int(os.environ.get("AIOPS_MAX_ANALYZER_CONCURRENCY", "3"))

    # 内部 API 共享密钥(SECURITY §2),通过 X-Internal-Token 头发送。
    internal_token: str = os.environ.get("AIOPS_INTERNAL_TOKEN", "")

    # 可观测性(架构 §16):OTLP 端点(host:port)。为空表示关闭。
    otlp_endpoint: str = os.environ.get("AIOPS_OTLP_ENDPOINT", "")
    service_name: str = os.environ.get("AIOPS_SERVICE_NAME", "aiops-ai-worker")


def load_settings() -> Settings:
    """从当前环境变量构建一份新的 Settings 快照。"""
    return Settings()
