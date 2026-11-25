# transcriber

Go-based transcription API. Drop-in compatible with the existing Python
service (`POST /transcription/job`, `GET /transcription/job/{id}`), with
an adapter system that lets you swap the underlying ASR backend
(whisper.cpp, stub, ...) per request via an additive `model` field.

## Run

Two modes:

**Development** — Go API + Nuxt dev server with hot reload.

```sh
make dev            # both at once; Ctrl-C stops both
# or in separate terminals:
make dev-api        # Go on :8888
make dev-frontend   # Nuxt on :3000 (proxies API calls)
```

**Single-binary** — SPA embedded in the Go binary, both served from `:8888`.

```sh
make build          # pnpm generate → internal/web/dist → go build
./transcriber
```

Both default to the `stub` adapter so they work without any ASR backend
installed. `internal/web/dist/` must be populated before the Go side will
compile (the `//go:embed` directive needs at least one file) — run
`make frontend` once after cloning, then `go run ./cmd/transcriber` works
on its own for API-only iteration.

The real adapters (`whisper-cpp-large-v3`, `nb-whisper-large`,
`nb-whisper-medium`) require
**whisper-cli** (whisper.cpp), **ffmpeg**, and **ffprobe** on `$PATH`:
the chunked wrapper uses ffprobe to read the input duration and ffmpeg
to extract each chunk to a 16kHz mono wav. Model files are downloaded
from Hugging Face on first use and cached on disk. The `stub` adapter
has no external dependencies.

On macOS, `brew install whisper-cpp ffmpeg` covers all three, and brew's
`whisper-cli` is Metal-accelerated — so a native run is both the fastest
and the only workable local option. **Do not use Docker for local
development:** the image requires an NVIDIA GPU (`whisper-cli` there is
linked against `libcuda.so.1`), so it cannot start on a Mac at all. See
DEPLOY.md.

## Configuration

The set of registered models lives in `cmd/transcriber/models.go` as typed
Go code. Server settings come from flags; per-machine paths from env vars.

| Flag                   | Default      | Meaning                                                                                                                                                                               |
| ---------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-port`                | `8888`       | HTTP listen port                                                                                                                                                                      |
| `-workers`             | `2`          | concurrent jobs on local models (whisper.cpp, GPU-bound)                                                                                                                              |
| `-remote-workers`      | `10`         | concurrent jobs on cloud models (ElevenLabs, OpenRouter), in their own queue: they neither wait behind nor hold up local jobs                                                        |
| `-callback-workers`    | `2`          | webhook delivery goroutines                                                                                                                                                           |
| `-default-model`       | `stub`       | adapter ID used when the request omits `model`                                                                                                                                        |
| `-default-prompt-file` | `prompts/whisper.txt` | free-text prompt used when the request omits one, for Whisper-style models; missing file = no default. A non-empty `prompt` in the request fully overrides it |
| `-default-keyterms-file` | `prompts/keyterms.txt` | keyterm list used instead of the free-text prompt for keyterm models (`elevenlabs-*`, `openrouter/deepgram/*`, `openrouter/elevenlabs/*`, `openrouter/assemblyai/*`); missing file = those models get the free-text prompt |
| `-job-timeout`         | `30m`        | wall-clock cap per job; on expiry the worker cancels the subprocess and marks the job `FAILED` with `error: "timeout"`. Per-request `timeout_seconds` overrides this. `<= 0` disables |
| `-max-terminal-jobs`   | `20`         | how many finished jobs (completed/failed/canceled) to retain in memory; `<= 0` disables the cap                                                                                       |
| `-log-format`          | `text`       | `text` for human-readable output (dev), `json` for structured logs (prod). The Dockerfile sets `json`                                                                                 |
| `-scratch-dir`         | _(OS temp)_  | where per-job scratch dirs are created for adapter intermediates (extracted chunk wavs, raw model output). Keep on local disk — chunking writes ~115 MB per hour of audio            |
| `-keep-work-dirs`      | `false`      | retain per-job scratch dirs after completion, for inspecting a bad transcription. They are large; off by default                                                                     |

| Env var             | Default                         | Meaning                                                                                                                                             |
| ------------------- | ------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| `WHISPER_CPP_BIN`   | `/opt/homebrew/bin/whisper-cli` | whisper.cpp binary                                                                                                                                  |
| `WHISPER_CPP_MODEL` | _(unset → fetched from HF)_     | local path override for the `whisper-cpp-large-v3` adapter. Unset = auto-download `ggerganov/whisper.cpp/ggml-large-v3.bin` via `internal/hfcache`. |
| `NB_WHISPER_MODEL`  | _(unset → fetched from HF)_     | local path override for the `nb-whisper-large` adapter. Unset = auto-download `NbAiLab/nb-whisper-large/ggml-model.bin` via `internal/hfcache`.     |
| `WHISPER_VAD_MODEL` | _(unset → fetched from HF)_     | local path override for the Silero VAD model used to skip music/silence. Unset = auto-download `ggml-org/whisper-vad/ggml-silero-v5.1.2.bin`.       |
| `XDG_CACHE_HOME`    | `~/.cache`                      | base for the HF cache (`<root>/transcriber/hf/<repo>/<file>`).                                                                                      |
| `ELEVENLABS_API_KEY` | _(unset)_ | enables the ElevenLabs Scribe backends (see below). Unset = not registered. |
| `ELEVENLABS_DEFAULT_MODEL` | `elevenlabs-scribe-v2` | which Scribe adapter the unversioned `elevenlabs-scribe` ID points to. |
| `ALIGNER_URL` | _(unset → off; compose sets `http://aligner:8000`)_ | forced-aligner service that re-times the local whisper models' words. See [Forced alignment](#forced-alignment). |
| `OPENROUTER_KEY` | _(unset)_ | enables the OpenRouter backends (see below). Unset = not registered. |
| `OPENROUTER_MODELS` | _(unset → Norwegian-capable list)_ | comma-separated OpenRouter model slugs to register instead of the built-in list. |
| `OPENROUTER_DEFAULT_MODEL` | `openai/whisper-large-v3` | which model the bare `openrouter` ID points to. |

## API

### `POST /transcription/job`

```json
{
  "path": "/mnt/storage/audio/foo.wav",
  "language": "no",
  "format": "all",
  "output_path": "/mnt/storage/out/foo/",
  "priority": 5,
  "callback": "https://example.com/hook",
  "model": "whisper-cpp-large-v3",
  "timeout_seconds": 1800
}
```

`model` is optional — omit to use the default. `format: "all"` writes
json+srt+vtt+txt; or pass a comma-separated subset like `"json,srt"`.
`timeout_seconds` is optional — omit to inherit the server's `-job-timeout`.

Send an `Idempotency-Key` header to dedupe retries: a repeated POST with the
same key returns the original job (`200 OK`) instead of creating a new one
(`202 Accepted`). The mapping lives as long as the job is in the store, so
once a job has been evicted (see `-max-terminal-jobs`) the same key starts
fresh.

```sh
# Submit a job, then poll until it completes.
JOB=$(curl -sS -X POST http://localhost:8888/transcription/job \
    -H 'content-type: application/json' \
    -d '{
        "path": "/mnt/storage/audio/foo.wav",
        "language": "no",
        "format": "all",
        "output_path": "/mnt/storage/out/foo/",
        "model": "whisper-cpp-large-v3"
    }' | jq -r .id)

while :; do
    curl -sS "http://localhost:8888/transcription/job/$JOB" | jq '{status, progress, result}'
    sleep 2
done
```

### `GET /transcription/job/{id}`

Returns the current job state. `status` is one of `PENDING`, `RUNNING`,
`COMPLETED`, `FAILED`, `CANCELED`. `progress` is 0–100. `result` is the
path to `transcript.json` once `COMPLETED`.

The JSON result includes word-level timestamps. Each segment carries a
`words` array (`text` / `start` / `end`) in addition to the segment-level
`text`/`start`/`end`. See
`internal/formats/testdata/golden/transcript.json` for the canonical
shape — this fixture is the source of truth that every adapter must
serialize to.

### ElevenLabs Scribe

With `ELEVENLABS_API_KEY` set, the cloud backends `elevenlabs-scribe-v3`, `elevenlabs-scribe-v2` and
`elevenlabs-scribe-v1` are registered. Pass any of them as `model` to choose it
per job. `elevenlabs/scribe_v3`, `elevenlabs/scribe_v2` and `elevenlabs/scribe_v1` work as aliases, and
`elevenlabs-scribe` resolves to `ELEVENLABS_DEFAULT_MODEL`. The input is
transcoded with ffmpeg to 16 kHz mono Opus and uploaded in a single request,
because Scribe handles long audio itself and no chunking is needed. On v2 and v3 the
`prompt` is sent as keyterms: comma-, newline- or semicolon-separated entries
of at most 5 words or 50 characters. Anything longer is ignored. Audio for these
jobs leaves the host.

### OpenRouter

With `OPENROUTER_KEY` set, OpenRouter speech-to-text models are registered as
`openrouter/<slug>`, for example `openrouter/openai/whisper-large-v3`. The
set is a hardcoded list of models whose vendors document Norwegian support
(`openRouterNorwegianModels` in `cmd/transcriber/models.go`). At boot it is
checked against OpenRouter's live model list and withdrawn models are skipped.
`OPENROUTER_MODELS` replaces the list. `openrouter` alone resolves to
`OPENROUTER_DEFAULT_MODEL`. Jobs use OpenRouter's `/api/v1/audio/transcriptions` endpoint with
`verbose_json` and segment and word timestamps. Input is split into 10-minute
chunks, and up to 4 chunks run at once. Each chunk is transcoded to 16 kHz mono
Opus, which keeps every upload far below the 25 MB limit. `language` and
`prompt` are passed through. Segment and word timestamps depend on the
provider. A provider that returns only text produces one segment with no
timing. Audio for these jobs leaves the host.

### Forced alignment

With `ALIGNER_URL` set, the local whisper models (`whisper-cpp-large-v3`,
`nb-whisper-large`, `nb-whisper-medium`, and the Nordlyd Qwen model) send their result to the aligner service (`aligner/`)
after transcription. It keeps whisper's text and moves each word's timestamps
to where a wav2vec2 CTC model finds it in the audio, which fixes words whisper
placed at the wrong side of a pause. Segment bounds follow their words.
Subtitle cues are built from word times, so they follow too.

Models per language (override with `ALIGN_MODELS`): Norwegian
`NbAiLab/nb-wav2vec2-1b-bokmaal-v2` (`no`/`nb`), `NbAiLab/nb-wav2vec2-300m-nynorsk`
(`nn`), and `jonatasgrosman/wav2vec2-large-xlsr-53-*` for `en`, `de`, `nl`.
Words with no letters the model knows, such as bare numbers, keep whisper's
time. Alignment is best effort: if the aligner is down, has no model for the
language or errors, the job completes with whisper's timestamps and a warning
is logged. Cloud backends are not aligned.

The aligner reads the job's audio path itself, so it needs the same mounts as
the transcriber. It has no authentication; docker-compose keeps it on the
internal network.

### Nordlyd Qwen3-ASR

With `QWEN_ASR_URL` set, `nordlyd-asr-qwen-0.6b` (alias
`NbAiLab/nordlyd-asr-qwen06B-nov26preview`) is registered as a local model.
It is served by the `qwen-asr/` service, which runs the model with the
`qwen-asr` package on the GPU. The model is gated on Hugging Face, so the
service needs an `HF_TOKEN` from an account that was granted access.
Without one, or if the model fails to load, the service stays up but reports
itself disabled on `/healthz`, and the transcriber leaves the model
unregistered. It checks once at boot (retrying for up to a minute), so restart
the transcriber after adding the token.

Qwen3-ASR returns text without timestamps. The service splits the audio at
quiet points into pieces of 20 s or less and returns each piece's text with
its span. The adapter spreads the words over the span by length, and the
aligner then places them, so word and subtitle timing depends on the aligner.
Without it, word times are estimates within each piece. The request `prompt`
is passed as the model's context, as a keyterm list. Norwegian cannot be
forced in `qwen-asr`, so for `no`/`nb`/`nn` the model detects the language
itself; codes it does support, such as `en`, are forced.

### Additive endpoints

- `GET  /transcription/jobs` — list all jobs
- `DELETE /transcription/job/{id}` — cancel a queued or running job
- `GET  /models` — list registered adapters
- `GET  /config` — server-side defaults (`default_prompt`, `default_keyterms`, `default_language`)
- `GET  /models` also reports each model's `prompt_style` (`text` or `keyterms`) and the `default_prompt` it gets
- `GET  /healthz`, `GET /readyz`

## Adding a new backend

1. Implement `transcriber.Transcriber` in `internal/transcriber/<name>/`.
2. Add another `r.Register(...)` call in `cmd/transcriber/models.go` with
   the adapter's typed `Config`. Use a distinct ID per variant
   (e.g. `whisper-cpp-large-v3`, `whisper-cpp-medium`) so you can A/B
   test by passing `"model": "..."` in the request body.
3. Wrap the adapter in `chunked.New(inner, chunked.Config{})` at
   registration time if it should handle long-form audio — the wrapper
   passes short files (≤ `ChunkLengthSec`, default 5 min) through
   unchanged and chunks longer files transparently.
4. Add a `testdata/raw.json` fixture and a parser test in the adapter
   package that round-trips the parsed `Transcription` through
   `formats.Write` and byte-compares each output against
   `internal/formats/testdata/golden/transcript.<ext>`. This is the
   contract every adapter is held to.
