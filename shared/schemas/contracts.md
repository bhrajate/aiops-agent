# 共享数据契约(Frozen Contracts)

对应架构设计文档第 10 节。Go 控制面与 Python AI Worker 均据此实现。DDL 见 [`../sql/001_schema.sql`](../sql/001_schema.sql)。

## 事件总线 Topic

| Topic | Key | 生产者 | 消费者 |
|---|---|---|---|
| `signals` | `signal_id` | Signal Ingress | Incident Manager |
| `incidents` | `incident_id` | Incident Manager | Trigger Policy Engine |
| `investigations` | `investigation_id` | API / Trigger | (审计、通知) |

## Signal(信号)

```jsonc
{
  "signal_id": "sig-...",
  "tenant_id": "default",
  "cluster_id": "prod-cn-1",
  "source": "alertmanager",          // alertmanager | kubernetes | cicd | itsm | slo
  "signal_type": "alert",            // alert | change | event | resolved
  "resource_ref": { "namespace": "payment", "kind": "Deployment", "name": "checkout", "uid": "..." },
  "severity": "critical",
  "starts_at": "2026-07-26T10:00:00Z",
  "ends_at": null,
  "labels": { "alertname": "HighErrorRate", "rule_id": "r-123" },
  "payload_ref": "s3://.../raw",
  "payload_hash": "sha256:..."
}
```

## Incident(事件)

幂等/聚合键:`tenant_id / cluster_id / namespace / resource_uid / signal_type / rule_id`

```jsonc
{
  "incident_id": "inc-...",
  "version": 3,
  "grouping_key": "...",
  "status": "open",                  // open | acknowledged | resolved | closed
  "severity": "P2",                  // P1 | P2 | P3 | P4
  "fault_category": "release_regression",
  "affected_resources": [ { "kind": "Deployment", "name": "checkout", "namespace": "payment" } ],
  "blast_radius": { "services": 3, "namespaces": 1 },
  "topology_refs": [],
  "change_refs": [],
  "first_seen": "...",
  "last_seen": "..."
}
```

## Investigation(调查)

`phase` 状态机(文档第 7.3 节):
`queued → triaging → (triage_published | planning) → collecting → synthesizing → (concluded | needs_human) → waiting_feedback → closed`,以及任意阶段可 `cancelled`。

`budget` / `usage`(文档第 8.4 节有界执行):
```jsonc
{
  "budget": { "max_duration_sec": 300, "max_rounds": 3, "max_tokens": 200000,
              "max_cost_usd": 2.0, "max_tool_calls": 20 },
  "usage":  { "elapsed_sec": 0, "rounds": 0, "tokens": 0, "cost_usd": 0, "tool_calls": 0 }
}
```

## Evidence(证据,不可变)

```jsonc
{
  "evidence_id": "ev-...",
  "type": "metric",                  // metric | log | trace | kubernetes | change | knowledge
  "source": "prometheus",
  "tool_name": "query_metrics",
  "query": { "expr": "...", "redacted": true },
  "time_range": { "from": "...", "to": "..." },
  "summary": "checkout 新版本实例 5xx 错误率从 0.1% 升至 8%",
  "raw_ref": "s3://.../ev-...",
  "content_hash": "sha256:...",
  "freshness": "10s",
  "redaction_status": "clean"        // clean | redacted
}
```

## Hypothesis(假设)

```jsonc
{
  "hypothesis_id": "hyp-...",
  "rank": 1,
  "statement": "新版本连接池配置导致依赖请求排队",
  "component_ref": { "kind": "Deployment", "name": "checkout" },
  "confidence": 0.68,
  "supporting_evidence_ids": ["ev-10", "ev-18"],
  "contradicting_evidence_ids": ["ev-21"],
  "missing_evidence": ["新旧版本实例级连接池指标"],
  "status": "supported"              // proposed | supported | rejected | unresolved
}
```

## DiagnosisResult(诊断结果,文档第 10.6 节)

```jsonc
{
  "incident_id": "inc-123",
  "status": "unresolved",            // resolved | unresolved | inconclusive
  "confirmed_facts": [],
  "hypotheses": [
    { "rank": 1, "statement": "...", "confidence": 0.68,
      "supporting_evidence_ids": ["ev-10"], "contradicting_evidence_ids": ["ev-21"] }
  ],
  "missing_information": ["新旧版本实例级连接池指标"],
  "next_actions": ["按版本维度查询连接池等待时间"],
  "remediation_proposal": null       // 首版恒为 null(默认只读)
}
```

## 类型化只读工具(文档第 9.1 节)

Cluster Agent 暴露、Tool Gateway 代理:
```
get_workload_state | get_kubernetes_events | query_metrics | search_logs
get_traces | list_recent_changes | inspect_dependencies | retrieve_runbook
```
