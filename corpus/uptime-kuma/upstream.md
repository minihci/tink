# Uptime Kuma

Upstream install instructions, verbatim. Retrieved 2026-10-21.

Source: https://github.com/louislam/uptime-kuma (README.md and compose.yaml, master)

## docker run

```sh
docker run -d --restart=always -p 3001:3001 -v uptime-kuma:/app/data --name uptime-kuma louislam/uptime-kuma:2
```

## compose.yaml

```yaml
services:
  uptime-kuma:
    image: louislam/uptime-kuma:2
    restart: unless-stopped
    volumes:
      - ./data:/app/data
    ports:
      # <Host Port>:<Container Port>
      - "3001:3001"
```
