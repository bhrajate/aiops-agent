"""CLI: offline evaluation replay + metric report (architecture 18).

    python -m aiops_worker.evaluation.run [--from-db] [--write] [--json]

Loads golden cases (seed fixtures by default, or ``golden_cases`` table with
``--from-db``), replays each through the RCA pipeline, prints the quality-gate
report, and optionally writes the run to ``evaluation_runs`` (``--write``).

Runs fully offline with the default mock provider -- no Temporal, no network.
"""
from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys

from ..model_gateway import build_provider
from ..config import load_settings
from .metrics import gate_report
from .models import EvaluationRunSummary
from .runner import run_evaluation
from .seed_cases import load_seed_cases


def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="python -m aiops_worker.evaluation.run",
        description="Offline RCA evaluation replay over golden cases.",
    )
    p.add_argument("--from-db", action="store_true",
                   help="load golden cases from the golden_cases table (else seed fixtures)")
    p.add_argument("--dsn", default=os.environ.get("AIOPS_DB_DSN", ""),
                   help="database DSN (default $AIOPS_DB_DSN)")
    p.add_argument("--tenant", default="default", help="tenant_id")
    p.add_argument("--write", action="store_true",
                   help="write the run summary to evaluation_runs")
    p.add_argument("--json", action="store_true",
                   help="print the full summary as JSON instead of a table")
    return p


def _print_report(summary: EvaluationRunSummary) -> None:
    print("=" * 66)
    print(" AIOps 离线评测报告 (Offline Evaluation)")
    print("=" * 66)
    print(f" 用例总数 total_cases         : {summary.total_cases}")
    print(f" Top-1 命中               : {summary.top1_hits}/{summary.total_cases}"
          f"  ({summary.top1_rate:.0%})")
    print(f" Top-3 命中               : {summary.top3_hits}/{summary.total_cases}"
          f"  ({summary.top3_rate:.0%})")
    print(f" 关键结论证据引用率        : {summary.evidence_citation_rate:.0%}")
    print(f" 无证据支撑根因比例(幻觉率): {summary.hallucination_rate:.2%}")
    print(f" P95 首次诊断耗时          : {summary.p95_first_diag_sec*1000:.1f} ms")
    print("-" * 66)
    print(" 分故障类别 Top-1/Top-3:")
    for cat, c in summary.detail.get("by_category", {}).items():
        print(f"   {cat:22s} top1={c['top1']}/{c['total']} top3={c['top3']}/{c['total']}")
    print("-" * 66)
    print(" 逐用例:")
    for r in summary.results:
        hit = "T1" if r.top1_hit else ("T3" if r.top3_hit else "--")
        print(f"   [{hit}] {r.case_id:20s} status={r.diagnosis_status:12s}"
              f" matched={r.matched_keyword or '-'}")
    print("-" * 66)
    print(" 质量门槛 (architecture 18.1):")
    for gate, ok in gate_report(summary).items():
        print(f"   {'PASS' if ok else 'FAIL'}  {gate}")
    print("=" * 66)


def main(argv: list[str] | None = None) -> int:
    args = _build_parser().parse_args(argv)

    if args.from_db:
        if not args.dsn:
            print("error: --from-db requires --dsn or $AIOPS_DB_DSN", file=sys.stderr)
            return 2
        from .store import load_golden_cases_from_db
        cases = load_golden_cases_from_db(dsn=args.dsn, tenant_id=args.tenant)
        if not cases:
            print("error: no approved golden cases found in DB", file=sys.stderr)
            return 1
    else:
        cases = load_seed_cases()

    settings = load_settings()
    provider = build_provider(settings)
    summary = asyncio.run(
        run_evaluation(
            cases,
            provider=provider,
            model_version=provider.name,
            tenant_id=args.tenant,
        )
    )

    if args.write:
        if not args.dsn:
            print("error: --write requires --dsn or $AIOPS_DB_DSN", file=sys.stderr)
            return 2
        from .store import write_evaluation_run
        run_id = write_evaluation_run(summary, dsn=args.dsn)
        summary.run_id = run_id

    if args.json:
        print(summary.model_dump_json(indent=2))
    else:
        _print_report(summary)
        if summary.run_id:
            print(f" evaluation_runs.run_id = {summary.run_id}")

    # Exit non-zero if any release gate fails -> usable in CI.
    gates = gate_report(summary)
    return 0 if all(gates.values()) else 1


if __name__ == "__main__":
    raise SystemExit(main())
