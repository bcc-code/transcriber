"""HTTP sidecar serving Meta Omnilingual ASR to the Go transcriber.

The model loads once at startup. POST /transcribe takes one audio file
(multipart field `audio`, any format ffmpeg can decode) and an optional
`lang` (Omnilingual code such as `nob_Latn`). The audio is split on silence
with Silero VAD into speech regions of at most OMNI_MAX_REGION_SEC (30 s), because the
non-"Unlimited" checkpoints reject inputs over 40 s. Each region is
transcribed as one segment; the model returns no word timestamps.

Response: {"language": str|null, "segments": [{"start", "end", "text"}]}
with times in seconds relative to the uploaded audio.
"""

from __future__ import annotations

import logging
import os
import subprocess
import tempfile
import threading
from contextlib import asynccontextmanager

import numpy as np
import torch
from fastapi import FastAPI, File, Form, HTTPException, UploadFile
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import JSONResponse
from silero_vad import get_speech_timestamps, load_silero_vad

from regions import MAX_SPEECH_SEC, plan_regions

SAMPLE_RATE = 16000
MODEL_CARD = os.environ.get("OMNI_MODEL_CARD", "omniASR_LLM_7B_v2")
BATCH_SIZE = int(os.environ.get("OMNI_BATCH_SIZE", "4"))

log = logging.getLogger("omnilingual")
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")

state: dict = {}
# One model on one GPU: serialize inference.
infer_lock = threading.Lock()


@asynccontextmanager
async def lifespan(app: FastAPI):
    # Imported here so `import server` (e.g. for the region helpers) doesn't
    # pull in fairseq2.
    from omnilingual_asr.models.inference.pipeline import ASRInferencePipeline

    log.info("loading %s", MODEL_CARD)
    state["vad"] = load_silero_vad()
    state["pipeline"] = ASRInferencePipeline(model_card=MODEL_CARD)
    log.info("model ready")
    yield
    state.clear()


app = FastAPI(lifespan=lifespan)


@app.get("/healthz")
def healthz():
    if "pipeline" not in state:
        return JSONResponse({"status": "loading"}, status_code=503)
    return {"status": "ok", "model": MODEL_CARD}


def decode(path: str) -> np.ndarray:
    """Decode any audio/video file to 16 kHz mono float32 via ffmpeg."""
    proc = subprocess.run(
        ["ffmpeg", "-nostdin", "-loglevel", "error", "-i", path,
         "-f", "f32le", "-ac", "1", "-ar", str(SAMPLE_RATE), "-"],
        capture_output=True,
    )
    if proc.returncode != 0:
        raise ValueError(f"ffmpeg: {proc.stderr.decode(errors='replace').strip()}")
    return np.frombuffer(proc.stdout, dtype=np.float32)


def transcribe_file(path: str, lang: str | None) -> dict:
    wav = decode(path)
    total = len(wav) / SAMPLE_RATE
    if total == 0:
        return {"language": lang, "segments": []}

    with infer_lock:
        ts = get_speech_timestamps(
            torch.from_numpy(wav.copy()),
            state["vad"],
            sampling_rate=SAMPLE_RATE,
            max_speech_duration_s=MAX_SPEECH_SEC,
            return_seconds=True,
        )
        regions = plan_regions([(t["start"], t["end"]) for t in ts], total)
        if not regions:
            return {"language": lang, "segments": []}

        inputs = [
            {"waveform": wav[int(s * SAMPLE_RATE):int(e * SAMPLE_RATE)], "sample_rate": SAMPLE_RATE}
            for s, e in regions
        ]
        texts = state["pipeline"].transcribe(
            inputs, lang=[lang] * len(inputs), batch_size=BATCH_SIZE
        )

    segments = [
        {"start": round(s, 3), "end": round(e, 3), "text": t.strip()}
        for (s, e), t in zip(regions, texts)
        if t.strip()
    ]
    return {"language": lang, "segments": segments}


@app.post("/transcribe")
async def transcribe(audio: UploadFile = File(...), lang: str | None = Form(None)):
    if "pipeline" not in state:
        raise HTTPException(503, "model not loaded")
    suffix = os.path.splitext(audio.filename or "")[1]
    with tempfile.NamedTemporaryFile(suffix=suffix) as tmp:
        while chunk := await audio.read(1 << 20):
            tmp.write(chunk)
        tmp.flush()
        try:
            return await run_in_threadpool(transcribe_file, tmp.name, lang or None)
        except ValueError as e:
            raise HTTPException(400, str(e)) from e
