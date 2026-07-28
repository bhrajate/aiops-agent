"""Seed Golden Cases for offline replay (architecture 18.2).

Five reproducible cases covering the four first-version fault classes
(architecture 6.2): 发布回归 / Pod 异常 / 资源瓶颈 / 依赖超时. Each carries a
``signal_fixture`` (incident stub + signals) so replay is deterministic, plus
``expected_top_causes`` keywords the diagnosis must surface.

These are the Python form loaded by the CLI's default. A SQL form
(seed_golden_cases.sql) inserts the same cases into ``golden_cases`` for the
``--from-db`` path.
"""
from __future__ import annotations

from .models import GoldenCase, SignalFixture


def _fixture(incident_id: str, fault_category: str, severity: str,
             component: dict, signals: list[dict], with_change: bool = False,
             blast: dict | None = None) -> SignalFixture:
    incident = {
        "incident_id": incident_id,
        "version": 1,
        "status": "closed",
        "severity": severity,
        "fault_category": fault_category,
        "affected_resources": [component],
        "blast_radius": blast or {"services": 1, "namespaces": 1},
        "change_refs": (["chg-" + incident_id] if with_change else []),
    }
    return SignalFixture(incident=incident, signals=signals)


SEED_GOLDEN_CASES: list[GoldenCase] = [
    # 1) 发布回归 (release regression)
    GoldenCase(
        case_id="gc-release-001",
        incident_id="inc-release-001",
        fault_category="release_regression",
        root_cause="checkout 新版本连接池配置回归导致依赖调用排队、5xx 上升",
        affected_component="payment/checkout",
        expected_top_causes=["新版本", "连接池", "错误率"],
        signal_fixture=_fixture(
            "inc-release-001", "release_regression", "P2",
            {"kind": "Deployment", "name": "checkout", "namespace": "payment"},
            [
                {"signal_id": "s-r1", "cluster_id": "prod-cn-1", "source": "cicd",
                 "signal_type": "change",
                 "labels": {"event": "rollout", "version": "v2.3.0"}},
                {"signal_id": "s-r2", "cluster_id": "prod-cn-1", "source": "alertmanager",
                 "signal_type": "alert", "severity": "critical",
                 "labels": {"alertname": "HighErrorRate", "release": "v2.3.0"}},
            ],
            with_change=True,
            blast={"services": 2, "namespaces": 1},
        ),
    ),
    # 2) 资源瓶颈 (resource saturation) — explicit fault_category
    GoldenCase(
        case_id="gc-resource-001",
        incident_id="inc-resource-001",
        fault_category="resource_saturation",
        root_cause="订单服务内存接近 limit 触发 OOMKill 与 CPU throttling",
        affected_component="orders/order-api",
        expected_top_causes=["OOMKill", "throttling", "资源"],
        signal_fixture=_fixture(
            "inc-resource-001", "resource_saturation", "P2",
            {"kind": "Deployment", "name": "order-api", "namespace": "orders"},
            [
                {"signal_id": "s-c1", "cluster_id": "prod-cn-1", "source": "alertmanager",
                 "signal_type": "alert", "severity": "warning",
                 "labels": {"alertname": "CPUThrottlingHigh", "resource": "cpu"}},
            ],
        ),
    ),
    # 3) 依赖超时 (dependency failure)
    GoldenCase(
        case_id="gc-dependency-001",
        incident_id="inc-dependency-001",
        fault_category="dependency_failure",
        root_cause="下游支付网关超时导致上游 checkout 级联失败",
        affected_component="payment/checkout",
        expected_top_causes=["依赖", "超时", "级联"],
        signal_fixture=_fixture(
            "inc-dependency-001", "dependency_failure", "P1",
            {"kind": "Deployment", "name": "checkout", "namespace": "payment"},
            [
                {"signal_id": "s-d1", "cluster_id": "prod-cn-1", "source": "alertmanager",
                 "signal_type": "alert", "severity": "critical",
                 "labels": {"alertname": "DownstreamTimeout", "dependency": "pay-gw"}},
            ],
            blast={"services": 3, "namespaces": 2},
        ),
    ),
    # 4) Pod 异常 (CrashLoop / config) — mapped to config_error scenario
    GoldenCase(
        case_id="gc-pod-001",
        incident_id="inc-pod-001",
        fault_category="config_error",
        root_cause="错误的 ConfigMap 连接串导致 Pod CrashLoopBackOff",
        affected_component="catalog/catalog-svc",
        expected_top_causes=["配置", "连接串"],
        signal_fixture=_fixture(
            "inc-pod-001", "config_error", "P2",
            {"kind": "Deployment", "name": "catalog-svc", "namespace": "catalog"},
            [
                {"signal_id": "s-p1", "cluster_id": "prod-cn-1", "source": "kubernetes",
                 "signal_type": "event",
                 "labels": {"reason": "CrashLoopBackOff", "configmap": "catalog-cfg"}},
            ],
            with_change=True,
        ),
    ),
    # 5) 资源瓶颈,通过关键词推断得出(不显式给 fault_category)——
    #    用于检验 infer_fault_category 能否从信号标签(OOMKilled)推断类别。
    GoldenCase(
        case_id="gc-pod-oom-002",
        incident_id="inc-pod-oom-002",
        fault_category="resource_saturation",
        root_cause="内存泄漏导致 Pod 反复 OOMKilled",
        affected_component="search/search-api",
        expected_top_causes=["OOMKill", "资源", "重启"],
        signal_fixture=SignalFixture(
            incident={
                "incident_id": "inc-pod-oom-002",
                "version": 1,
                "status": "closed",
                "severity": "P2",
                # 故意不写 fault_category -> 交由信号推断得出。
                "affected_resources": [
                    {"kind": "Deployment", "name": "search-api", "namespace": "search"}
                ],
                "blast_radius": {"services": 1, "namespaces": 1},
            },
            signals=[
                {"signal_id": "s-o1", "cluster_id": "prod-cn-1", "source": "kubernetes",
                 "signal_type": "event",
                 "labels": {"reason": "OOMKilled", "alertname": "PodOOMKilled"}},
            ],
        ),
    ),
]


def load_seed_cases() -> list[GoldenCase]:
    """返回一份新的 seed 用例副本(已通过校验)。"""
    return [GoldenCase.model_validate(c.model_dump()) for c in SEED_GOLDEN_CASES]
