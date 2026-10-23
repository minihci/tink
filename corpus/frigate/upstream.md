# Frigate NVR (Docker)

Upstream install instructions, verbatim. Retrieved 2026-10-23.

Source: https://docs.frigate.video/frigate/installation (source: github.com/blakeblackshear/frigate, docs/docs/frigate/installation.md, dev at 91e91c68ef; the page's compose-generator widget is not reproduced)

## docker compose (the page's example)

The page says: "Running through Docker with Docker Compose is the recommended install method."

```yaml
services:
  frigate:
    container_name: frigate
    # privileged: true # ONLY enable if your hardware requires it (see hardware-specific docs); prefer the device mappings below
    restart: unless-stopped
    stop_grace_period: 30s # allow enough time to shut down the various services
    image: ghcr.io/blakeblackshear/frigate:stable
    shm_size: "512mb" # update for your cameras based on calculation above
    devices:
      - /dev/bus/usb:/dev/bus/usb # Passes the USB Coral, needs to be modified for other versions
      - /dev/apex_0:/dev/apex_0 # Passes a PCIe Coral, follow driver instructions here https://github.com/jnicolson/gasket-builder
      - /dev/video11:/dev/video11 # For Raspberry Pi 4B
      - /dev/dri/renderD128:/dev/dri/renderD128 # AMD / Intel GPU, needs to be updated for your hardware
      - /dev/kfd:/dev/kfd # AMD Kernel Fusion Driver for ROCm
      - /dev/accel:/dev/accel # AMD / Intel NPU
    volumes:
      - /etc/localtime:/etc/localtime:ro
      - /path/to/your/config:/config
      - /path/to/your/storage:/media/frigate
      - type: tmpfs # 1GB In-memory filesystem for recording segment storage
        target: /tmp/cache
        tmpfs:
          size: 1000000000
    ports:
      - "8971:8971"
      # - "5000:5000" # Internal unauthenticated access. Expose carefully.
      - "8554:8554" # RTSP feeds
      - "8555:8555/tcp" # WebRTC over tcp
      - "8555:8555/udp" # WebRTC over udp
    environment:
      FRIGATE_RTSP_PASSWORD: "password"
```

## docker run

"If you can't use Docker Compose, you can run the container with something similar to this:"

```bash
docker run -d \
  --name frigate \
  --restart=unless-stopped \
  --stop-timeout 30 \
  --mount type=tmpfs,target=/tmp/cache,tmpfs-size=1000000000 \
  --device /dev/bus/usb:/dev/bus/usb \
  --device /dev/dri/renderD128 \
  --shm-size=64m \
  -v /path/to/your/storage:/media/frigate \
  -v /path/to/your/config:/config \
  -v /etc/localtime:/etc/localtime:ro \
  -e FRIGATE_RTSP_PASSWORD='password' \
  -p 8971:8971 \
  -p 8554:8554 \
  -p 8555:8555/tcp \
  -p 8555:8555/udp \
  ghcr.io/blakeblackshear/frigate:stable
```

## What the page says about the host (quoted)

> **MQTT broker (optional)** - An MQTT broker is optional with Frigate, but is required for the Home Assistant integration. If using Home Assistant, Frigate and Home Assistant must be connected to the same MQTT broker.

> Frigate runs best with Docker installed on bare metal Debian-based distributions. For ideal performance, Frigate needs low overhead access to underlying hardware for the Coral and GPU devices. Running Frigate in a VM on top of Proxmox, ESXi, Virtualbox, etc. is not recommended though [some users have had success with Proxmox](#proxmox).

> Frigate uses the following locations for read/write operations in the container. Docker volume mappings can be used to map these to any location on your host machine.

> - `/config`: Used to store the Frigate config file and sqlite database. You will also see a few files alongside the database file while Frigate is running.
> - `/media/frigate/clips`: Used for snapshot storage. In the future, it will likely be renamed from `clips` to `snapshots`. The file structure here cannot be modified and isn't intended to be browsed or managed manually.
> - `/media/frigate/recordings`: Internal system storage for recording segments. The file structure here cannot be modified and isn't intended to be browsed or managed manually.
> - `/media/frigate/exports`: Storage for clips and timelapses that have been exported via the WebUI or API.
> - `/tmp/cache`: Cache location for recording segments. Initial recordings are written here before being checked and converted to mp4 and moved to the recordings folder. Segments generated via the `clip.mp4` endpoints are also concatenated and processed here. It is recommended to use a [`tmpfs`](https://docs.docker.com/storage/tmpfs/) mount for this.
> - `/dev/shm`: Internal cache for raw decoded frames in shared memory. It is not recommended to modify this directory or map it with docker. The minimum size is impacted by the `shm-size` calculations below.

> The following ports are used by Frigate and can be mapped via docker as required.

> | Port   | Description                                                                                                                                                                |
> | ------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
> | `8971` | Authenticated UI and API access without TLS. Reverse proxies should use this port.                                                                                         |
> | `5000` | Internal unauthenticated UI and API access. Access to this port should be limited. Intended to be used within the docker network for services that integrate with Frigate. |
> | `8554` | RTSP restreaming. By default, these streams are unauthenticated. Authentication can be configured in go2rtc section of config.                                             |
> | `8555` | WebRTC connections for cameras with two-way talk support.                                                                                                                  |

> Frigate utilizes shared memory to store frames during processing. The default `shm-size` provided by Docker is **64MB**.

> Frigate does not need elevated container privileges for most setups. The following hardens the container; add the `devices`/`group_add` entries your hardware requires (see the hardware acceleration docs):

The hardening the page recommends (add the `devices` your hardware needs):

```yaml
services:
  frigate:
    ...
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
```
