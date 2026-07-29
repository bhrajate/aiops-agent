"""Worker 入口:连接 Temporal,注册工作流与 Activity,并运行
``investigation-ai`` 任务队列。

使用 Pydantic 数据转换器,使契约模型在传输时序列化为 JSON
(与 Go 控制面跨语言兼容)。
"""
from __future__ import annotations

import asyncio
import logging

from temporalio.client import Client
from temporalio.contrib.pydantic import pydantic_data_converter
from temporalio.worker import Worker

from .activities import InvestigationActivities
from .config import load_settings
from .model_gateway import build_provider
from .telemetry import build_interceptors
from .workflow import InvestigationWorkflow

logger = logging.getLogger("aiops_worker")


async def run_worker() -> None:
    settings = load_settings()
    provider = build_provider(settings)
    logger.info(
        "starting worker: temporal=%s ns=%s queue=%s provider=%s",
        settings.temporal_hostport,
        settings.temporal_namespace,
        settings.task_queue,
        provider.name,
    )

    client = await Client.connect(
        settings.temporal_hostport,
        namespace=settings.temporal_namespace,
        data_converter=pydantic_data_converter,
    )

    acts = InvestigationActivities(
        provider,
        http_timeout_sec=settings.http_timeout_sec,
        internal_token=settings.internal_token,
    )
    interceptors = build_interceptors(settings.otlp_endpoint, settings.service_name)
    worker = Worker(
        client,
        task_queue=settings.task_queue,
        interceptors=interceptors,
        max_concurrent_activities=settings.max_concurrent_activities,
        workflows=[InvestigationWorkflow],
        activities=[
            acts.load_incident_context,
            acts.run_quick_triage,
            acts.evaluate_deep_rca_policy,
            acts.build_investigation_plan,
            acts.build_supplemental_plan,
            acts.retrieve_runbooks,
            acts.run_analyzer,
            acts.synthesize_hypotheses,
            acts.publish_diagnosis,
            acts.record_phase,
            acts.record_event,
            acts.record_usage,
        ],
    )
    await worker.run()


def main() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    asyncio.run(run_worker())


if __name__ == "__main__":
    main()
