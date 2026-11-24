"""Forced-alignment service for the transcriber.

POST /align re-times whisper's words against the audio with a wav2vec2 CTC
model for the language. The transcriber sends the words it already has;
this service only moves their timestamps.

Request:
  {"audio_path": "/mnt/storage/in.wav", "language": "no",
   "segments": [{"words": [{"text": "Hei", "start": 0.1, "end": 0.4}, ...]}]}
Response:
  {"model": "NbAiLab/...", "segments": [{"words": [{"start": 0.12, "end": 0.38} | null, ...]}]}

A null word could not be aligned (e.g. a bare number, which CTC vocabularies
don't contain); the caller keeps whisper's time for it. 422 means there is no
alignment model for the language.

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
from transformers import Wav2Vec2FeatureExtractor, Wav2Vec2ForCTC

from align import Word, align_words, group_words

SAMPLE_RATE = 16000
# Audio around each window, so words whisper timed slightly wrong still fit.
PAD_SEC = float(os.environ.get("ALIGN_PAD_SEC", "0.5"))

DEFAULT_MODELS = {
    "no": "NbAiLab/nb-wav2vec2-1b-bokmaal-v2",
    "nb": "NbAiLab/nb-wav2vec2-1b-bokmaal-v2",
    "nn": "NbAiLab/nb-wav2vec2-300m-nynorsk",
    "en": "jonatasgrosman/wav2vec2-large-xlsr-53-english",
    "de": "jonatasgrosman/wav2vec2-large-xlsr-53-german",
    "nl": "jonatasgrosman/wav2vec2-large-xlsr-53-dutch",
}

log = logging.getLogger("aligner")


def model_table() -> dict[str, str]:
    """DEFAULT_MODELS, overridden per language by ALIGN_MODELS
    ("no=org/model,en=org/model")."""
    table = dict(DEFAULT_MODELS)
    for item in os.environ.get("ALIGN_MODELS", "").split(","):
        if "=" in item:
            lang, name = item.split("=", 1)
            table[lang.strip()] = name.strip()
    return table


class Model:
    def __init__(self, name: str, device: str):
        self.name = name
        self.device = device
        self.dtype = torch.float16 if device == "cuda" else torch.float32
        self.features = Wav2Vec2FeatureExtractor.from_pretrained(name)
        self.model = Wav2Vec2ForCTC.from_pretrained(name, dtype=self.dtype).to(device).eval()
        vocab_path = _hf_file(name, "vocab.json")
        with open(vocab_path, encoding="utf-8") as f:
            vocab: dict[str, int] = json.load(f)
        self.blank = vocab.get("<pad>", self.model.config.pad_token_id or 0)
        self.delimiter = vocab.get("|")
        # Single characters only; specials like <s>, </s>, <unk> never match.
        self.vocab = {k: v for k, v in vocab.items() if len(k) == 1 and k != "|"}

    @torch.inference_mode()
    def log_probs(self, audio: np.ndarray) -> np.ndarray:
        inputs = self.features(audio, sampling_rate=SAMPLE_RATE, return_tensors="pt")
        values = inputs.input_values.to(self.device, self.dtype)
        logits = self.model(values).logits[0].float()
        return torch.log_softmax(logits, dim=-1).cpu().numpy()


def _hf_file(repo: str, filename: str) -> str:
    from huggingface_hub import hf_hub_download

    return hf_hub_download(repo, filename)


class Aligner:
    def __init__(self):
        self.device = "cuda" if torch.cuda.is_available() else "cpu"
        self.table = model_table()
        self.models: dict[str, Model] = {}
        # One GPU, one model call at a time; audio decoding runs in parallel.
        self.lock = threading.Lock()

    def model_for(self, language: str) -> Model | None:
        name = self.table.get(language)
        if not name:
            return None
        if name not in self.models:
            log.info("loading %s on %s", name, self.device)
            self.models[name] = Model(name, self.device)
        return self.models[name]

    def align(self, req: dict) -> tuple[int, dict]:
        language = (req.get("language") or "").lower()
        segments = req.get("segments") or []
        with self.lock:
            model = self.model_for(language)
        if model is None:
            return 422, {"error": f"no alignment model for language {language!r}"}

        audio = load_audio(req["audio_path"])
        duration = len(audio) / SAMPLE_RATE
        out = []
        aligned = total = 0
        for seg in segments:
            words = [Word(w["text"], float(w["start"]), float(w["end"])) for w in seg.get("words") or []]
            result: list[dict | None] = [None] * len(words)
            for group in group_words(words):
                start = max(0.0, words[group[0]].start - PAD_SEC)
                end = min(duration, words[group[-1]].end + PAD_SEC)
                window = audio[int(start * SAMPLE_RATE) : int(end * SAMPLE_RATE)]
                if len(window) < SAMPLE_RATE // 10:
                    continue
                with self.lock:
                    lp = model.log_probs(window)
                frame_sec = len(window) / SAMPLE_RATE / lp.shape[0]
                times = align_words(
                    [words[i].text for i in group], lp, model.vocab, model.blank,
                    frame_sec, start, delimiter=model.delimiter,
                )
                for i, t in zip(group, times):
                    if t is not None:
                        result[i] = {"start": t[0], "end": t[1]}
            aligned += sum(r is not None for r in result)
            total += len(result)
            out.append({"words": result})
        log.info("aligned %d/%d words in %s (%.0fs audio, %s)", aligned, total, req["audio_path"], duration, model.name)
        return 200, {"model": model.name, "segments": out}


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


ALIGNER: Aligner | None = None


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
            self._send(200, {"status": "ok", "device": ALIGNER.device, "languages": sorted(ALIGNER.table)})
        else:
            self._send(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/align":
            self._send(404, {"error": "not found"})
            return
        t0 = time.monotonic()
        try:
            length = int(self.headers.get("Content-Length", "0"))
            req = json.loads(self.rfile.read(length))
            status, body = ALIGNER.align(req)
        except (KeyError, ValueError, TypeError) as e:
            status, body = 400, {"error": f"bad request: {e}"}
        except Exception as e:  # noqa: BLE001 - report, keep serving
            log.exception("align failed")
            status, body = 500, {"error": str(e)}
        body["seconds"] = round(time.monotonic() - t0, 2)
        self._send(status, body)

    def log_message(self, fmt, *args):
        log.debug(fmt, *args)


def main():
    global ALIGNER
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    ALIGNER = Aligner()
    port = int(os.environ.get("PORT", "8000"))
    log.info("listening on :%d (device %s)", port, ALIGNER.device)
    for lang in filter(None, os.environ.get("ALIGN_PRELOAD", "").split(",")):
        ALIGNER.model_for(lang.strip())
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
