# Piper, as the Wyoming Piper image (Docker)

Upstream install instructions, verbatim. Retrieved 2026-10-23.

Source: https://github.com/rhasspy/wyoming-piper#docker-image (source: github.com/rhasspy/wyoming-piper, README.md, main at 544902bc7f)

## The Docker Image section, to the end of the README

From the README's "Docker Image" heading to its end, unchanged. The earlier sections (the Home Assistant add-on, a local install, phonemizers, a web UI) are not copied.

## Docker Image

``` sh
docker run -it \
    -p 10200:10200 \
    -v /path/to/local/data:/data \
    rhasspy/wyoming-piper \
    --voice en_US-lessac-medium
```

OmniVoice ships as a separate `omnivoice` tag, because it pulls in torch and
transformers. It is built for `linux/amd64` only — OmniVoice needs a
desktop/server CPU:

``` sh
docker run -it \
    -p 10200:10200 \
    -v /path/to/local/data:/data \
    rhasspy/wyoming-piper:omnivoice \
    --backend omnivoice \
    --omnivoice-ref-dir /data/cloned-voices \
    --omnivoice-steps 10  # higher = better quality but slower
```

The voice management web UI is **off by default**. It has no authentication, so
only enable it on a network you trust, and add `--web-server-allow` to limit it
to the clients that should reach it:

``` sh
docker run -it \
    -p 10200:10200 -p 5000:5000 \
    -v /path/to/local/data:/data \
    rhasspy/wyoming-piper \
    --voice en_US-lessac-medium \
    --web-server --web-server-host 0.0.0.0
```

### NVIDIA GPU image

Build the local GPU image with:

``` sh
docker build -f Dockerfile.gpu -t wyoming-piper:gpu .
```

The image contains CUDA-enabled PyTorch and ONNX Runtime, includes OmniVoice,
and enables `--use-cuda` automatically. Run Piper with the NVIDIA Container
Toolkit and a persistent data directory:

``` sh
docker run --rm -it \
    --gpus all \
    -p 10200:10200 \
    -v /path/to/local/data:/data \
    wyoming-piper:gpu \
    --voice en_US-lessac-medium
```

Set `WYOMING_PIPER_ARGS` to a shell-style argument string when Docker Compose
environment variables are more convenient than `command`. For example:

``` yaml
services:
  piper:
    build:
      context: .
      dockerfile: Dockerfile.gpu
    gpus: all
    ports:
      - "10200:10200"
    volumes:
      - ./data:/data
    environment:
      WYOMING_PIPER_ARGS: >-
        --voice en_US-lessac-medium
```

To run OmniVoice instead, replace the environment value with:

``` yaml
      WYOMING_PIPER_ARGS: >-
        --backend omnivoice
        --omnivoice-ref-dir /data/cloned-voices
        --omnivoice-steps 10
```

The host must have an NVIDIA driver and the NVIDIA Container Toolkit. The GPU
image is currently `linux/amd64` only because its PyTorch base image is amd64.

### Container health check

The image has a health check that asks the server for its info over the Wyoming
protocol, so `docker ps` reports `unhealthy` if the server stops answering. It
assumes the default `tcp://0.0.0.0:10200`; if you override `--uri`, override the
check to match:

``` sh
docker run -it \
    -p 10300:10300 \
    -v /path/to/local/data:/data \
    --health-cmd '/usr/src/.venv/bin/python3 -m wyoming_piper.health_check --uri tcp://127.0.0.1:10300' \
    rhasspy/wyoming-piper \
    --uri tcp://0.0.0.0:10300 \
    --voice en_US-lessac-medium
```

Loading the backend can take minutes on first run, since the model has to be
downloaded before the server starts listening. The check's start period allows
for that, so the container reports `starting` rather than `unhealthy` until then.

[Source](https://github.com/rhasspy/wyoming-addons/tree/master/piper)
