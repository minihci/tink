# Syncthing

Upstream install instructions, verbatim. Retrieved 2026-10-21.

Source: https://github.com/syncthing/syncthing (README-Docker.md, main)

## docker run

```sh
$ docker run --network=host  -e STGUIADDRESS= \
    -v /wherever/st-sync:/var/syncthing \
    syncthing/syncthing:latest
```

## docker compose

```yaml
---
version: "3"
services:
  syncthing:
    image: syncthing/syncthing
    container_name: syncthing
    hostname: my-syncthing
    environment:
      - PUID=1000
      - PGID=1000
      - STGUIADDRESS=
    volumes:
      - /wherever/st-sync:/var/syncthing
    network_mode: host
    restart: unless-stopped
    healthcheck:
      test: curl -fkLsS -m 2 127.0.0.1:8384/rest/noauth/health | grep -o --color=never OK || exit 1
      interval: 1m
      timeout: 10s
      retries: 3
```
