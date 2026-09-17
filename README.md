# tink

Tink is not Kubernetes - the companion tool for the Mini HCI / Tink
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
already needed before it got a name here - see each package's doc
comment for the working bash it's replacing.

| Command | Status | Replaces |
|---|---|---|
| `tink apply` | ported, not yet verified live | `incus-host/scripts/deploy.sh` |
| `tink ingress reconcile` / `tink ingress status` | ported, not yet cut over | `incus-host/reconciler/reconcile.sh` |
| `tink mongo snapshot` | not yet designed | (none yet - still under discussion) |

`apply` converges a host to its declared platform state - storage
volumes, profiles, the `ingress`/`authelia`/`incus-ui` instances, the
daemon's OIDC/authorization config. `ingress reconcile` discovers
instances that opt in via `user.ingress.{domain,port,enabled}` config and
converges the shared `ingress` instance's routes to match, without a
restart or a manual file push. `mongo snapshot` doesn't have a settled
design yet.

## Why a separate repo from `incus-host`

`incus-host` describes one specific host's configuration - its own
Caddyfile, its own Authelia config, its own secrets. Tink is meant to be
the tool that host, and any future one, runs - not tied to any single
host's specifics. `incus-host` is expected to become a *consumer* of
tink (cloning and building a pinned version as part of its own deploy
step) rather than containing tink's code directly.

## Architecture

```
cmd/tink/            thin CLI entrypoint (cobra) - argument parsing only
internal/incusapi/   shared Incus API client, used by every capability
internal/bootstrap/  tink apply
internal/ingress/    tink ingress ...
internal/backup/     tink mongo ... (undesigned)
```

Each capability is a plain importable package, not logic embedded in the
CLI layer - so a future interface (a web UI, an API server) can reuse the
same code the CLI calls, without duplicating it.

## Status

`ingress` is implemented: `tink ingress reconcile` and `tink ingress
status` use Incus's own Go client (`GetInstancesFull`, matching
`recursion=2`) instead of curl+jq, and `reconcile --dry-run` computes and
reports what would change without writing anything or reloading Caddy.
Not yet cut over on the live host - the plan stands as written: run it
side by side with `incus-host/reconciler/reconcile.sh` against
`incus.xlii.co`'s real state, diff the output, and only then move the
cron entry over. The bash script stays in place as rollback until that's
proven.

`apply` (capability zero) is implemented and verified end-to-end against
a real, freshly installed Incus 7.4 host -- not just dry-run, a real run
that stood up authelia/incus-ui/ingress with real secrets, a real
GHCR-published image, and real domains, confirmed by both instances
serving over HTTPS with real Let's Encrypt certs afterward. Same order as
`deploy.sh` (registries, storage volumes, profiles, incus-ui, authelia,
ingress, daemon config, reconciler cron), same behavior including
deploy.sh's own existing quirk of unconditionally deleting and
relaunching incus-ui/authelia/ingress on every run, not just the first.
Profiles, storage volumes, and instance existence/stop/delete go through
Incus's real Go client (verified against its actual interface
definitions); registries and instance launch still shell out, since
remotes have no daemon API to call instead and launch means resolving an
OCI image from a named remote. `--dry-run` computes and reports the full
action plan without touching the daemon, any instance, or the crontab.

Live testing found and fixed three real bugs unreachable from unit tests
or dry-run alone: a storage-volume existence check that never matched
(the client returns type-prefixed names like `custom/foo`), a file-push
race against a freshly launched container's readiness, and a restart
step that's a genuine logic gap, not just a race -- `incus restart`
requires an already-running instance, and an app container with no
config yet (true on every first boot) can crash to Stopped before
restart is even called. All three are fixed with tests; see the "Fix
three real bugs" commit for the full detail on each.

Confirmed idempotent: running `apply` twice in a row against the same
host, back to back, produced identical results both times with no
manual intervention needed on the second run.

## Building

```
go build -o tink ./cmd/tink
```
