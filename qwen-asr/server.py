"""Qwen3-ASR transcription service for the transcriber.

Runs NbAiLab's Nordlyd fine-tune of Qwen3-ASR (or any Qwen3-ASR checkpoint)
with the qwen-asr package's transformers backend.

POST /transcribe
  {"audio_path": "/mnt/storage/chunk.wav", "language": "no", "context": "Brunstad, ..."}
Response:
  {"model": "NbAiLab/...", "language": "no",
   "segments": [{"start": 0.0, "end": 18.4, "text": "..."}, ...]}

Qwen3-ASR returns text without timestamps. The audio is split into short
pieces at low-energy points and each piece is transcribed on its own, so
every segment's text is known to lie within [start, end]. The transcriber
spreads the words over that span and the aligner service re-times them.

The service reads audio_path directly, so the audio mounts must be the same
as the transcriber's. It has no authentication: keep it off public networks.
"""

from __future__ import annotations

import json
import logging
import os
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import numpy as np
import torch
from qwen_asr import Qwen3ASRModel
from qwen_asr.inference.utils import SUPPORTED_LANGUAGES, split_audio_into_chunks

SAMPLE_RATE = 16000
MODEL_NAME = os.environ.get("QWEN_ASR_MODEL", "NbAiLab/nordlyd-asr-qwen06B-nov26preview")
# Longest piece transcribed in one go. Short pieces keep the untimed text
# local enough for the forced aligner (which aligns windows of <= 30 s).
PIECE_SEC = float(os.environ.get("QWEN_ASR_PIECE_SEC", "20"))
BATCH = int(os.environ.get("QWEN_ASR_BATCH", "16"))
# Pieces quieter than this (RMS, full scale = 1.0) are skipped: the model
# tends to invent text for silence.
SILENCE_RMS = float(os.environ.get("QWEN_ASR_SILENCE_RMS", "0.002"))

# ISO 639-1 -> Qwen3-ASR language name. Only languages the package can force
# are listed; anything else (Norwegian included) is left to the model, which
# the Nordlyd fine-tune handles itself.
CODE_TO_NAME = {
    "en": "English", "de": "German", "fr": "French", "es": "Spanish",
    "pt": "Portuguese", "it": "Italian", "nl": "Dutch", "sv": "Swedish",
    "da": "Danish", "fi": "Finnish", "pl": "Polish", "cs": "Czech",
    "ru": "Russian", "ro": "Romanian", "hu": "Hungarian", "el": "Greek",
    "tr": "Turkish", "zh": "Chinese", "ja": "Japanese", "ko": "Korean",
    "ar": "Arabic",
}
NAME_TO_CODE = {v.lower(): k for k, v in CODE_TO_NAME.items()} | {
    "norwegian": "no", "bokmål": "nb", "bokmal": "nb", "nynorsk": "nn",
    "norwegian bokmål": "nb", "norwegian bokmal": "nb", "norwegian nynorsk": "nn",
}

log = logging.getLogger("qwen-asr")


class Disabled(Exception):
    """The model is unavailable (no token, or it failed to load)."""


class Service:
    def __init__(self):
        self.device = "cuda:0" if torch.cuda.is_available() else "cpu"
        self.model: Qwen3ASRModel | None = None
        # The Nordlyd model is gated, so without a token there is nothing to
        # load. The service still runs and reports itself disabled, and the
        # transcriber leaves the model unregistered.
        self.disabled_reason = "" if os.environ.get("HF_TOKEN") else "HF_TOKEN not set"
        # One GPU, one generate call at a time; audio decoding runs in parallel.
        self.lock = threading.Lock()

    def load(self) -> Qwen3ASRModel:
        if self.disabled_reason:
            raise Disabled(self.disabled_reason)
        if self.model is None:
            log.info("loading %s on %s", MODEL_NAME, self.device)
            self.model = Qwen3ASRModel.from_pretrained(
                MODEL_NAME,
                dtype=torch.bfloat16 if self.device != "cpu" else torch.float32,
                device_map=self.device,
                max_inference_batch_size=BATCH,
                max_new_tokens=512,
            )
        return self.model

    def preload(self):
        """Load in the background so /healthz answers during the download. A
        failed load (e.g. a token without access) disables the service
        instead of crashing it."""
        try:
            with self.lock:
                self.load()
        except Disabled:
            pass
        except Exception as e:  # noqa: BLE001
            log.exception("loading %s failed; disabled", MODEL_NAME)
            self.disabled_reason = f"loading {MODEL_NAME} failed: {e}"

    def transcribe(self, req: dict) -> tuple[int, dict]:
        if self.disabled_reason:
            raise Disabled(self.disabled_reason)
        code = (req.get("language") or "").lower()
        force = CODE_TO_NAME.get(code)
        if force is not None and force not in SUPPORTED_LANGUAGES:
            force = None
        context = req.get("context") or ""

        audio = load_audio(req["audio_path"])
        duration = len(audio) / SAMPLE_RATE
        pieces = [
            (wav, offset)
            for wav, offset in split_audio_into_chunks(audio, SAMPLE_RATE, max_chunk_sec=PIECE_SEC)
            if len(wav) and float(np.sqrt(np.mean(np.square(wav)))) >= SILENCE_RMS
        ]
        segments = []
        detected: list[str] = []
        if pieces:
            with self.lock:
                model = self.load()
                results = model.transcribe(
                    audio=[(wav, SAMPLE_RATE) for wav, _ in pieces],
                    context=[context] * len(pieces),
                    language=[force] * len(pieces),
                )
            for (wav, offset), r in zip(pieces, results):
                text = " ".join((r.text or "").split())
                if not text:
                    continue
                segments.append({
                    "start": round(offset, 3),
                    "end": round(offset + len(wav) / SAMPLE_RATE, 3),
                    "text": text,
                })
                detected.extend(l for l in (r.language or "").split(",") if l)

        language = code if code and code != "auto" else language_code(detected)
        log.info("transcribed %s: %.0fs audio, %d/%d pieces with text, language %s",
                 req["audio_path"], duration, len(segments), len(pieces), language or "?")
        return 200, {"model": MODEL_NAME, "language": language, "segments": segments}


def language_code(names: list[str]) -> str:
    """Most frequent detected language as an ISO code ("" if unknown)."""
    counts: dict[str, int] = {}
    for n in names:
        c = NAME_TO_CODE.get(n.strip().lower())
        if c:
            counts[c] = counts.get(c, 0) + 1
    return max(counts, key=counts.get) if counts else ""


def load_audio(path: str) -> np.ndarray:
    """Whole file as 16 kHz mono float32, decoded by ffmpeg."""
    proc = subprocess.run(
        ["ffmpeg", "-nostdin", "-loglevel", "error", "-i", path,
         "-f", "f32le", "-ac", "1", "-ar", str(SAMPLE_RATE), "-"],
        capture_output=True, check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(f"ffmpeg: {proc.stderr.decode(errors='replace').strip()}")
    return np.frombuffer(proc.stdout, dtype=np.float32)


SERVICE: Service | None = None


class Handler(BaseHTTPRequestHandler):
    def _send(self, status: int, body: dict):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/healthz":
            self._send(200, {"status": "ok", "device": SERVICE.device, "model": MODEL_NAME,
                             "enabled": not SERVICE.disabled_reason,
                             "disabled_reason": SERVICE.disabled_reason,
                             "loaded": SERVICE.model is not None})
        else:
            self._send(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/transcribe":
            self._send(404, {"error": "not found"})
            return
        t0 = time.monotonic()
        try:
            length = int(self.headers.get("Content-Length", "0"))
            req = json.loads(self.rfile.read(length))
            status, body = SERVICE.transcribe(req)
        except Disabled as e:
            status, body = 503, {"error": f"disabled: {e}"}
        except (KeyError, ValueError, TypeError) as e:
            status, body = 400, {"error": f"bad request: {e}"}
        except Exception as e:  # noqa: BLE001 - report, keep serving
            log.exception("transcribe failed")
            status, body = 500, {"error": str(e)}
        body["seconds"] = round(time.monotonic() - t0, 2)
        self._send(status, body)

    def log_message(self, fmt, *args):
        log.debug(fmt, *args)


def main():
    global SERVICE
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    SERVICE = Service()
    port = int(os.environ.get("PORT", "8000"))
    if SERVICE.disabled_reason:
        log.warning("disabled: %s", SERVICE.disabled_reason)
    elif os.environ.get("QWEN_ASR_PRELOAD", "1") not in ("", "0", "false"):
        threading.Thread(target=SERVICE.preload, daemon=True).start()
    log.info("listening on :%d (device %s)", port, SERVICE.device)
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
