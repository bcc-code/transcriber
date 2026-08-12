# Deploying transcriber

Target: on-prem Linux host with an NVIDIA GPU, running Docker. Everything
the container needs is in the image except the ggml model files
(downloaded from Hugging Face on first use into a persisted volume).

> **An NVIDIA GPU and driver are required.** `whisper-cli` is dynamically
> linked against `libcuda.so.1`, which the NVIDIA Container Toolkit injects
> at container start. Without it the dynamic loader fails before `main()`,
> so the binary cannot run at all — there is no CPU fallback in the
> container, and `-whispercpp-no-gpu` is unreachable there. For CPU or
> Apple-Metal hosts, run the Go binary natively against a native
> `whisper-cli` (see README.md); that is also the recommended local
> development path.

## Two images

The build is split in two, because compiling whisper.cpp with nvcc takes
15–30 minutes and only changes when whisper.cpp or CUDA does:

| Image                                     | Built by                              | Contains                                              |
| ----------------------------------------- | ------------------------------------- | ----------------------------------------------------- |
| `ghcr.io/bcc-code/whisper-cuda:<ref>-cuda<ver>` | `Dockerfile.whisper`, `whisper-base.yml` | whisper.cpp (CUDA), ffmpeg/ffprobe, CUDA runtime libs |
| `ghcr.io/bcc-code/transcriber:<tag>`      | `Dockerfile`, `image.yml`             | the above + Go binary with embedded SPA               |

Ordinary code changes rebuild only the app image, in seconds. `BASE_IMAGE`
in `.env` / `docker-compose.yml` selects the base; the tag encodes both the
whisper.cpp ref and the CUDA version.

The base must exist before the app image can build at all — the app's
`FROM` resolves it. `image.yml` checks for it up front and fails with an
actionable message rather than a cryptic buildx error.

**Publishing a base.** `whisper-base.yml` triggers on a push (any branch)
that touches `Dockerfile.whisper`, and via `Actions → whisper-base → Run
workflow` once the workflow is on the default branch — `workflow_dispatch`
is not offered for workflows that only exist on a feature branch, so on a
new branch the push path is how you bootstrap it.

It **skips the build if the target tag already exists**, so a re-trigger
costs seconds rather than ~20 minutes. Consequently, changing *how* the
base is built without changing a version does not republish: pass
`force: true`. That is deliberate — it stops an in-progress branch from
silently replacing the base image the deployed app is running on. A
feature branch may publish the versioned tag, but only the default branch
moves `latest`.

**Bumping whisper.cpp or CUDA:** dispatch whisper-base with the new
ref/version, then update `BASE_IMAGE` in `.env.example`,
`docker-compose.yml`, and `image.yml`.

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
5. **Registry access.** Images pushed by GitHub Actions land as **private**
   GHCR packages, so the host must authenticate before it can pull either
   image:

   ```sh
   # PAT needs the read:packages scope.
   echo "$GHCR_PAT" | docker login ghcr.io -u <github-user> --password-stdin
   ```

   Alternatively set both `transcriber` and `whisper-cuda` packages to
   internal/public under `github.com/orgs/bcc-code/packages`. A failure
   here looks like `denied` or `manifest unknown` on `docker compose pull`,
   before anything GPU-related is even attempted.

[nvct]: https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html
[cuda-compat]: https://docs.nvidia.com/deploy/cuda-compatibility/

## First deployment, in order

Each step's failure mode is distinct, so doing them in order means a
failure tells you exactly which one you're on. See Troubleshooting at the
end of this file for symptom → cause.

```sh
# 1. Verify the driver is new enough for the CUDA the image was built on.
nvidia-smi                              # need >= 560.28.03

# 2. Verify the container runtime can see the GPU at all.
docker run --rm --gpus all nvidia/cuda:12.6.3-base-ubuntu24.04 nvidia-smi

# 3. Authenticate to GHCR (see Preflight step 5).
echo "$GHCR_PAT" | docker login ghcr.io -u <github-user> --password-stdin

# 4. Configure. STORAGE_PATH must already exist and must match the paths
#    the caller puts in `path` / `output_path`.
cp .env.example .env && $EDITOR .env
ls -ld "$(grep -E '^STORAGE_PATH=' .env | cut -d= -f2)"

# 5. Pull. Fails here = registry auth or a tag that was never published.
docker compose pull

# 6. Pre-seed the models BEFORE the first job — see "Pre-seeding models".
#    Skipping this is the most common cause of a confusing first failure:
#    the ~3 GB download runs inside the job's own timeout budget.

# 7. Start, then watch it become healthy.
docker compose up -d
docker compose ps                        # want STATUS: healthy
curl -fsS localhost:8888/healthz

# 8. Short file first (single-pass), then a long one (exercises chunking).
curl -sS -X POST localhost:8888/transcription/job \
  -H 'content-type: application/json' \
  -d '{"path":"/mnt/storage/<short>.mp3","language":"no","format":"all",
       "output_path":"/mnt/storage/out/test1"}'
```

Poll with `GET /transcription/job/{id}` and watch `docker compose logs -f
transcriber`. On success, `output_path` should contain **only** the five
transcript files — any `chunks/` directory or `whispercpp_out.json` there
means the scratch-directory handling regressed.

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

Routine deploys and restarts. For a host that has never run this before,
work through "First deployment, in order" above instead — it covers the
one-time driver, registry, and model-seeding steps.

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
no NVIDIA GPU it fails loudly with "could not select device driver", which
is the correct outcome: the image cannot run without a GPU regardless.

The API is served on `:8888`. Open `http://<host>:8888/` for the SPA or
hit `POST /transcription/job` directly. `GET /healthz` and `GET /readyz`
are available for probes, and the compose file wires `/healthz` into a
container healthcheck — `docker compose ps` shows health at a glance.

> **Note:** `/readyz` currently only checks that a default model is
> registered, which is always true. It does **not** verify that
> `whisper-cli` runs, that a GPU is visible, or that model files are
> present. Treat a green `/readyz` as "the process is up", not "the next
> job will succeed".

### Building the images

**App image** (`Dockerfile`) — cheap. It only builds the SPA and the Go
binary, both of which run natively on the build host and cross-compile, so
nothing is qemu-emulated even on an arm64 Mac. Measured locally: ~2 s with
a warm cache. `.github/workflows/image.yml` builds it on every push and
tags `latest`, `sha-<short>`, and semver on tags. Pin a `sha-` tag in
`.env` for a rollback-able deploy.

**Base image** (`Dockerfile.whisper`) — expensive, and rarely rebuilt.
Compiling ggml-cuda is ~15–30 min on an x86_64 runner. Two things about
this build are easy to get wrong and are pinned deliberately:

- **`BUILD_JOBS`.** nvcc needs roughly 2 GB of RAM per concurrent job for
  ggml-cuda's template instantiations, so the build is memory-bound rather
  than core-bound. A bare `-j` (unlimited) launched 138 concurrent nvcc
  processes on a 4-vCPU/16 GB runner and the OOM killer took down the
  runner agent mid-build, leaving no compiler error behind — the log simply
  stopped. The job count is now derived from available memory and capped at
  the core count; override with `--build-arg BUILD_JOBS=N`.
- **The CUDA driver stub on the link line.** ggml-cuda calls driver-API
  functions (`cuMemCreate`, `cuGetErrorString`, …) that live in
  `libcuda.so`, not `libcudart`. With `BUILD_SHARED_LIBS=ON`,
  `libggml-cuda.so` links fine with those undefined, and the failure
  surfaces much later as `undefined reference to 'cuGetErrorString'` when
  an executable links against it.

Build it on an x86_64 host or via the **whisper-base** workflow. On arm64
it also builds natively (nvidia/cuda publishes arm64 manifests), which is
useful for validating changes to `Dockerfile.whisper` without qemu.

The frontend build downloads webfonts from `fonts.gstatic.com`
(`@nuxt/fonts`), so the *builder* needs outbound internet even though the
resulting image is self-contained.

## Scratch space

Adapter intermediates — extracted chunk wavs and raw whisper JSON — go into a
per-job scratch directory that is deleted when the job ends, including on
timeout, cancellation, and failure. Only the final transcripts are written to
`output_path`.

Scratch defaults to the OS temp dir (`/tmp` in the container, so the container's
writable layer). Chunking writes roughly **115 MB per hour of audio**, so with
`WORKERS=2` on long files budget a few GB of headroom. Set `-scratch-dir` to
move it — keep it on local disk rather than network storage, since none of it
is worth shipping over the wire. `-keep-work-dirs` retains the directories for
inspecting a bad transcription.

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
- **Output files are root-owned.** The container runs as root, so
  transcripts land on `STORAGE_PATH` as `root:root`.
- **No authentication.** Anything that can reach the port can submit jobs
  with arbitrary absolute `path` / `output_path` values. Bind to an
  internal interface (`BIND_ADDR`) and firewall it.

## Troubleshooting

| Symptom                                                                | Cause / fix                                                                                                                                                       |
| ---------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `denied` / `manifest unknown` on `docker compose pull`                  | Not logged in to GHCR, or `IMAGE` / `BASE_IMAGE` points at a tag that was never published. See Preflight step 5.                                                   |
| `nvidia-container-cli: requirement error: unsatisfied condition: cuda>=12.6` — container won't start | Host driver older than 560.28.03. Lower `CUDA_VERSION` and rebuild the base, or set `NVIDIA_DISABLE_REQUIRE=1` on the service.                                     |
| `could not select device driver` on `up`                               | NVIDIA Container Toolkit not installed, or Docker not restarted after installing it.                                                                               |
| `whisper-cli: error while loading shared libraries: libcuda.so.1`      | Running without GPU access. The image has no CPU mode — `libcuda.so.1` is injected by the toolkit. Check the `deploy.resources.reservations` block survived.        |
| Every job fails instantly with ENOENT                                  | `STORAGE_PATH` doesn't exist on the host, or the caller's absolute paths don't match the mount. Host and container paths must be identical.                        |
| First job fails with `error: "timeout"`                                | The ~3 GB model download runs inside the job's own timeout budget. Pre-seed the models, or raise `JOB_TIMEOUT` for the first run.                                   |
| Jobs fail after a long wait; `hfcache` errors in the log               | No outbound HTTPS to `huggingface.co`. Pre-seed the models instead.                                                                                               |
| Very slow start per job, or a CUDA init failure                        | `CUDA_ARCHS` doesn't match the GPU, so the kernels PTX-JIT at startup. Rebuild the base with the right SM version (86 = RTX 3090).                                 |
| A job fails with a CUDA OOM                                            | Too many concurrent jobs for the VRAM. Each worker holds a ~3 GB FP16 model plus buffers; lower `WORKERS`.                                                          |
| `chunks/` or `whispercpp_out.json` appearing in `output_path`           | Scratch-directory handling regressed — adapters should only write under the per-job work dir. See "Scratch space".                                                 |
| Caller gets 404 for a job it just submitted                            | Either the process restarted (the job store is in-memory) or the job was evicted — raise `MAX_TERMINAL_JOBS`.                                                       |
| Base image build dies mid-compile with no compiler error               | OOM-killed. `BUILD_JOBS` is derived from available memory, but override it lower: `--build-arg BUILD_JOBS=2`.                                                       |
| `undefined reference to 'cuGetErrorString'` building the base          | The CUDA driver stub is missing from the link line — see the `CUDA_STUBS` args in `Dockerfile.whisper`.                                                             |

Logs are the first stop for anything job-related: `docker compose logs -f
transcriber`. `GET /stats` gives queue/running/processed counts, and
`GET /transcription/jobs` lists every job the store still holds.
