# AI Worker image. Uses uv for fast, reproducible installs.
FROM python:3.11-slim AS base

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    UV_INDEX_URL=https://mirrors.aliyun.com/pypi/simple/ \
    UV_LINK_MODE=copy

# uv (static binary).
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

WORKDIR /app

# Install deps first for layer caching.
COPY pyproject.toml README.md ./
COPY aiops_worker ./aiops_worker
RUN uv sync --no-dev

# Non-root runtime.
RUN useradd --create-home --uid 10001 worker
USER worker

# No inbound ports: the worker dials out to Temporal + control-plane.
ENV AIOPS_MODEL_PROVIDER=mock
CMD ["uv", "run", "aiops-worker"]
