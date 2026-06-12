# syntax=docker/dockerfile:1.7

# Target on-prem x86_64 GPU servers. On arm64 hosts (Apple Silicon) the build
# runs under qemu emulation, which is slow but produces deployment-correct
# binaries; on x86_64 hosts --platform is a no-op.

# ---- Stage 1: build whisper.cpp (whisper-cli) with CUDA backend ----
# Pinned to the on-prem host's GPU stack: NVIDIA only. CUDA is whisper.cpp's
# most-optimized backend (1.5–2× over Vulkan on NVIDIA). The runtime stage
# uses the matching nvidia/cuda runtime image so libcudart / libcublas etc.
# are available without polluting the host. Local Mac dev still works in
# CPU-only mode (`-whispercpp-no-gpu`) — the CUDA libs are present but
# unused.
ARG CUDA_VERSION=12.6.3
FROM --platform=linux/amd64 nvidia/cuda:${CUDA_VERSION}-devel-ubuntu24.04 AS whisper-build
ARG WHISPER_CPP_REF=v1.8.6
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential cmake git ca-certificates pkg-config \
        libopenblas-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth 1 --branch ${WHISPER_CPP_REF} https://github.com/ggerganov/whisper.cpp.git .
# BLAS stays on for the CPU fallback path (`-whispercpp-no-gpu`); on CUDA
# hosts it's unused at runtime, so the only cost is image size.
#
# CMAKE_CUDA_ARCHITECTURES must be pinned: ggml-cuda's default is `native`,
# which queries nvidia-smi on the build host — docker build has no GPU, so
# the build would fail. Pinned to sm_86 for the on-prem RTX 3090 (Ampere).
# If the deployment GPU changes, update this — building for the wrong arch
# either falls back to PTX JIT at startup (slow) or fails outright.
ARG CUDA_ARCHS="86"
RUN cmake -B build \
        -DCMAKE_BUILD_TYPE=Release \
        -DGGML_CUDA=ON \
        -DCMAKE_CUDA_ARCHITECTURES="${CUDA_ARCHS}" \
        -DGGML_BLAS=ON \
        -DGGML_BLAS_VENDOR=OpenBLAS \
        -DWHISPER_BUILD_TESTS=OFF \
        -DWHISPER_BUILD_EXAMPLES=ON \
        -DBUILD_SHARED_LIBS=ON \
    && cmake --build build --config Release -j \
    && cmake --install build --prefix /opt/whisper

# ---- Stage 2: build frontend (Nuxt → static) ----
FROM --platform=linux/amd64 node:22-bookworm-slim AS frontend-build
# Pin pnpm v10: lockfile is v9.0 (created by pnpm 9), pnpm 10 reads it natively.
# Avoids the pnpm v11 "approved builds" gate which requires interactive
# pnpm approve-builds to allow esbuild / @parcel/watcher native postinstalls.
RUN npm install -g pnpm@10
WORKDIR /src/frontend
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY frontend/ ./
RUN pnpm generate

# ---- Stage 3: build Go binary ----
FROM --platform=linux/amd64 golang:1.26-bookworm AS go-build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
# Replace the embedded dist with the freshly built SPA.
RUN rm -rf internal/web/dist && mkdir -p internal/web/dist
COPY --from=frontend-build /src/frontend/.output/public/ ./internal/web/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/transcriber ./cmd/transcriber

# ---- Stage 4: runtime ----
# Matches the whisper-build base so glibc / libstdc++ and CUDA runtime libs
# (libcudart, libcublas) are present without bundling the full toolkit.
FROM --platform=linux/amd64 nvidia/cuda:${CUDA_VERSION}-runtime-ubuntu24.04 AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg ca-certificates libgomp1 libstdc++6 \
        libopenblas0-pthread \
    && rm -rf /var/lib/apt/lists/*

# NVIDIA Container Toolkit reads these to expose the right device files and
# driver libraries. `compute,utility` is all CUDA needs — we dropped `graphics`
# along with the Vulkan backend.
ENV NVIDIA_VISIBLE_DEVICES=all \
    NVIDIA_DRIVER_CAPABILITIES=compute,utility

# Model cache: $XDG_CACHE_HOME/transcriber/hf/<repo>/<file>
ENV XDG_CACHE_HOME=/var/cache \
    WHISPER_CPP_BIN=/usr/local/bin/whisper-cli
RUN mkdir -p /var/cache/transcriber/hf

# `cmake --install` lays out whisper-cli + the libwhisper/libggml shared libs
# it dynamically links against under one prefix. Drop it into /usr/local so the
# binary and libs land on the default $PATH / loader path.
COPY --from=whisper-build /opt/whisper/ /usr/local/
RUN ldconfig
COPY --from=go-build /out/transcriber /usr/local/bin/transcriber

WORKDIR /app
EXPOSE 8888

# `prompt.txt` is optional; mount one in if you want a default prompt.
ENTRYPOINT ["/usr/local/bin/transcriber"]
CMD ["-port=8888", "-workers=2", "-default-model=whisper-cpp-large-v3", "-log-format=json"]
