# AI Worker 镜像。使用 uv 以获得快速且可复现的安装过程。
FROM python:3.11-slim AS base

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    UV_INDEX_URL=https://mirrors.aliyun.com/pypi/simple/ \
    UV_LINK_MODE=copy

# uv(静态二进制)。
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

WORKDIR /app

# 先安装依赖,以利用镜像层缓存。
COPY pyproject.toml README.md ./
COPY aiops_worker ./aiops_worker
RUN uv sync --no-dev

# 以非 root 身份运行。
RUN useradd --create-home --uid 10001 worker
USER worker

# 不暴露入站端口:worker 只主动外连 Temporal 与控制面。
ENV AIOPS_MODEL_PROVIDER=mock
CMD ["uv", "run", "aiops-worker"]
