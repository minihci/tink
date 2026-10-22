# Forgejo

Upstream install instructions, verbatim. Retrieved 2026-10-21.

Source: https://codeberg.org/forgejo/docs (docs/admin/installation/docker.md, next); the database variants are shown there as diffs against this file

## docker compose, SQLite

```yaml
networks:
  forgejo:
    external: false

services:
  server:
    image: codeberg.org/forgejo/forgejo:16
    container_name: forgejo
    environment:
      - USER_UID=1000
      - USER_GID=1000
    restart: always
    networks:
      - forgejo
    volumes:
      - ./forgejo:/data
      - /etc/localtime:/etc/localtime:ro
    ports:
      - '3000:3000'
      - '222:22'
```
