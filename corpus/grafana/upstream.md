# Grafana

Upstream install instructions, verbatim. Retrieved 2026-10-21.

Source: https://github.com/grafana/grafana (docs/sources/setup-grafana/installation/docker/index.md, main)

## docker run, with a persistent volume

```sh
   # start grafana
   docker run -d -p 3000:3000 --name=grafana \
     --volume grafana-storage:/var/lib/grafana \
     grafana/grafana-enterprise
```

## docker compose, with a persistent volume

```yaml
   services:
     grafana:
       image: grafana/grafana-enterprise
       container_name: grafana
       restart: unless-stopped
       ports:
         - '3000:3000'
       volumes:
         - grafana-storage:/var/lib/grafana
   volumes:
     grafana-storage: {}
```
