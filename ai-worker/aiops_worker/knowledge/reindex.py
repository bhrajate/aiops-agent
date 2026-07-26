"""CLI: rebuild pgvector embeddings for knowledge_items.

    python -m aiops_worker.knowledge.reindex [--query "..."] [--kind runbook]

Reads the DSN from ``AIOPS_DB_DSN`` and the embedding provider from
``AIOPS_EMBEDDING_PROVIDER`` (default ``mock``). After reindexing it optionally
runs a sample semantic query so you can eyeball retrieval quality.
"""
from __future__ import annotations

import argparse
import os
import sys

from .embeddings import build_embedding_provider
from .store import DEFAULT_DSN_ENV, KnowledgeStore


def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="python -m aiops_worker.knowledge.reindex",
        description="Rebuild pgvector embeddings for knowledge_items.",
    )
    p.add_argument("--dsn", default=os.environ.get(DEFAULT_DSN_ENV, ""),
                   help=f"database DSN (default ${DEFAULT_DSN_ENV})")
    p.add_argument("--tenant", default=None, help="restrict to a tenant_id")
    p.add_argument("--provider", default=None,
                   help="embedding provider (default $AIOPS_EMBEDDING_PROVIDER or mock)")
    p.add_argument("--query", default=None,
                   help="after reindex, run this semantic query and print hits")
    p.add_argument("--kind", default=None, help="filter --query by knowledge kind")
    p.add_argument("--top-k", type=int, default=5, help="hits to show for --query")
    return p


def main(argv: list[str] | None = None) -> int:
    args = _build_parser().parse_args(argv)
    if not args.dsn:
        print(f"error: no DSN. Set {DEFAULT_DSN_ENV} or pass --dsn", file=sys.stderr)
        return 2

    provider = build_embedding_provider(args.provider)
    store = KnowledgeStore(provider, dsn=args.dsn)

    total_before, indexed_before = store.count_indexed(args.tenant)
    print(f"embedding provider = {provider.name} (dim={provider.dim})")
    print(f"before: {indexed_before}/{total_before} items have an embedding")

    updated = store.reindex_all(tenant_id=args.tenant)
    total_after, indexed_after = store.count_indexed(args.tenant)
    print(f"reindexed {updated} item(s); now {indexed_after}/{total_after} indexed")

    if args.query:
        print(f"\nsemantic search for: {args.query!r}")
        hits = store.search(args.query, top_k=args.top_k, kind=args.kind,
                            tenant_id=args.tenant)
        if not hits:
            print("  (no hits)")
        for i, h in enumerate(hits, 1):
            print(f"  {i}. score={h.score:.4f} [{h.kind}] {h.title}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
