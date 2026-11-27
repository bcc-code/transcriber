# Drop-in compatibility with the legacy Whisper API

This service is intended as a drop-in replacement for the old Python-backed
Whisper API. The endpoint paths, HTTP verbs, and JSON field names match.
What follows is the delta between the two — things a legacy caller would
hit if it pointed at this server unchanged.

## Accepted behavioral differences

Verified against the current caller — none of the items below produce a
real failure. They're documented for context if a future caller appears.

- **`result` field semantics.** Old: accumulated stdout from the python child process (mostly debug noise). New: filesystem path to `transcript.json`. The caller never reads `result`, so this is invisible.
- **Callback firing on failure.** Old fired `doCallback` only on `COMPLETED`. New fires for `FAILED` and `CANCELED` as well. The caller doesn't register a callback (it polls), so this is invisible.
- **`output_path` is required.** Old accepted empty; new returns `400`. The caller always sets it.
- **`format` field honoring.** Old python ignored `format` entirely and always wrote all five files. New honors `format` strictly. The caller always sends `format: "all"`, which writes all five (now including `words.srt`).
- **`list` contents.** Old kept only the last 10 processed jobs in memory. New persists jobs in SQLite and keeps terminal ones (completed/failed/canceled) for `-retention` (default 7 days) after they end; active jobs are never pruned. The list is larger, and survives restarts.
- **`no_speech` field added.** `true` on a `COMPLETED` job whose audio held no speech; its output files are written but empty. Old had no such signal. The status stays `COMPLETED` rather than getting a new value, because the caller polls forever on unknown statuses.
- **Status set additive.** New emits `CANCELED` (in addition to the old `QUEUED|RUNNING|COMPLETED|FAILED`); the caller's status switch falls through unknowns and keeps polling.

## Missing endpoints

- `GET /smi` — old shelled out to `nvidia-smi` and returned text. Only meaningful on GPU boxes; skip unless ops asks.
