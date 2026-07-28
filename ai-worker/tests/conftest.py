"""共用的测试 fixture 与构造器。不依赖真实 Temporal 服务,也不走网络。"""
from __future__ import annotations

from aiops_worker.contracts import (
    Evidence,
    Incident,
    IncidentContext,
    ResourceRef,
    Signal,
)


def make_context(
    fault_category: str = "release_regression",
    severity: str = "P2",
    with_change: bool = True,
) -> IncidentContext:
    incident = Incident(
        incident_id="inc-123",
        version=1,
        status="open",
        severity=severity,
        fault_category=fault_category,
        affected_resources=[
            ResourceRef(kind="Deployment", name="checkout", namespace="payment")
        ],
        blast_radius={"services": 3, "namespaces": 1},
        change_refs=(["chg-1"] if with_change else []),
    )
    signals = [
        Signal(
            signal_id="sig-1",
            tenant_id="default",
            cluster_id="prod-cn-1",
            source="alertmanager",
            signal_type="alert",
            severity="critical",
            labels={"alertname": "HighErrorRate", "rule_id": "r-123"},
        )
    ]
    return IncidentContext(incident=incident, signals=signals)


def make_evidence(evidence_id: str, etype: str = "metric") -> Evidence:
    return Evidence(
        evidence_id=evidence_id,
        type=etype,
        source="prometheus",
        tool_name="query_metrics",
        summary=f"证据 {evidence_id}: checkout 5xx 错误率从 0.1% 升至 8%",
        redaction_status="clean",
    )
