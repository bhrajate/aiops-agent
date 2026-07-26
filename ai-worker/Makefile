UV ?= uv
export UV_INDEX_URL ?= https://mirrors.aliyun.com/pypi/simple/

.PHONY: install run test lint fmt clean evaluate reindex

install:
	$(UV) sync --extra dev

# Install with the optional anthropic SDK too.
install-anthropic:
	$(UV) sync --extra dev --extra anthropic

run:
	$(UV) run aiops-worker

test:
	$(UV) run --extra dev pytest -q

# Prove the workflow module imports cleanly (no syntax/import errors).
check-import:
	$(UV) run python -c "import aiops_worker.workflow; print('workflow import OK')"

# Offline evaluation replay over the seed golden cases (no DB needed).
# Add --from-db --write once AIOPS_DB_DSN is set to persist to evaluation_runs.
evaluate:
	$(UV) run python -m aiops_worker.evaluation.run

# Rebuild pgvector embeddings for knowledge_items (requires AIOPS_DB_DSN + db extra).
reindex:
	$(UV) run python -m aiops_worker.knowledge.reindex --query "发布回归导致依赖超时"

clean:
	rm -rf .venv .pytest_cache **/__pycache__ .uv
