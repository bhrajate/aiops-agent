"""控制面内部 API(`:8090`)的异步 HTTP 客户端。

AI worker **绝不**直连数据库。它只通过这些端点读取上下文,并回写
phase/events/hypotheses/diagnosis/usage(docs/INTEGRATION.md「内部 API」)。
工具调用同样在此代理:网关注入 scope、校验授权、调用 cluster-agent、脱敏,
然后持久化并返回一条 Evidence 记录。
"""
from __future__ import annotations

from typing import Any, Optional

import httpx

from .contracts import (
    DiagnosisResult,
    Evidence,
    Hypothesis,
    IncidentContext,
    Usage,
)


class ToolDenied(Exception):
    """网关拒绝某次工具调用(策略 / 授权原因)时抛出。

    拒绝原因是结构化数据,而不是模型可以改写以绕过的东西(架构 9.2 / 14.2)。"""

    def __init__(self, tool: str, reason: str):
        super().__init__(f"tool {tool!r} denied: {reason}")
        self.tool = tool
        self.reason = reason


class InternalAPIClient:
    def __init__(self, base_url: str, timeout_sec: float = 15.0, internal_token: str = ""):
        self._base = base_url.rstrip("/")
        self._timeout = timeout_sec
        # 内部 API 共享密钥(SECURITY §2);为空则不发送(开发未启用时兼容)。
        self._headers = {"X-Internal-Token": internal_token} if internal_token else {}

    async def _post(self, path: str, json_body: dict[str, Any]) -> dict[str, Any]:
        async with httpx.AsyncClient(timeout=self._timeout) as client:
            resp = await client.post(self._base + path, json=json_body, headers=self._headers)
            resp.raise_for_status()
            return resp.json() if resp.content else {}

    async def _get(self, path: str) -> dict[str, Any]:
        async with httpx.AsyncClient(timeout=self._timeout) as client:
            resp = await client.get(self._base + path, headers=self._headers)
            resp.raise_for_status()
            return resp.json()

    # -- reads ---------------------------------------------------------------

    async def load_context(self, investigation_id: str) -> IncidentContext:
        data = await self._get(f"/internal/investigations/{investigation_id}/context")
        return IncidentContext.model_validate(data)

    # -- 工具调用(代理到 gateway -> cluster-agent) --------------------------

    async def invoke_tool(
        self,
        investigation_id: str,
        incident_id: str,
        tool: str,
        arguments: dict[str, Any],
        scope: Optional[dict[str, Any]] = None,
    ) -> Evidence:
        body = {
            "investigation_id": investigation_id,
            "incident_id": incident_id,
            "tool": tool,
            "arguments": arguments,
        }
        if scope is not None:
            body["scope"] = scope
        data = await self._post("/internal/tools/invoke", body)
        if data.get("status") == "denied":
            raise ToolDenied(tool, data.get("reason", "unspecified"))
        return Evidence.model_validate(data["evidence"])

    # -- writes --------------------------------------------------------------

    async def set_phase(self, investigation_id: str, phase: str) -> None:
        await self._post(
            f"/internal/investigations/{investigation_id}/phase", {"phase": phase}
        )

    async def emit_event(
        self,
        investigation_id: str,
        event_type: str,
        payload: dict[str, Any],
        idempotency_key: str = "",
    ) -> None:
        # ``idempotency_key`` 让控制面能对 Temporal activity 重试所导致的重复事件
        # 去重(同一逻辑事件 -> 同一 key)。若控制面尚未实现去重,该字段也无副作用。
        body: dict[str, Any] = {"event_type": event_type, "payload": payload}
        if idempotency_key:
            body["idempotency_key"] = idempotency_key
        await self._post(
            f"/internal/investigations/{investigation_id}/events",
            body,
        )

    async def put_hypotheses(
        self, investigation_id: str, hypotheses: list[Hypothesis]
    ) -> None:
        # 整体替换(docs/INTEGRATION.md)。
        await self._post(
            f"/internal/investigations/{investigation_id}/hypotheses",
            {"hypotheses": [h.model_dump(mode="json") for h in hypotheses]},
        )

    async def put_diagnosis(
        self, investigation_id: str, diagnosis: DiagnosisResult, phase: str
    ) -> None:
        await self._post(
            f"/internal/investigations/{investigation_id}/diagnosis",
            {"diagnosis": diagnosis.model_dump(mode="json"), "phase": phase},
        )

    async def put_usage(self, investigation_id: str, usage: Usage) -> None:
        await self._post(
            f"/internal/investigations/{investigation_id}/usage",
            {"usage": usage.model_dump(mode="json")},
        )
