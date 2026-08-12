# Deploying transcriber

Target: on-prem Linux host with an NVIDIA GPU, running Docker. The image
bundles `whisper-cli` (whisper.cpp, built with the **CUDA** GGML backend
plus OpenBLAS for the CPU fallback path), `ffmpeg`/`ffprobe`, and the Go
API + embedded SPA. Everything the container needs is in the image
except the ggml model files (downloaded from Hugging Face on first use
into a persisted volume).

## Preflight

Run through these before deploying — each one is a failure that otherwise
shows up minutes later as a mysteriously failed job.

1. **NVIDIA driver version.** `nvidia-smi`. The image is built on CUDA
   12.6, and `nvidia/cuda` images carry `NVIDIA_REQUIRE_CUDA=cuda>=12.6`
   which the NVIDIA Container Toolkit **enforces at container start**.
   With an older driver the container refuses to start:
   `nvidia-container-cli: requirement error: unsatisfied condition:
   cuda>=12.6`. Driver must be **≥ 560.28.03**. To target an older
   driver, lower `CUDA_VERSION` to a release whose minimum driver matches
   (see the [CUDA compatibility matrix][cuda-compat]), or set
   `NVIDIA_DISABLE_REQUIRE=1` to bypass the check.
2. **NVIDIA Container Toolkit installed.** [Install guide][nvct]. Verify:
   `docker run --rm --gpus all nvidia/cuda:12.6.3-base-ubuntu24.04 nvidia-smi`.
3. **`STORAGE_PATH` exists on the host.** Docker otherwise creates it as
   an empty root-owned directory and every job fails with ENOENT.
4. **Outbound HTTPS to `huggingface.co`.** Models (~3 GB) are fetched on
   first use. If the host is firewalled, pre-seed them (below) — otherwise
   every job fails.

[nvct]: https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html
[cuda-compat]: https://docs.nvidia.com/deploy/cuda-compatibility/

## Configure

All configuration lives in `.env`, read automatically by `docker compose`:

```sh
cp .env.example .env
$EDITOR .env
```

`.env.example` documents every knob. The ones that matter most:
`STORAGE_PATH` (must match the paths the caller sends), `CUDA_ARCHS`
(`86` for the RTX 3090; `89` for L4/RTX 40xx, `90` for H100),
`DEFAULT_LANGUAGE`, and `WORKERS`.

## Deploy

Preferred — pull the CI-built image (no compiling on the host):

```sh
docker compose pull
docker compose up -d
```

Or build locally, on an **x86_64** host:

```sh
docker compose build
docker compose up -d
```

GPU access is part of `docker-compose.yml`, so the short command above is
the production command — there is no overlay to remember. On a host with
no NVIDIA GPU this fails loudly with "could not select device driver"
rather than silently running 20–50× slower on CPU. For those hosts:

```sh
# CPU-only (Mac dev, CPU-only Linux). Also set NO_GPU=true in .env.
docker compose -f docker-compose.yml -f docker-compose.cpu.yml up -d
```

The API is served on `:8888`. Open `http://<host>:8888/` for the SPA or
hit `POST /transcription/job` directly. `GET /healthz` and `GET /readyz`
are available for probes, and the compose file wires `/healthz` into a
container healthcheck — `docker compose ps` shows health at a glance.

> **Note:** `/readyz` currently only checks that a default model is
> registered, which is always true. It does **not** verify that
> `whisper-cli` runs, that a GPU is visible, or that model files are
> present. Treat a green `/readyz` as "the process is up", not "the next
> job will succeed".

### Building the image

Only the whisper.cpp/CUDA stage and the runtime stage are target-arch
(`linux/amd64`); the frontend and Go stages run natively on the build host
and cross-compile. So on an arm64 Mac, `docker compose build` still has to
qemu-emulate `nvcc`, which is slow (30–90 min) and prone to dying outright.

Build on the Linux host, or let CI do it: `.github/workflows/image.yml`
builds on an x86_64 runner and pushes to
`ghcr.io/bcc-code/transcriber`, tagged `latest`, `sha-<short>`, and
semver on tags. Pin a `sha-` tag in `.env` for a rollback-able deploy.

The frontend build downloads webfonts from `fonts.gstatic.com`
(`@nuxt/fonts`), so the *builder* needs outbound internet even though the
resulting image is self-contained.

## Volumes

| Mount                                          | Purpose                                                                                                                                              |
| ---------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `models:/var/cache/transcriber`                | ggml whisper.cpp models. Survives restarts — first job downloads ~3 GB.                                                                               |
| `${STORAGE_PATH}:${STORAGE_PATH}`              | Audio inputs (`path`) and transcript outputs (`output_path`). Mounted at the same path inside and out, because request paths resolve in the container. |
| `./prompt.txt:/app/prompt.txt:ro` _(optional)_ | Default prompt file. Uncomment in `docker-compose.yml`. Without it, only requests carrying their own `prompt` get one.                                 |

Env vars baked into the image:

- `WHISPER_CPP_BIN=/usr/local/bin/whisper-cli`
- `XDG_CACHE_HOME=/var/cache` → models live at `/var/cache/transcriber/hf/<repo>/<file>`

Both whisper.cpp adapters resolve the FP16 large-v3 weights (~3 GB
each) — the reference quality. On the RTX 3090 (24 GB VRAM) there's no
reason to trade accuracy for the Q5_0 variant; CUDA inference is
compute-bound here, not memory-bound. Override `WHISPER_CPP_MODEL` /
`NB_WHISPER_MODEL` / `WHISPER_VAD_MODEL` on the service to pin a model to
a specific file on disk instead of letting the HF cache resolve it.

## Pre-seeding models

Worth doing even with internet access: the download currently happens
*inside* the first job's context, so it competes with `-job-timeout`
(default 30m) and a slow fetch makes the first job fail with
`error: "timeout"`. With `WORKERS=2`, the second job blocks on the first
job's download while its own deadline runs.

```sh
# Find the volume path:
docker volume inspect transcriber_models -f '{{ .Mountpoint }}'

# Copy pre-downloaded models into place:
sudo mkdir -p <mountpoint>/transcriber/hf/ggerganov/whisper.cpp
sudo cp ggml-large-v3.bin <mountpoint>/transcriber/hf/ggerganov/whisper.cpp/
sudo mkdir -p <mountpoint>/transcriber/hf/ggml-org/whisper-vad
sudo cp ggml-silero-v5.1.2.bin <mountpoint>/transcriber/hf/ggml-org/whisper-vad/
```

## Upgrading

```sh
git pull                 # picks up .env.example / compose changes
docker compose pull      # or: docker compose build
docker compose up -d
```

The API has a 10 s graceful shutdown — in-flight HTTP requests finish,
but workers receive a cancel and any running transcription jobs are
killed. **The job store is in-memory**, so a restart also discards the
queue and all job history; the caller's next poll returns 404. Drain
first (`GET /transcription/jobs`, `DELETE /transcription/job/{id}`) or
redeploy when idle.

## Logs

`docker compose logs -f transcriber` — the API logs via slog to stderr,
JSON by default (`LOG_FORMAT` in `.env`).

## Known rough edges

Tracked in `IMPROVEMENTS.md`; these are the ones that affect operations:

- **In-memory job store.** Restart or crash loses the queue and history.
  `MAX_TERMINAL_JOBS` (default 200 here) also evicts completed jobs, so a
  large enough burst can evict a result before the caller reads it.
- **Chunk temp files are left behind.** For audio longer than 5 minutes,
  per-chunk `.wav` files are written into the job's `output_path` and never
  cleaned up — roughly 115 MB per hour of audio, accumulating on shared
  storage.
- **Output files are root-owned.** The container runs as root, so
  transcripts land on `STORAGE_PATH` as `root:root`.
- **No authentication.** Anything that can reach the port can submit jobs
  with arbitrary absolute `path` / `output_path` values. Bind to an
  internal interface (`BIND_ADDR`) and firewall it.
