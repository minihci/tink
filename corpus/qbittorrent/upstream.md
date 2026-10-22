# qBittorrent (LinuxServer.io image)

Upstream install instructions, verbatim. Retrieved 2026-10-21.

Source: https://github.com/linuxserver/docker-qbittorrent (README.md, master)

## docker compose

```yaml
---
services:
  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=Etc/UTC
      - WEBUI_PORT=8080
      - TORRENTING_PORT=6881
    volumes:
      - /path/to/qbittorrent/appdata:/config
      - /path/to/downloads:/downloads #optional
    ports:
      - 8080:8080
      - 6881:6881
      - 6881:6881/udp
    stop_grace_period: "10s" #optional
    restart: unless-stopped
```

## docker run

```sh
docker run -d \
  --name=qbittorrent \
  -e PUID=1000 \
  -e PGID=1000 \
  -e TZ=Etc/UTC \
  -e WEBUI_PORT=8080 \
  -e TORRENTING_PORT=6881 \
  -p 8080:8080 \
  -p 6881:6881 \
  -p 6881:6881/udp \
  -v /path/to/qbittorrent/appdata:/config \
  -v /path/to/downloads:/downloads `#optional` \
  --stop-timeout="10s" `#optional` \
  --restart unless-stopped \
  lscr.io/linuxserver/qbittorrent:latest
```
