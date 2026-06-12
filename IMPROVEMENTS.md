# Potential improvements

Working notes for hardening this into a production API-first service that
backs a VOD pipeline. Ordered roughly by ROI for that use case; the
chunking work for long-form audio is already in `internal/transcriber/chunked`.

## Persistent job store

`internal/jobs.Store` is in-memory: a server restart loses the queue and all
history. For an API behind a pipeline this is unacceptable — retries can't
deduplicate, the caller can't query status across restarts, and a crash
mid-job silently drops work.

Approach: drop in SQLite via `modernc.org/sqlite` (pure Go, no CGO). The
existing `Store` interface fits a SQL backing with `jobs` + `job_events`
tables. Restore in-flight jobs on boot (mark `RUNNING`-but-unowned ones as
`PENDING` and requeue). Half-day swap.

## Network access control

Currently anything reachable on the port can submit jobs. With a single
known caller, the cheap fix is network-level: bind to the internal
interface, firewall the port, or front with a reverse proxy that
allowlists the caller's source IPs. A shared API token checked in
middleware is the next step up if network controls aren't enough.

## URL-based input + signed-URL output

The API takes a server-local file path, which forces the caller to drop
files on the box before submitting. For a pipeline that's a step nobody
wants.

- Input: accept `source_url` (`http(s)://` or `s3://`); download to a scratch
  dir, transcribe, clean up. Bound max size + content-type, respect signed
  URL TTLs.
- Output: optionally PUT the result files (json/srt/vtt/txt/zip) to a
  caller-supplied signed URL or S3 prefix. The VOD service then never
  reads back through this API.

Once these land, the box is stateless from the caller's perspective.

## Webhook signing + richer payload

`internal/callback` fires on status changes but the payload isn't signed
and is fairly bare. For pipeline integration:

- Sign with HMAC-SHA256 over the raw body using a shared secret with the
  caller; include `X-Signature` + `X-Timestamp` headers and reject
  replays older than ~5 min on the receiver side.
- On `COMPLETED`, include the result inline (small) or a result URL the
  receiver can pull, plus `duration`, `language`, `model`, `word_count`.
- Retries: exponential backoff with a dead-letter after N attempts.

## Forced alignment mode

If the caller already has a clean human-edited transcript (very common in
VOD — there's a script), aligning it to audio gives sub-100ms timestamps
with zero transcription errors. This is the killer feature for production
subtitle workflows.

Implementation options:

- Wrap WhisperX or stable-ts as a third adapter (`task=align`); request
  takes `audio` + `transcript`, returns word-level timestamps for the
  provided text.
- Build it natively against whisper.cpp's DTW (see DTW section below) —
  more work but no Python dependency.

## Speaker diarization

whisper.cpp `-tdrz` (tinydiarize, requires a `-tdrz` model) or pyannote
via faster-whisper labels speakers. Add a `speaker` field to `Segment`
and `Word`. Useful for multi-voice content (interviews, panels, church
services with multiple readers).

## Translation task

Whisper has a built-in `task=translate` that translates any source
language to English in one pass. For a multilingual content library this
eliminates a second processing step. Add a `task` field (`transcribe` /
`translate`) on the job request.

## Prometheus metrics + structured JSON logging

For operability without poking the API:

- `/metrics` endpoint with queue depth gauge, job duration histogram
  (by model + status), job throughput counter, callback delivery
  success/failure counts.
- Switch the `slog` text handler to JSON, attach `job_id` (and
  `api_key_id` once auth lands) to every log line via context.

## Audio preprocessing pipeline

Right now each adapter does its own decoding. Centralize a preprocessing
step: normalize loudness, downmix to mono, resample to 16kHz, optional
silence trim. Adapters then receive a known-good wav and never see raw
mp3/m4a/etc. Also makes the chunked extractor's output the _only_ shape
adapters need to support.

## Parallel chunk transcription

`chunked.Config.Parallelism` is wired but defaults to 1. On CPU
(whisper.cpp) or multi-GPU hosts, raising this gives substantial
wall-time improvement on long files. Needs measurement per backend
(whisper.cpp's internal threading vs. process-level concurrency) before
choosing a default > 1.

## Persistent model worker

Each `whisper-cli` / `whisper-ctranslate2` invocation reloads the model
from disk (~5s for large-v3). When chunking a 1-hour file into 12 chunks
that's a meaningful tax. Long-running per-model worker processes amortize
the load cost across all chunks of a job and across jobs.

whisper.cpp ships a `server` binary built from the same source we already
clone in the Dockerfile — adding `whisper-server` to the cmake `--target`
list is a one-liner. The real work is the adapter: a new
`internal/transcriber/whisperserver` that POSTs audio to the running
server over HTTP, plus lifecycle management (start it from the Go
process, health-check it, restart on crash).

## Image / deployment

Quality-of-life items for the Docker image and on-prem deployment flow.

- **Pin whisper.cpp by commit SHA.** Currently `WHISPER_CPP_REF=v1.7.4`,
  which is a mutable tag — git tags can be force-moved. Switch to a
  40-char SHA so the image is bit-reproducible.

- **Compose healthcheck.** Add `healthcheck:` to `docker-compose.yml`
  hitting `/healthz`. Gives Docker's restart policy something to act on
  if the API hangs without crashing, and `docker compose ps` will show
  health status at a glance.

- **Multi-arch image.** Every stage in the Dockerfile is pinned to
  `linux/amd64` to match the on-prem target. Building also for
  `linux/arm64` via `docker buildx` makes local dev on Apple Silicon
  fast (no qemu) and futureproofs against arm64 servers.

- **Build-push to an internal registry.** Today every host that runs
  `docker compose build` rebuilds whisper.cpp from source. Build once
  in CI, push to ghcr.io or an internal registry, `docker compose pull`
  on-prem. Saves the C++ compile per host and gives you image-version
  pinning for rollbacks.

## Result-display duration cap

We removed the broadcast 6-second cue cap because it fragmented sentences
during slow speech (multi-second per-word durations turned single-line
sentences into orphan cues). With VAD now stripping music/silence,
per-word durations stay realistic, but a runaway holding cue can still
occur — e.g. a sung line where the model emits one word with a 7-second
duration. A _display-duration_ cap (cap how long any cue stays on screen,
splitting greedily if needed) would catch these without re-introducing
the old fragmentation. Not urgent; deferred until a real example bites.
