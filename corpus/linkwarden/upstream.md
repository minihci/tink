# Linkwarden

Upstream install instructions, verbatim. Retrieved 2026-10-27.

Source: https://github.com/linkwarden/linkwarden (docker-compose.yml, last changed in commit e0cd593b5b; .env.sample; main, release v2.16.3)

## docker-compose.yml

```yaml
services:
  postgres:
    image: postgres:16-alpine
    env_file: .env
    restart: always
    volumes:
      - ./pgdata:/var/lib/postgresql/data
  linkwarden:
    env_file: .env
    environment:
      - DATABASE_URL=postgresql://postgres:${POSTGRES_PASSWORD}@postgres:5432/postgres
    restart: always
    image: ghcr.io/linkwarden/linkwarden:latest
    ports:
      - 3000:3000
    volumes:
      - ./data:/data/data
    depends_on:
      - postgres
      - meilisearch
  meilisearch:
    image: getmeili/meilisearch:v1.13.3
    restart: always
    env_file:
      - .env
    volumes:
      - ./meili_data:/meili_data
```

## .env.sample (the lines the compose file and the install page use)

The file is 489 lines: the four settings the install needs, then about 330 further settings, all empty (storage, single sign-on providers,
AI providers, email, proxies, limits), and the two search-engine lines `MEILI_HOST=` and `MEILI_MASTER_KEY=` among them. Only those lines are copied
here, as published; the compose file reads this file as `.env` (`env_file: .env`) and substitutes `${POSTGRES_PASSWORD}` from it.

```sh
NEXTAUTH_URL=http://localhost:3000/api/v1/auth
NEXTAUTH_SECRET=

# Manual installation database settings
# Example: DATABASE_URL=postgresql://user:password@localhost:5432/linkwarden
DATABASE_URL=

# Docker installation database settings
POSTGRES_PASSWORD=

MEILI_HOST=
MEILI_MASTER_KEY=
```
