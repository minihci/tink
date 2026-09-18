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
| `tink deploy` | verified live | `incus-host/scripts/deploy.sh` |
| `tink run` | implemented, not yet verified live | (new - the manual per-tenant "mental docker-run into `incus launch` plus a sequence of `incus config`/`incus config device add` calls" dance) |
| `tink ingress reconcile` / `tink ingress status` | live on `incus.xlii.co` | `incus-host/reconciler/reconcile.sh` |
| `tink daemon run` / `tink daemon install` | implemented | (new - cron is still how `ingress reconcile` actually runs today) |
| `tink mongo snapshot` | not yet designed | (none yet - still under discussion) |

`deploy` converges a host to its declared platform state - storage
volumes, profiles, the `ingress`/`authelia`/`incus-ui` instances, the
daemon's OIDC/authorization config. `run` translates a docker-run-shaped
invocation onto real Incus primitives - see
[`internal/run/DESIGN.md`](internal/run/DESIGN.md) for the flag-mapping
table and its deliberate non-goals (not a Docker CLI clone). `ingress
reconcile` discovers instances that opt in via
`user.ingress.{domain,port,enabled}` config and converges the shared
`ingress` instance's routes to match, without a restart or a manual file
push. `daemon run` runs that same reconcile loop as a persistent process
instead of a cron-invoked one-shot; `daemon install` prints (doesn't
apply) the systemd unit or OpenRC init script needed to supervise it, for
whichever init system the host actually runs. `mongo snapshot` doesn't
have a settled design yet.

## Relationship to `incus-host`

Tink started as a separate repo from
[`incus-host`](https://github.com/xlii-co/incus-host) (the reference
deployment, currently running on `incus.xlii.co`) on the assumption that
`incus-host` would become a *consumer* of tink - cloning and building a
pinned version as part of its own deploy step. That assumption inverted
once `tink deploy` actually existed: `incus-host`'s real ongoing job
turned out to be "carry `deploy`'s config templates" (its Caddyfile, its
Authelia config, its Incus profile YAMLs), not "run a script that calls
tink." A tool and the templates it renders don't need to be two repos
just because they started in two places, so those templates moved into
this repo's own [`configs/`](configs/README.md) (2026-09-18) -
`--repo-root` defaults there now. A host running `tink deploy` needs one
checkout (this one), not two. `incus-host` keeps the reconciler's
reference bash implementation and design doc, kept for history rather
than moved, since neither is what's actually executing anywhere anymore.

## Architecture

```
cmd/tink/            thin CLI entrypoint (cobra) - argument parsing only
internal/incusapi/   shared Incus API client, used by every capability
internal/bootstrap/  tink deploy
internal/run/        tink run - see internal/run/DESIGN.md
internal/ingress/    tink ingress ...
internal/daemon/     tink daemon ...
internal/backup/     tink mongo ... (undesigned)
configs/             tink deploy's config templates - see configs/README.md
```

One binary, not two: `tink daemon run`/`tink daemon install` are
subcommands of the same `tink` binary, not a separate `tinkd` daemon
binary or a client/server split. Considered both and rejected them -- see
`internal/daemon`'s own package doc for why (short version: a
symlink/argv[0] dispatch trick is implicit "magic," and a
thin-client-talks-to-a-daemon-API design only earns its complexity when
the daemon owns state a one-shot invocation can't otherwise see, which
tink doesn't have -- Incus's own daemon already owns everything tink
cares about).

Each capability is a plain importable package, not logic embedded in the
CLI layer - so a future interface (a web UI, an API server) can reuse the
same code the CLI calls, without duplicating it.

## Status

`ingress` is implemented and **cut over live on `incus.xlii.co`**: `tink
ingress reconcile` and `tink ingress status` use Incus's own Go client
(`GetInstancesFull`, matching `recursion=2`) instead of curl+jq. The
cutover followed the plan as written -- ran `tink daemon run` side by
side with the still-cron-invoked `incus-host/reconciler/reconcile.sh` for
two full passes against real production data (the live `ns-caddy`
registration, not a scratch instance), confirmed byte-identical output
and zero unnecessary writes (route file mtime unchanged across both
passes), then removed the cron entry. `ns.xlii.co`/`incus.xlii.co`/
`auth.xlii.co` all confirmed healthy throughout and afterward.

`deploy` (capability zero) is implemented and verified end-to-end against
a real, freshly installed Incus 7.4 host -- not just dry-run, a real run
that stood up authelia/incus-ui/ingress with real secrets, a real
GHCR-published image, and real domains, confirmed by both instances
serving over HTTPS with real Let's Encrypt certs afterward. Same order as
`deploy.sh` (registries, storage volumes, profiles, incus-ui, authelia,
ingress, daemon config), same behavior including deploy.sh's own existing
quirk of unconditionally deleting and relaunching incus-ui/authelia/ingress
on every run, not just the first. One deliberate improvement over
deploy.sh, not just a port: the last step installs and enables `tink
daemon run` under the host's real init system instead of installing the
old cron entry, removing any leftover legacy cron entry from an older run
first.
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

Confirmed idempotent: running `deploy` twice in a row against the same
host, back to back, produced identical results both times with no
manual intervention needed on the second run.

`daemon` is implemented, smoke-tested on a disposable VPS, and **now the
live mechanism running the reconciler on `incus.xlii.co`** -- supervised
by a real, `systemd-analyze verify`-passed unit (`systemctl enable --now
tink-daemon`), not cron. `daemon run`'s loop mechanics (immediate first
pass, ticks at the given interval, survives a failed pass without dying,
exits cleanly on SIGTERM/SIGINT) are unit-tested with an injected
reconcile function; `daemon install` auto-detects the init system,
failing honestly rather than guessing wrong when neither systemd nor
OpenRC is found. `deploy` (`tink` and `deploy.sh` both) now installs and
enables this instead of the old cron entry on every run, so a future
re-deploy can't silently reinstate cron underneath it.

## Building

```
go build -o tink ./cmd/tink
```
