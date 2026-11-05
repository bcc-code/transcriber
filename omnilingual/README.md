# Omnilingual ASR sidecar

HTTP service that serves [Meta Omnilingual ASR](https://github.com/facebookresearch/omnilingual-asr)
to the Go transcriber. The model is Python-only (fairseq2 + PyTorch) and a 7B
checkpoint is far too slow to load per chunk, so it runs as a long-lived
process. The Go adapter (`internal/transcriber/omnilingual`) registers the
`omnilingual-7b` model when `OMNILINGUAL_URL` is set.

## What it does

`POST /transcribe` (multipart: `audio` file, optional `lang` such as `nob_Latn`):

1. Decodes the audio with ffmpeg to 16 kHz mono.
2. Splits it on silence with Silero VAD into regions of at most 30 s. The
   non-"Unlimited" checkpoints reject inputs over 40 s.
3. Transcribes all regions in batches and returns one segment per region:
   `{"language": ..., "segments": [{"start", "end", "text"}]}`.

`GET /healthz` returns 503 while the model loads and 200 after.

Limitations compared with the whisper adapters:

- No word timestamps, so subtitles get one cue per VAD region.
- No prompt or vocabulary support. The job's `prompt` is ignored.
- Text is likely lowercase with little punctuation, because upstream's
  tokenizer stripped punctuation.
- Norwegian is Bokmål only (`nob_Latn`). `nn` requests run with no language
  hint.

## Configuration

| Env var               | Default             | Meaning                                                                                       |
| --------------------- | ------------------- | --------------------------------------------------------------------------------------------- |
| `OMNI_MODEL_CARD`     | `omniASR_LLM_7B_v2` | fairseq2 model card. `omniASR_CTC_7B_v2` is ~16× faster, uses ~15 GiB and ignores `lang`.     |
| `OMNI_BATCH_SIZE`     | `4`                 | VAD regions per forward pass. Raise it if VRAM allows.                                        |
| `OMNI_MAX_REGION_SEC` | `30`                | Longest region sent to the model. Must stay below 40.                                         |

VRAM (BF16, upstream figures): ~17 GiB for LLM 7B and ~15 GiB for CTC 7B.

## Run natively (Linux + NVIDIA GPU)

```sh
cd omnilingual
uv sync --locked
uv run uvicorn server:app --port 8000
# in the repo root:
OMNILINGUAL_URL=http://localhost:8000 go run ./cmd/transcriber
```

The lock targets linux/x86_64 only, because fairseq2 has no macOS wheels.

## Smoke test

```sh
curl -F audio=@sample.wav -F lang=nob_Latn localhost:8000/transcribe
```

## Tests

```sh
uv run --no-project --with pytest pytest -q test_regions.py
```
