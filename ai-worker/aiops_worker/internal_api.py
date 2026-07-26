"""Async HTTP client for the control-plane internal API (`:8090`).

The AI worker NEVER touches the database. It reads context and writes back
phase/events/hypotheses/diagnosis/usage exclusively through these endpoints
(docs/INTEGRATION.md "内部 API"). Tool invocation is also proxied here: the
gateway injects scope, validates authz, calls cluster-agent, redacts, then
persists and returns an Evidence record.
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
    """Raised when the gateway denies a tool invocation (policy/authz).

    The denial reason is structured data, not something the model can rewrite
    to bypass (architecture 9.2 / 14.2)."""

    def __init__(self, tool: str, reason: str):
        super().__init__(f"tool {tool!r} denied: {reason}")
        self.tool = tool
        self.reason = reason


class InternalAPIClient:
    def __init__(self, base_url: str, timeout_sec: float = 15.0):
        self._base = base_url.rstrip("/")
        self._timeout = timeout_sec

    async def _post(self, path: str, json_body: dict[str, Any]) -> dict[str, Any]:
        async with httpx.AsyncClient(timeout=self._timeout) as client:
            resp = await client.post(self._base + path, json=json_body)
            resp.raise_for_status()
            return resp.json() if resp.content else {}

    async def _get(self, path: str) -> dict[str, Any]:
        async with httpx.AsyncClient(timeout=self._timeout) as client:
            resp = await client.get(self._base + path)
            resp.raise_for_status()
            return resp.json()

    # -- reads ---------------------------------------------------------------

    async def load_context(self, investigation_id: str) -> IncidentContext:
        data = await self._get(f"/internal/investigations/{investigation_id}/context")
        return IncidentContext.model_validate(data)

    # -- tool invocation (proxied to gateway -> cluster-agent) ---------------

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
        self, investigation_id: str, event_type: str, payload: dict[str, Any]
    ) -> None:
        await self._post(
            f"/internal/investigations/{investigation_id}/events",
            {"event_type": event_type, "payload": payload},
        )

    async def put_hypotheses(
        self, investigation_id: str, hypotheses: list[Hypothesis]
    ) -> None:
        # Full replacement (docs/INTEGRATION.md).
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
