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

The real adapters (`whisper-cpp-large-v3`, `nb-whisper-large`) require
**whisper-cli** (whisper.cpp), **ffmpeg**, and **ffprobe** on `$PATH`:
the chunked wrapper uses ffprobe to read the input duration and ffmpeg
to extract each chunk to a 16kHz mono wav. Model files are downloaded
from Hugging Face on first use and cached on disk. The `stub` adapter
has no external dependencies.

## Configuration

The set of registered models lives in `cmd/transcriber/models.go` as typed
Go code. Server settings come from flags; per-machine paths from env vars.

| Flag                   | Default      | Meaning                                                                                                                                                                               |
| ---------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-port`                | `8888`       | HTTP listen port                                                                                                                                                                      |
| `-workers`             | `2`          | concurrent transcription jobs                                                                                                                                                         |
| `-callback-workers`    | `2`          | webhook delivery goroutines                                                                                                                                                           |
| `-default-model`       | `stub`       | adapter ID used when the request omits `model`                                                                                                                                        |
| `-default-prompt-file` | `prompt.txt` | file whose contents are used as the prompt when the request omits one; missing file = no default. A non-empty `prompt` in the request fully overrides it                              |
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

### Additive endpoints

- `GET  /transcription/jobs` — list all jobs
- `DELETE /transcription/job/{id}` — cancel a queued or running job
- `GET  /models` — list registered adapters
- `GET  /config` — server-side defaults (currently `default_prompt`)
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
