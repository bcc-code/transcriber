# syntax=docker/dockerfile:1.7

# Only the whisper.cpp stage and the runtime stage have to be the *target*
# architecture (they carry CUDA and the compiled whisper-cli). The frontend and
# Go stages run natively on the build host ($BUILDPLATFORM) and cross-compile,
# which keeps them out of qemu when building for amd64 from an arm64 Mac.
#
# Building the CUDA stage on an arm64 host still means qemu-emulating nvcc,
# which is slow and flaky — build on an x86_64 host or in CI (see
# .github/workflows/image.yml) and `docker compose pull` on-prem.
ARG ARCH=amd64
ARG CUDA_VERSION=12.6.3

# ---- Stage 1: build whisper.cpp (whisper-cli) with CUDA backend ----
# Pinned to the on-prem host's GPU stack: NVIDIA only. CUDA is whisper.cpp's
# most-optimized backend (1.5–2× over Vulkan on NVIDIA). The runtime stage
# uses the matching nvidia/cuda runtime image so libcudart / libcublas etc.
# are available without polluting the host.
FROM --platform=linux/${ARCH} nvidia/cuda:${CUDA_VERSION}-devel-ubuntu24.04 AS whisper-build
ARG WHISPER_CPP_REF=v1.8.6
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential cmake git ca-certificates pkg-config \
        libopenblas-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
# WHISPER_CPP_REF is a mutable tag — git tags can be force-moved, so the image
# is not bit-reproducible. Pin a 40-char commit SHA when that matters.
RUN git clone --depth 1 --branch ${WHISPER_CPP_REF} https://github.com/ggerganov/whisper.cpp.git .
# BLAS stays on for the CPU fallback path (`-whispercpp-no-gpu`); on CUDA
# hosts it's unused at runtime, so the only cost is image size.
#
# CMAKE_CUDA_ARCHITECTURES must be pinned: ggml-cuda's default is `native`,
# which queries nvidia-smi on the build host — docker build has no GPU, so
# the build would fail. Pinned to sm_86 for the on-prem RTX 3090 (Ampere).
# If the deployment GPU changes, override CUDA_ARCHS — building for the wrong
# arch either falls back to PTX JIT at startup (slow) or fails outright.
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
# Fail here rather than at first transcription if the install layout changes.
RUN test -x /opt/whisper/bin/whisper-cli

# ---- Stage 2: build frontend (Nuxt → static) ----
# Runs on the build host's native arch: the output is platform-independent
# static JS/CSS, so there is nothing to gain from emulating the target.
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS frontend-build
# PNPM_VERSION must match frontend/package.json's "packageManager" field.
# pnpm >= 10 self-manages to whatever that field declares, so installing a
# different major here does not pin anything — it silently re-execs the
# declared version mid-build. --config.manage-package-manager-versions=false
# disables that, so the version installed here is the version that runs and
# any drift fails loudly (lockfile mismatch) instead of quietly.
ARG PNPM_VERSION=11.21.0
RUN npm install -g pnpm@${PNPM_VERSION}
ENV PNPM_FLAGS="--config.manage-package-manager-versions=false"
WORKDIR /src/frontend
# pnpm-workspace.yaml carries settings pnpm needs AT INSTALL TIME: allowBuilds
# (permits the esbuild / @parcel/watcher native postinstalls) and
# minimumReleaseAge. Omit it from this layer and `pnpm install` still exits 0
# but skips those builds (ERR_PNPM_IGNORED_BUILDS), and the Nuxt build fails
# downstream. It must be copied alongside package.json, not with the source.
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./
RUN pnpm ${PNPM_FLAGS} install --frozen-lockfile
COPY frontend/ ./
# @nuxt/fonts downloads webfonts from fonts.gstatic.com during the build, so
# this stage needs outbound internet even though the result is self-contained.
RUN pnpm ${PNPM_FLAGS} generate

# ---- Stage 3: build Go binary ----
# Also native + cross-compiled: CGO_ENABLED=0 makes GOARCH a pure flag flip.
FROM --platform=$BUILDPLATFORM golang:1.26.3-bookworm AS go-build
ARG ARCH
# go.mod pins a patch version (go 1.26.3). GOTOOLCHAIN=local makes a mismatch
# with this base image fail loudly instead of silently downloading a toolchain
# from proxy.golang.org — which breaks in network-restricted builders and makes
# the build depend on whatever the floating image tag resolved to.
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
# internal/web/dist is gitignored, so it is absent (or stale) in the build
# context. Replace it with the freshly built SPA — //go:embed needs it to
# exist and contain index.html.
RUN rm -rf internal/web/dist && mkdir -p internal/web/dist
COPY --from=frontend-build /src/frontend/.output/public/ ./internal/web/dist/
RUN GOARCH=${ARCH} go build -trimpath -ldflags='-s -w' -o /out/transcriber ./cmd/transcriber

# ---- Stage 4: runtime ----
# Matches the whisper-build base so glibc / libstdc++ and CUDA runtime libs
# (libcudart, libcublas) are present without bundling the full toolkit.
#
# NOTE: nvidia/cuda images carry NVIDIA_REQUIRE_CUDA=cuda>=<major.minor>, which
# the NVIDIA Container Toolkit *enforces* at container start. With CUDA 12.6 the
# host driver must be >= 560.28.03 or the container refuses to start with
# "requirement error: unsatisfied condition: cuda>=12.6". Lower CUDA_VERSION or
# set NVIDIA_DISABLE_REQUIRE=1 to override. See DEPLOY.md.
FROM --platform=linux/${ARCH} nvidia/cuda:${CUDA_VERSION}-runtime-ubuntu24.04 AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg ca-certificates libgomp1 libstdc++6 \
        libopenblas0-pthread \
        curl \
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
# Flags come from docker-compose.yml (driven by .env); these defaults apply
# when running the image directly with `docker run`.
ENTRYPOINT ["/usr/local/bin/transcriber"]
CMD ["-port=8888", "-workers=2", "-default-model=whisper-cpp-large-v3", "-log-format=json"]
