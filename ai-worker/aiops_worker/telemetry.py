"""AI worker 的 OpenTelemetry 链路追踪(架构 §16)。

可选且惰性:未设置 ``AIOPS_OTLP_ENDPOINT`` 时,``build_interceptors`` 返回空列表,
也不需要任何 OTel 依赖。设置后会装配带 OTLP HTTP exporter 的 TracerProvider,
并由 Temporal 内置的 ``TracingInterceptor`` 把 workflow/activity 的 span 挂到与
Go 控制面同一条 trace 上(通过标准的 W3C traceparent 传播)。
"""
from __future__ import annotations

import logging
from typing import Any, Sequence

logger = logging.getLogger("aiops_worker.telemetry")


def build_interceptors(otlp_endpoint: str, service_name: str) -> Sequence[Any]:
    """返回用于链路追踪的 Temporal interceptor(关闭或不可用时返回空列表)。"""
    if not otlp_endpoint:
        return []
    try:
        from opentelemetry import trace
        from opentelemetry.exporter.otlp.proto.http.trace_exporter import (
            OTLPSpanExporter,
        )
        from opentelemetry.sdk.resources import Resource
        from opentelemetry.sdk.trace import TracerProvider
        from opentelemetry.sdk.trace.export import BatchSpanProcessor
        from temporalio.contrib.opentelemetry import TracingInterceptor

        endpoint = otlp_endpoint
        if not endpoint.startswith("http"):
            endpoint = f"http://{endpoint}/v1/traces"
        provider = TracerProvider(resource=Resource.create({"service.name": service_name}))
        provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter(endpoint=endpoint)))
        trace.set_tracer_provider(provider)
        logger.info("otlp tracing enabled endpoint=%s", endpoint)
        return [TracingInterceptor()]
    except Exception as exc:  # pragma: no cover - 可选依赖 / 网络问题
        logger.warning("otlp tracing unavailable, continuing without: %s", exc)
        return []
