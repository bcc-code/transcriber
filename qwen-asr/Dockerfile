# Qwen3-ASR service: runs NbAiLab's Nordlyd Qwen3-ASR fine-tune. See server.py
# for the API. Built by .github/workflows/image.yml as
# ghcr.io/<owner>/<repo>-qwen-asr. Needs an NVIDIA GPU (falls back to CPU,
# which is slow but works).
#
# CUDA 12.6 to match the whisper and aligner images, so all run on the same driver.
FROM pytorch/pytorch:2.14.1-cuda12.6-cudnn9-runtime

RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt .
# The base image's Python is Ubuntu's system python3.12 (no conda since
# pytorch 2.14), which PEP 668 marks externally managed. torch lives in that
# environment under /usr/local, so install there too rather than in a venv.
RUN pip install --no-cache-dir --break-system-packages -r requirements.txt
COPY server.py ./

# Hugging Face model cache; mount a volume here so models survive restarts.
# The Nordlyd model is gated: pass HF_TOKEN from an account that has access.
ENV HF_HOME=/models \
    PORT=8000 \
    PYTHONUNBUFFERED=1
EXPOSE 8000

CMD ["python", "server.py"]
