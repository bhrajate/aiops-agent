UV ?= uv
export UV_INDEX_URL ?= https://mirrors.aliyun.com/pypi/simple/

.PHONY: install run test lint fmt clean evaluate reindex

install:
	$(UV) sync --extra dev

# 连可选的 anthropic SDK 一起安装。
install-anthropic:
	$(UV) sync --extra dev --extra anthropic

run:
	$(UV) run aiops-worker

test:
	$(UV) run --extra dev pytest -q

# 验证 workflow 模块可以干净导入(无语法/导入错误)。
check-import:
	$(UV) run python -c "import aiops_worker.workflow; print('workflow import OK')"

# 在内置 seed 黄金用例上做离线评估重放(无需数据库)。
# 配好 AIOPS_DB_DSN 后可加 --from-db --write,把结果持久化到 evaluation_runs。
evaluate:
	$(UV) run python -m aiops_worker.evaluation.run

# 重建 knowledge_items 的 pgvector embedding(需要 AIOPS_DB_DSN 与 db extra)。
reindex:
	$(UV) run python -m aiops_worker.knowledge.reindex --query "发布回归导致依赖超时"

clean:
	rm -rf .venv .pytest_cache **/__pycache__ .uv
