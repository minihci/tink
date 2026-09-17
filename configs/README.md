# configs/

`tink deploy`'s config templates — moved here from
[`incus-host`](https://github.com/xlii-co/incus-host) (2026-09-18) so a
host only needs one checkout (this repo's) to run `deploy`, not two.
`--repo-root` defaults to this directory; `deploy.env` and the real
secrets below are resolved relative to whatever `--repo-root` points at.

Same split as `incus-host` always had: files here are templates and
`deploy` fills in `${VAR}` placeholders from `deploy.env` before pushing
them — see `internal/bootstrap/render.go`. This directory also mixes in
host-owned, gitignored files at deploy time (`deploy.env`, `secrets/`,
`authelia/users_database.yml`) — those never leave whatever host they're
generated on, same as before.

| path | purpose |
|---|---|
| `deploy.env.example` | per-host values — copy to `deploy.env` (or wherever `--deploy-env` points), fill in, never commit |
| `daemon/server-config.yaml` | Incus server-config template (OIDC, authorization, trusted proxy) |
| `daemon/authorization.star` | the whole authorization policy — spliced into `server-config.yaml` at deploy time; see its own header comment |
| `incus-ui/incus-ui.profile.yaml` | Incus profile template for the `incus-ui` OCI application container ([`minihci/incus-ui`](https://github.com/minihci/incus-ui) builds the image itself) |
| `authelia/configuration.yml` | Authelia config — safe to commit as-is, see the comment at its top for how secrets and per-host domains get resolved without ever being written here |
| `authelia/authelia.profile.yaml` | Incus profile template for Authelia, official upstream image |
| `authelia/users_database.yml.example` | shape only; the real file has a real password hash and isn't committed |
| `ingress/ingress.profile.yaml` | Incus profile template for the shared `ingress` Caddy instance |
| `ingress/Caddyfile` | the shared public edge — owns `:80`/`:443`, no domain logic of its own, just `import routes/*.caddy` |
| `ingress/routes/*.caddy` | one file per public domain this host hand-maintains (`incus-ui.caddy`/`auth.caddy`) — a project like `nightscout-podman` registers its own domain at runtime instead, via `user.ingress.*` config; see `internal/ingress`'s package doc |

**What didn't move here**: `scripts/generate-authelia-secrets.sh` (one-time
per host, mints every secret via Authelia's own CLI) and the reconciler's
reference bash implementation (`reconciler/{reconcile.sh,DESIGN.md}`) are
staying in `incus-host` for now — see that repo's own README for why.
