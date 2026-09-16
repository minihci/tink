# tink

Tink is not Kubernetes — the companion tool for the Mini HCI / Tink
homelab platform: opinionated, Incus-native primitives for running
self-hosted projects, made executable instead of just documented.

## What this is

The Mini HCI / Tink platform is a set of opinionated defaults for running
self-hosted infrastructure on [Incus](https://linuxcontainers.org/incus/):
bare application containers (no Kubernetes, no Podman/systemd Quadlet),
one shared Caddy `ingress` instance as the platform's only public entry
point, Authelia for SSO, and a growing set of small automations that keep
those pieces converged without manual steps.

Those automations started as one-off bash scripts living in
[`incus-host`](https://github.com/xlii-co/incus-host) (the reference
deployment, currently running on `incus.xlii.co`). Tink exists to
consolidate them into one tool that any host or project adopting this
platform can run, instead of continuing to mint a new script per need.

## Capabilities

Each capability corresponds to something that was already real and
already needed before it got a name here — see each package's doc
comment for the working bash it's replacing.

| Command | Status | Replaces |
|---|---|---|
| `tink apply` | not yet ported | `incus-host/scripts/deploy.sh` |
| `tink ingress reconcile` / `tink ingress status` | not yet ported | `incus-host/reconciler/reconcile.sh` |
| `tink mongo snapshot` | not yet designed | (none yet — still under discussion) |

`apply` converges a host to its declared platform state — storage
volumes, profiles, the `ingress`/`authelia`/`incus-ui` instances, the
daemon's OIDC/authorization config. `ingress reconcile` discovers
instances that opt in via `user.ingress.{domain,port,enabled}` config and
converges the shared `ingress` instance's routes to match, without a
restart or a manual file push. `mongo snapshot` doesn't have a settled
design yet.

## Why a separate repo from `incus-host`

`incus-host` describes one specific host's configuration — its own
Caddyfile, its own Authelia config, its own secrets. Tink is meant to be
the tool that host, and any future one, runs — not tied to any single
host's specifics. `incus-host` is expected to become a *consumer* of
tink (cloning and building a pinned version as part of its own deploy
step) rather than containing tink's code directly.

## Architecture

```
cmd/tink/            thin CLI entrypoint (cobra) — argument parsing only
internal/incusapi/   shared Incus API client, used by every capability
internal/bootstrap/  tink apply
internal/ingress/    tink ingress ...
internal/backup/     tink mongo ... (undesigned)
```

Each capability is a plain importable package, not logic embedded in the
CLI layer — so a future interface (a web UI, an API server) can reuse the
same code the CLI calls, without duplicating it.

## Status

Skeleton only — no capability is implemented yet. `ingress` is next,
since it's simpler than `apply` (no secrets, no sequencing) and already
fully specified in `incus-host/reconciler/DESIGN.md`. The cutover plan
for the live reconciler: build a dry-run mode, run this alongside the
bash version against `incus.xlii.co`'s real state, diff the output, and
only then move the cron entry over — the bash script stays in place as
rollback until this is proven.

## Building

```
go build -o tink ./cmd/tink
```
