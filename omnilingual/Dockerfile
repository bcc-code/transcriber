# syntax=docker/dockerfile:1.7

# Meta Omnilingual ASR sidecar. The Go transcriber talks to it over HTTP
# (OMNILINGUAL_URL); see README.md in this directory.
#
# No CUDA base image: the PyPI torch wheel bundles its own CUDA runtime
# (torch 2.8.0 → CUDA 12.8), and the NVIDIA Container Toolkit injects the
# driver. The host driver must be new enough for that runtime; see DEPLOY.md.

FROM python:3.12-slim-bookworm

# libsndfile: fairseq2's audio stack. ffmpeg: decodes whatever the caller uploads.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg libsndfile1 curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=ghcr.io/astral-sh/uv:0.12.15 /uv /usr/local/bin/uv

WORKDIR /app
ENV UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    UV_PROJECT_ENVIRONMENT=/opt/venv
COPY pyproject.toml uv.lock ./
RUN uv sync --locked --no-dev

COPY server.py regions.py ./

# fairseq2 downloads checkpoints to ~/.cache/fairseq2/assets (several GB for
# the 7B models). HOME=/cache puts that on the mounted volume.
ENV HOME=/cache \
    PATH=/opt/venv/bin:$PATH \
    NVIDIA_VISIBLE_DEVICES=all \
    NVIDIA_DRIVER_CAPABILITIES=compute,utility \
    OMNI_MODEL_CARD=omniASR_LLM_7B_v2 \
    OMNI_BATCH_SIZE=4
RUN mkdir -p /cache

EXPOSE 8000
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8000"]
