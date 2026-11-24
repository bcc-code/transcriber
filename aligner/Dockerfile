# Forced-alignment service: re-times whisper's words with wav2vec2 CTC models.
# See server.py for the API. Built by .github/workflows/image.yml as
# ghcr.io/<owner>/<repo>-aligner. Needs an NVIDIA GPU (falls back to CPU,
# which is slow but works).
#
# CUDA 12.6 to match the whisper image, so both run on the same driver.
FROM pytorch/pytorch:2.14.1-cuda12.6-cudnn9-runtime

RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY align.py server.py ./

# Hugging Face model cache; mount a volume here so models survive restarts.
ENV HF_HOME=/models \
    PORT=8000 \
    PYTHONUNBUFFERED=1
EXPOSE 8000

CMD ["python", "server.py"]
