# Deploying transcriber

Target: on-prem Linux host running Docker. The image bundles `whisper-cli`
(whisper.cpp, built with the **Vulkan** GGML backend), `ffmpeg`/`ffprobe`,
and the Go API + embedded SPA, so the container has everything it needs
except the ggml model files (downloaded from Hugging Face on first use
into a persisted volume).

## GPU access (Vulkan)

whisper.cpp uses Vulkan for GPU acceleration — same image works for
NVIDIA, AMD, and Intel, just with slightly different runtime wiring.

**AMD / Intel** — the host needs working Mesa drivers; `docker-compose.gpu.yml`
exposes `/dev/dri` and joins the `video`/`render` groups. Verify the GPU
is visible inside the container:

```sh
docker compose exec transcriber vulkaninfo --summary  # if installed
# or just look at the first job's logs: whisper-cli prints the picked device.
```

**NVIDIA** — install the [NVIDIA Container Toolkit][nvct] on the host;
`docker-compose.gpu.yml` already includes the `deploy.resources.reservations.devices`
block that hands the GPUs to the container. The
`NVIDIA_DRIVER_CAPABILITIES=compute,utility,graphics` baked into the image
is what causes the toolkit to mount the NVIDIA Vulkan ICD inside.

To run on CPU only, just bring up `docker-compose.yml` alone (no GPU
overlay) — whisper.cpp falls back to the CPU backend when no Vulkan
device is available.

[nvct]: https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html

## Build & run

```sh
# CPU-only (local sanity check / dev machine without GPU):
docker compose build
docker compose up -d

# On-prem with GPU access (Vulkan):
docker compose -f docker-compose.yml -f docker-compose.gpu.yml up -d
```

The base `docker-compose.yml` works anywhere; `docker-compose.gpu.yml`
overlays the GPU device exposure and is **Linux-host only** — it mounts
`/dev/dri` and reserves NVIDIA devices, neither of which exists on
macOS or Windows. On a Mac dev machine, run the base compose file alone
(CPU fallback, under qemu emulation — slow but correct).

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

Env vars set inside the image:

- `WHISPER_CPP_BIN=/usr/local/bin/whisper-cli`
- `XDG_CACHE_HOME=/var/cache` → models live at `/var/cache/transcriber/hf/<repo>/<file>`

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
