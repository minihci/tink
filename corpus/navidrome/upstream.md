# Navidrome

Upstream install instructions, verbatim. Retrieved 2026-10-21.

Source: https://github.com/navidrome/website (content/en/docs/installation/docker.md, master)

## docker compose

```yaml
services:
  navidrome:
    image: navidrome/navidrome:latest
    user: 1000:1000 # must own the data folder and be able to read music folder(s). See Permissions below
    ports:
      - "4533:4533"
    restart: unless-stopped
    environment:
      # Optional: put your config options customization here. Examples:
      # ND_LOGLEVEL: debug
    volumes:
      - "/path/to/data:/data"
      - "/path/to/your/music/folder:/music:ro"
```

## docker run

```sh
$ docker run -d \
   --name navidrome \
   --restart=unless-stopped \
   --user $(id -u):$(id -g) \
   -v /path/to/music:/music \
   -v /path/to/data:/data \
   -p 4533:4533 \
   -e ND_LOGLEVEL=info \
   navidrome/navidrome:latest
```
