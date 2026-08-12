# syntax=docker/dockerfile:1.7

# App image: the Go binary with the SPA embedded, on top of the prebuilt
# whisper.cpp/CUDA base (see Dockerfile.whisper). Nothing here compiles CUDA, so
# this builds in seconds — bump BASE_IMAGE when whisper.cpp or CUDA changes.
#
# The frontend and Go stages run natively on the build host ($BUILDPLATFORM) and
# cross-compile, so building an amd64 image from an arm64 Mac never touches qemu.
#
# NOTE: the base image requires an NVIDIA GPU and driver at runtime — whisper-cli
# is dynamically linked against libcuda.so.1, injected by the NVIDIA Container
# Toolkit. There is no CPU-only mode. For local development on a machine without
# an NVIDIA GPU, run the binary natively (`make build`) against a native
# whisper-cli; see README.md.

ARG ARCH=amd64
ARG BASE_IMAGE=ghcr.io/bcc-code/whisper-cuda:v1.8.6-cuda12.6.3

# ---- Stage 1: build frontend (Nuxt → static) ----
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

# ---- Stage 2: build Go binary ----
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

# ---- Stage 3: runtime ----
FROM --platform=linux/${ARCH} ${BASE_IMAGE}

# Model cache: $XDG_CACHE_HOME/transcriber/hf/<repo>/<file>
ENV XDG_CACHE_HOME=/var/cache \
    WHISPER_CPP_BIN=/usr/local/bin/whisper-cli
RUN mkdir -p /var/cache/transcriber/hf

COPY --from=go-build /out/transcriber /usr/local/bin/transcriber

WORKDIR /app
EXPOSE 8888

# `prompt.txt` is optional; mount one in if you want a default prompt.
# Flags come from docker-compose.yml (driven by .env); these defaults apply
# when running the image directly with `docker run`.
ENTRYPOINT ["/usr/local/bin/transcriber"]
CMD ["-port=8888", "-workers=2", "-default-model=whisper-cpp-large-v3", "-log-format=json"]
