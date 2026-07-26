"""OpenTelemetry tracing for the AI worker (architecture §16).

Optional and lazy: when ``AIOPS_OTLP_ENDPOINT`` is unset, ``build_interceptors``
returns an empty list and no OTel dependency is required. When set, a
TracerProvider with an OTLP HTTP exporter is installed and Temporal's built-in
``TracingInterceptor`` links workflow/activity spans into the same trace as the
Go control plane (propagated via the standard W3C traceparent).
"""
from __future__ import annotations

import logging
from typing import Any, Sequence

logger = logging.getLogger("aiops_worker.telemetry")


def build_interceptors(otlp_endpoint: str, service_name: str) -> Sequence[Any]:
    """Return Temporal interceptors for tracing (empty if disabled/unavailable)."""
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
    except Exception as exc:  # pragma: no cover - optional dep / network
        logger.warning("otlp tracing unavailable, continuing without: %s", exc)
        return []
