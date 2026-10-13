# Start-up

Done by: jsametz

Date: 2026-10-12

---
1. Connect to the host

   ```sh
   ssh <user>@<host>
   ```

2. Check the GPU driver (needs >= 560.28.03)

   ```sh
   nvidia-smi
   ```

3. Install Docker

   ```sh
   curl -fsSL https://get.docker.com | sudo sh
   sudo usermod -aG docker $USER
   exit
   ```

4. Reconnect

   ```sh
   ssh <user>@<host>
   ```

5. Install NVIDIA Container Toolkit

   ```sh
   curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
     | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
   curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
     | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
     | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
   sudo apt update && sudo apt install -y nvidia-container-toolkit
   sudo nvidia-ctk runtime configure --runtime=docker
   sudo systemctl restart docker
   ```

6. Check Docker sees the GPU

   ```sh
   docker run --rm --gpus all nvidia/cuda:12.6.3-base-ubuntu24.04 nvidia-smi
   ```

7. Clone the repo

   ```sh
   git clone https://github.com/bcc-code/transcriber.git
   cd transcriber
   ```

8. Create storage

   ```sh
   sudo mkdir -p /mnt/storage/out
   ```

9. Configure

   ```sh
   cp .env.example .env
   ```

10. Pull and start

    ```sh
    docker compose pull
    docker compose up -d
    ```

11. Check health

    ```sh
    docker compose ps
    curl -fsS -w '\n' localhost:8888/healthz
    ```

12. Submit a test job

    ```sh
    curl -sS -X POST localhost:8888/transcription/job -H 'content-type: application/json' \
      -d '{"path":"/mnt/storage/<file>","language":"no","format":"all","output_path":"/mnt/storage/out/test1"}'
    ```

13. Watch logs

    ```sh
    docker compose logs -f transcriber
    ```

14. Check output

    ```sh
    ls -lh /mnt/storage/out/test1
    ```
