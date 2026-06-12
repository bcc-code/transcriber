# Deploying transcriber

Target: on-prem Linux host with NVIDIA GPUs, running Docker. The image
bundles `whisper-cli` (whisper.cpp, built with the **CUDA** GGML backend
plus OpenBLAS for the CPU fallback path), `ffmpeg`/`ffprobe`, and the Go
API + embedded SPA. Everything the container needs is in the image
except the ggml model files (downloaded from Hugging Face on first use
into a persisted volume).

## GPU access (CUDA)

Install the [NVIDIA Container Toolkit][nvct] on the host. With it in
place, `docker-compose.gpu.yml` reserves all NVIDIA devices for the
container, and `NVIDIA_DRIVER_CAPABILITIES=compute,utility` (baked into
the image) tells the toolkit which driver libraries to expose. To run
on CPU only — e.g. a host without NVIDIA hardware, or local Mac dev —
bring up `docker-compose.yml` alone and add `-whispercpp-no-gpu` to the
service command.

CUDA runtime version is pinned via `CUDA_VERSION` build arg
(default `12.6.3`); this requires NVIDIA driver **≥ 560.28.03** on the
host. Check with `nvidia-smi` before deploying. To target an older
driver, lower `CUDA_VERSION` to a release whose minimum driver matches
what's installed — see the [CUDA compatibility matrix][cuda-compat].

GPU architecture is pinned via `CUDA_ARCHS` build arg (default `"86"`,
the SM version for the on-prem RTX 3090 / Ampere). If the deployment
GPU changes, override at build time (e.g. `--build-arg CUDA_ARCHS=89`
for L4 / RTX 40xx, `90` for H100) and update the Dockerfile default.

[nvct]: https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html
[cuda-compat]: https://docs.nvidia.com/deploy/cuda-compatibility/

## Build & run

```sh
# CPU-only (local sanity check / dev machine without GPU):
docker compose build
docker compose up -d

# On-prem with NVIDIA GPU:
docker compose -f docker-compose.yml -f docker-compose.gpu.yml up -d
```

The base `docker-compose.yml` works anywhere; `docker-compose.gpu.yml`
overlays the NVIDIA device reservation and is **Linux-host only** —
the `nvidia` driver runtime isn't available on macOS or Windows. On a
Mac dev machine, run the base compose file alone (CPU fallback, under
qemu emulation — slow but correct).

All Dockerfile stages are pinned to `linux/amd64` because the on-prem GPU
hosts are x86_64. On an x86_64 build host this is a no-op; on an arm64
host (Apple Silicon dev machine) the build runs under qemu emulation,
which is slow but produces deployment-correct binaries. To build natively
for arm64 instead, strip the `--platform=linux/amd64` from each `FROM`.

The API is served on `:8888`. Open `http://<host>:8888/` for the SPA or
hit `POST /transcription/job` directly. `GET /healthz` and `GET /readyz`
are available for liveness/readiness probes.

## Volumes

| Mount                                          | Purpose                                                                                                                                                                                      |
| ---------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `models:/var/cache/transcriber`                | ggml whisper.cpp models. Survives container restarts — first job downloads ~3 GB.                                                                                                            |
| `/mnt/storage:/mnt/storage`                    | Audio inputs (`path`) and transcript outputs (`output_path`). Paths in API requests are read inside the container, so the host paths you reference must be visible at the same mount points. |
| `./prompt.txt:/app/prompt.txt:ro` _(optional)_ | Default prompt file. Without it, only requests carrying their own `prompt` field get one.                                                                                                    |

## Configuration

Flags are set via the `command:` field in `docker-compose.yml`. The
defaults run `whisper-cpp-large-v3` with 2 workers; for a beefier host
something like `["-workers=4", "-callback-workers=4"]` is reasonable.

To skip whisper's language auto-detection (faster, more reliable when the
corpus is mono-lingual), pass an ISO 639-1 code with `-default-language`,
e.g. `["-default-language=no"]`. Requests can still override with their
own `language` field, or send `"auto"` to opt back into detection.

On hosts without an NVIDIA GPU (Mac dev, CPU-only Linux), pass
`-whispercpp-no-gpu` to force whisper-cli's CPU backend (OpenBLAS-
accelerated). Without it, the CUDA backend tries to initialize, fails
to find a device, and the job errors. The `docker-compose.override.yml`
in this repo already sets the flag for local dev; production NVIDIA
hosts don't need it.

Env vars set inside the image:

- `WHISPER_CPP_BIN=/usr/local/bin/whisper-cli`
- `XDG_CACHE_HOME=/var/cache` → models live at `/var/cache/transcriber/hf/<repo>/<file>`

Both whisper.cpp adapters resolve the FP16 large-v3 weights (~3 GB
each) — the reference quality. On the RTX 3090 (24 GB VRAM) there's no
reason to trade accuracy for the Q5_0 variant; CUDA inference is
compute-bound here, not memory-bound, so quantization wouldn't speed
things up meaningfully either. To switch to a quantized file anyway
(e.g. `ggml-large-v3-q5_0.bin`), pre-seed it into the volume and pin
via `WHISPER_CPP_MODEL` / `NB_WHISPER_MODEL`.

Override `WHISPER_CPP_MODEL` / `NB_WHISPER_MODEL` / `WHISPER_VAD_MODEL`
on the service to pin a model to a specific file on disk instead of
letting the HF cache resolve it.

## Pre-seeding models (optional)

To avoid the first-request download, drop ggml files into the volume
ahead of time:

```sh
# Find the volume path:
docker volume inspect transcriber_models -f '{{ .Mountpoint }}'

# Copy a pre-downloaded model into place:
sudo mkdir -p <mountpoint>/transcriber/hf/ggerganov/whisper.cpp
sudo cp ggml-large-v3.bin <mountpoint>/transcriber/hf/ggerganov/whisper.cpp/
```

## Upgrading

```sh
git pull
docker compose build
docker compose up -d
```

The API has a 10 s graceful shutdown — in-flight HTTP requests finish,
but workers receive a cancel and any running transcription jobs are
killed. Avoid redeploying while jobs are running, or drain the queue
first (`GET /transcription/jobs`, `DELETE /transcription/job/{id}`).

## Logs

`docker compose logs -f transcriber` — the API logs via slog to stderr.
