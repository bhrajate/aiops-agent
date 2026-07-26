UV ?= uv
export UV_INDEX_URL ?= https://mirrors.aliyun.com/pypi/simple/

.PHONY: install run test lint fmt clean

install:
	$(UV) sync --extra dev

# Install with the optional anthropic SDK too.
install-anthropic:
	$(UV) sync --extra dev --extra anthropic

run:
	$(UV) run aiops-worker

test:
	$(UV) run pytest -q

# Prove the workflow module imports cleanly (no syntax/import errors).
check-import:
	$(UV) run python -c "import aiops_worker.workflow; print('workflow import OK')"

clean:
	rm -rf .venv .pytest_cache **/__pycache__ .uv
