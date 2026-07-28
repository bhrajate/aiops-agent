"""评估服务:离线重放 + 质量闸门(架构 18)。

在**不依赖**真实 Temporal 服务的情况下,把 RCA 推理流水线跑在一组黄金用例上
(它直接调用同一批确定性策略函数与 ModelProvider),随后计算第一版的质量闸门指标
(架构 18.1):

- Top-1 / Top-3 根因命中率;
- 关键结论的证据引用率(目标 100%);
- 无依据根因占比,即幻觉率(目标 < 5%);
- 首次诊断延迟 P95(目标 < 5 分钟)。

运行汇总会写入 ``evaluation_runs`` 表。
"""
from __future__ import annotations

from .models import (
    EvaluationResult,
    EvaluationRunSummary,
    GoldenCase,
)

__all__ = [
    "EvaluationResult",
    "EvaluationRunSummary",
    "GoldenCase",
]
