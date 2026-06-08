# syntax=docker/dockerfile:1.7

# Target on-prem x86_64 GPU servers. On arm64 hosts (Apple Silicon) the build
# runs under qemu emulation, which is slow but produces deployment-correct
# binaries; on x86_64 hosts --platform is a no-op.

# ---- Stage 1: build whisper.cpp (whisper-cli) with Vulkan backend ----
# Ubuntu 24.04 (noble) ships a recent glslc that handles the coopmat shader
# syntax ggml-vulkan emits. Bookworm's package is too old; LunarG doesn't
# publish arm64. The runtime stage must use the same (or newer) base, since
# glibc / libstdc++ are backward-compatible only — a noble-built binary won't
# load against bookworm's older glibc.
FROM --platform=linux/amd64 ubuntu:24.04 AS whisper-build
ARG WHISPER_CPP_REF=v1.7.4
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential cmake git ca-certificates \
        libvulkan-dev glslc \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth 1 --branch ${WHISPER_CPP_REF} https://github.com/ggerganov/whisper.cpp.git .
RUN cmake -B build \
        -DCMAKE_BUILD_TYPE=Release \
        -DGGML_VULKAN=ON \
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
# Matches the whisper-build base (ubuntu:24.04) so whisper-cli's glibc /
# libstdc++ symbol requirements (GLIBC_2.38, GLIBCXX_3.4.32) are satisfied.
FROM --platform=linux/amd64 ubuntu:24.04 AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg ca-certificates libgomp1 libstdc++6 \
        libvulkan1 mesa-vulkan-drivers \
    && rm -rf /var/lib/apt/lists/*

# Allow the NVIDIA Container Toolkit to expose the proprietary Vulkan ICD
# when running on NVIDIA hosts. Harmless on AMD/Intel (no nvidia runtime → ignored).
ENV NVIDIA_VISIBLE_DEVICES=all \
    NVIDIA_DRIVER_CAPABILITIES=compute,utility,graphics

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
