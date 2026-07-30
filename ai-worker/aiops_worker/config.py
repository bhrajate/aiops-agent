"""从 ``AIOPS_`` 前缀的环境变量读取运行时配置。

刻意保持零依赖(只用 ``os.environ``),使导入本包时永远不需要可选 SDK。
参见 docs/INTEGRATION.md「环境变量」。
"""
from __future__ import annotations

import os
from dataclasses import dataclass, field

DEFAULT_ANTHROPIC_MODEL = "claude-opus-4-8[1M]"


class ConfigError(RuntimeError):
    """启动配置不可用于当前运行环境。由调用方 fail-fast。"""


# 每个字段都用 default_factory 而非直接的 os.environ.get(...)。
#
# dataclass 的字段默认值在**类定义时**求值一次,也就是 import 本模块的那一刻。
# 写成 `env: str = os.environ.get("AIOPS_ENV", ...)` 时,load_settings() 返回的
# 永远是导入瞬间的快照,之后改环境变量不再有任何效果 —— 而它的文档承诺的是
# "从当前环境变量构建"。生产下恰好无害(环境变量在进程启动前就位),但这会让
# 下面 validate() 的正确性取决于 import 顺序,而护栏不该依赖这种东西。
def _env(key: str, default: str = "") -> "field":
    return field(default_factory=lambda: os.environ.get(key, default))


@dataclass(frozen=True)
class Settings:
    # 运行环境。与 control-plane 的 config.IsProduction / cluster-agent 的
    # isProduction 同一口径(production | prod)。它是生产护栏的开关,不是日志标签。
    env: str = _env("AIOPS_ENV", "development")

    temporal_hostport: str = _env("AIOPS_TEMPORAL_HOSTPORT", "localhost:7233")
    temporal_namespace: str = _env("AIOPS_TEMPORAL_NAMESPACE", "default")
    task_queue: str = _env("AIOPS_TASK_QUEUE", "investigation-ai")

    # 控制面内部 API(唯一的数据库写入方)。AI worker 绝不直连数据库。
    control_internal_url: str = _env("AIOPS_CONTROL_INTERNAL_URL", "http://localhost:8090")

    model_provider: str = field(
        default_factory=lambda: os.environ.get("AIOPS_MODEL_PROVIDER", "mock").lower()
    )
    anthropic_api_key: str = _env("AIOPS_ANTHROPIC_API_KEY", "")
    anthropic_model: str = _env("AIOPS_ANTHROPIC_MODEL", DEFAULT_ANTHROPIC_MODEL)

    # 内部 API 客户端的 HTTP 超时(秒)。
    http_timeout_sec: float = field(
        default_factory=lambda: float(os.environ.get("AIOPS_HTTP_TIMEOUT_SEC", "15"))
    )

    # 单个 worker 并发执行的 activity 上限(架构文档 8.4)。它同时也是并发模型
    # 调用的上限 —— 一轮最多 5 个分析器并行,多条调查叠加时这是唯一的闸门。
    #
    # 为什么设在 worker 层而不是工作流里用 semaphore:被这个上限挡住的 activity
    # 处于「未开始」状态,start_to_close 计时还没启动;而 semaphore 是在 activity
    # **内部**等待,计时已经在跑 —— 排队久了会把正常任务拖成超时。
    #
    # 下限不能太低:record_phase / record_event 这类 I/O activity 与模型 activity
    # 共享槽位,若被长时间占满会撞上它们 30s 的超时并触发重试。
    max_concurrent_activities: int = field(
        default_factory=lambda: int(os.environ.get("AIOPS_MAX_CONCURRENT_ACTIVITIES", "16"))
    )

    # 内部 API 共享密钥(SECURITY §2),通过 X-Internal-Token 头发送。
    internal_token: str = _env("AIOPS_INTERNAL_TOKEN", "")

    # 可观测性(架构 §16):OTLP 端点(host:port)。为空表示关闭。
    otlp_endpoint: str = _env("AIOPS_OTLP_ENDPOINT", "")
    service_name: str = _env("AIOPS_SERVICE_NAME", "aiops-ai-worker")

    def is_production(self) -> bool:
        """是否处于生产模式(触发严格校验)。口径与 Go 侧两个组件保持一致。"""
        return self.env.strip().lower() in ("production", "prod")

    def validate(self) -> None:
        """启动校验:生产模式下拒绝会产出虚假产物的配置。

        目前只有一条,理由与控制面拒绝 ``AIOPS_OBS_DATASOURCE=mock``、
        cluster-agent 拒绝 mock 数据源相同:``MockProvider`` 返回的是**编造的**
        假设与诊断结论。它不报错、不超时、schema 完全合法,一路写进 incident 的
        诊断里 —— 值班人员没有任何线索能看出这份根因不是模型分析出来的。

        而 ``model_provider`` 的默认值恰好就是 ``mock``,所以漏配和显式配 mock
        是同一种失败,一并拒绝。
        """
        if not self.is_production():
            return
        if self.model_provider == "mock":
            raise ConfigError(
                "AIOPS_ENV=production 下不允许 AIOPS_MODEL_PROVIDER=mock"
                "(会产出编造的假设与诊断结论):请设为 anthropic,"
                "或仅在非生产环境使用 mock"
            )


def load_settings() -> Settings:
    """从当前环境变量构建一份新的 Settings 快照。"""
    return Settings()
