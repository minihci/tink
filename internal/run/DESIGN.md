# tink run — design

Translates a `docker run`-shaped invocation onto real Incus primitives —
launch the image, then set the config keys and add the devices that
correspond to the flags given — instead of the manual "translate the
mental docker-run image+flags into `incus launch` plus a sequence of
`incus config set`/`incus config device add` calls" dance every tenant
app on this platform has been built with so far.

## Why now, not earlier

Named as a future `tink` capability on 2026-09-18 (user's own observation:
Podman adopted and extended Docker's CLI flag syntax for compatibility;
nothing equivalent exists for Incus — checked via real web/GitHub search,
confirmed absent, not assumed) and deliberately not built then. The
concrete flag mappings below were hand-verified during that same session
while standing up `nextcloud-app`/`nextcloud-db`/`nextcloud-redis`, but
the named trigger — "the next tenant app that re-derives this same
translation from scratch" — was left unfired on purpose, per this
platform's standing principle of not building ahead of a real, live need.

That trigger has now fired: nextcloud-incus was itself already the
second time this exact manual dance happened (Nightscout's stack was the
first), which is the trigger condition as originally written, not a new
one.

## Scope: `run` only, not a Docker CLI clone

Podman's win was cloning `docker run`'s flag surface specifically — `ps`,
`exec`, `logs`, `stop`, `rm` were comparatively easy for Podman because
dockerd's own object model already matched runc almost 1:1. Incus doesn't
share that problem to begin with: `incus list`/`incus exec`/`incus
stop`/`incus delete` are already just as short as their Docker
equivalents, so a `tink ps`/`tink exec` wrapper would add a translation
layer for no ergonomic gain over learning four `incus` verbs once.

The real, repeatedly-hit friction is narrower: turning a mental
`docker run IMAGE [flags]` into Incus's profile/device/config vocabulary,
which requires knowing device *types* (`proxy`, `disk`, `nic`) a Docker
user has no reason to already know. `tink run` is scoped to exactly that
translation, at instance-creation time only.

**Explicit non-goals, not deferred-but-implied**:
- `tink ps` / `exec` / `logs` / `stop` / `start` / `rm` / `inspect` —
  `incus`'s own verbs are already equally short.
- `tink build` / `push` / `pull` — image building and registry management
  are different problems entirely, out of scope.
- `tink network create` / `volume create` — native Incus network/storage
  management already exists and isn't Docker-flag-shaped in any way worth
  imitating.
- **`docker logs` parity** — not a scope decision, a real platform
  limitation. OCI application-container console output lands in
  `/var/log/incus/<instance>/console.log`, root-only, and rotates/resets;
  there's an open, unresolved upstream request for exactly this
  (`lxc/incus#2527`). No wrapper can paper over a capability Incus itself
  doesn't expose yet.

## Flag mapping

Every mapping below except the two verified-in-this-design-pass entries
was hand-exercised for real, repeatedly, building nextcloud-incus.

| Docker flag | Incus primitive | Notes |
|---|---|---|
| `-e KEY=VAL` (repeatable) | `environment.KEY=VAL` config key | |
| `-p HOST:CONTAINER` (repeatable) | `proxy` device: `listen=tcp:0.0.0.0:HOST connect=tcp:127.0.0.1:CONTAINER` | Syntax confirmed against current Incus docs during this design pass |
| `-v /host/path:/container/path` (repeatable) | `disk` device, `source=/host/path path=/container/path` | Bind-mount form |
| `-v name:/container/path` (repeatable) | `disk` device pointing at a managed storage volume, in the pool named by `--pool` (default `default`) | Distinguished from the bind-mount form by whether the source contains a `/` — a bare name is a managed volume, a path is a bind mount, matching Docker's own disambiguation rule. **Docker/Incus semantic gap, found live**: Docker auto-creates a named volume on first use; Incus's own managed volumes don't — attaching a disk device to one that's never been created fails validation outright. `tink run` creates the volume first if it's missing, matching Docker's ergonomics rather than Incus's stricter default (confirmed against a real, disposable test host, 2026-09-18) |
| `IMAGE [CMD...]` (positional, after flags) | `oci.entrypoint` config key | Exactly what scoped `nextcloud-mcp` to `webdav`+`calendar` |
| `--network NAME` | NIC device's `network:` field | |
| `--ip ADDR` (requires `--network`) | NIC device's `ipv4.address:` field | Found missing while recreating a real known deployment (nextcloud-incus): every one of its five profiles pins a static address on `incusbr0`, which `--network` alone can't express. Errors if given without `--network` |
| `--name NAME` | the Incus instance name (positional in Incus, a flag in Docker) | **Required** — unlike Docker, `tink run` does not invent a random name when omitted; Incus instance names are meaningful and persistent on this platform (ingress registration, profiles), so an unnamed instance is a mistake to catch, not a default to paper over |
| `--restart=on-failure:5` | `boot.autorestart` (plain boolean) | **No clean map** — Incus has no retry-count concept. `tink run` accepts `--restart` as a boolean-ish flag (`always`/`unless-stopped` → `true`, `no` → `false`) and errors on a retry-count value rather than silently discarding it |
| `--rm` | `ephemeral: true` on the instance (`incus init/launch -e, --ephemeral`) | A real Docker flag with a real Incus analog, unlike `-it`/`-d` (see below). **Confirmed live which one wins when combined with `--restart`**: an ephemeral instance with `boot.autorestart: true`, stopped, is deleted rather than restarted — `ephemeral` wins, matching Docker's own `--rm` removing the container on any stop, not just a clean exit |

## Docker flags with no `tink run` analog

Checked rather than assumed, since these look like they should map:

- **`-d`/`--detach`** — not a gap, a non-issue: Incus instances are always
  created running in the background by the daemon. There's no "foreground,
  attached to my terminal" creation mode to opt out of the way plain
  `docker run` has by default, so there's nothing for a flag to do here.
- **`-it`** — architectural, not a missing flag. Docker's `-it` can make
  the container's own PID 1 be your terminal session directly. Incus
  splits creation from interaction: `tink run` creates and starts the
  instance, and a separate `incus exec -it <name> -- <shell>` attaches an
  interactive session to it afterward. That's the same category as
  `ps`/`exec`/`logs` already being non-goals above, not something
  `tink run` itself should grow a flag for.

## Architecture

New package `internal/run`, following this repo's existing split (a
plain importable package, not logic embedded in `cmd/tink`):

- **`flags.go`** — pure parsing and mapping, no Incus dependency at all:
  Docker-flag strings in, an Incus `Spec` (image, instance name,
  `map[string]string` config, `[]api.Device`-shaped devices) out. This is
  the part worth the most test coverage, and the only part that's
  meaningfully testable without a live daemon.
- **`run.go`** — orchestration, using `internal/incusapi.Connect` like
  every other capability. **Create, configure, then start once** —
  revised from an earlier launch-then-configure-then-restart design after
  live testing against a second real reference deployment
  (nextcloud-incus) showed it was wrong for a whole class of images, not
  just slow:
  1. `incus init <image> <name>` — creates the instance **without
     starting it**, unlike `incus launch`. This is not a style choice:
     nextcloud-app's and nextcloud-db's own profile comments already
     prescribe exactly this by hand, because Postgres's and Nextcloud's
     images each run a one-shot, config-gated action on first boot
     (Postgres's init scripts need `POSTGRES_PASSWORD` already present;
     Nextcloud's installer needs its DB credentials already present) —
     launching bare and configuring afterward either misses that moment
     entirely or crashes the instance before config can be applied at
     all. The remote/image-resolution shell-out itself is still
     unavoidable for the same reason `internal/bootstrap/instances.go`
     already documents: resolving `docker-oci:redis:7` means talking to
     that named remote as an ImageServer, a client-config concept with no
     daemon API to call instead.
  2. Create any managed storage volume a `-v name:path` device references
     that doesn't already exist yet (see the flag table above — a real
     gap found live, not anticipated in the original design).
  3. `server.UpdateInstance(name, ...)` via Incus's real Go client to set
     the translated `Config`/`Devices` — this part *is* modeled cleanly
     by the daemon API, unlike image resolution, so it doesn't need a
     second shell-out. **Applied atomically by Incus itself**: confirmed
     live that one invalid device (the missing-volume case above, before
     that fix) silently drops every other translated config/device change
     too — a real reason this step has to fully succeed before anything
     is ever started, not just a nicety.
  4. Start the instance — **always**, regardless of whether any `Config`
     was set, since `incus init` never starts it itself. This is always a
     genuine first start, never a restart: the instance is never running
     before its config is already in place, which is exactly what the
     Postgres/Nextcloud case above needs, and incidentally also fixes a
     latent gap in the original design (a devices-only run with no
     `Config` never got a restart either, which was never actually
     verified to be safe for device changes generally). The original
     design's justification for *why* a restart was needed at all still
     stands as the reasoning for why an initial start is required here —
     environment variables and `oci.entrypoint` are process-launch
     parameters, confirmed live that `UpdateInstance` alone leaves them
     inert with no process ever having read them — it just no longer
     needs to be conditional. Mirrors the same problem
     `internal/bootstrap`'s `applyIngress`/`applyAuthelia` already solved
     (`ensureRunning`/`restartAction`) for the same reason; `run.go`
     reimplements the same small start-vs-restart-by-current-status logic
     rather than importing it, since `bootstrap`'s version is tied to its
     own `*runner`/dry-run type.
  `--dry-run` computes and prints the full action plan without touching
  the daemon, matching `tink deploy`'s and `tink ingress reconcile`'s
  existing convention — this repo has one dry-run idiom, not a new one
  per capability.

## CLI shape

```
tink run [flags] IMAGE [CMD...]

  --name string        instance name (required)
  -e, --env KEY=VAL     set an environment variable (repeatable)
  -p, --publish HOST:CONTAINER   publish a port via a proxy device (repeatable)
  -v, --volume SRC:DST  bind-mount a host path or attach a managed volume (repeatable)
  --network string      NIC device's network
  --ip string            static ipv4.address on the NIC device (requires --network)
  --project string       Incus project to create the instance in (default: the daemon's own default project)
  --restart string      always|unless-stopped|no (boolean autorestart only)
  --rm                    delete the instance automatically once it stops, for any reason
  --profile string       an existing Incus profile to layer in addition (repeatable)
  --dry-run              compute and print the plan without applying it
```

Flags are parsed before the positional `IMAGE [CMD...]`, matching how
every real `docker run` invocation on this platform has actually been
written so far — interspersed flags after the image are out of scope.

## Testing plan

- **`flags_test.go`**: table-driven, one case per row in the mapping
  table above plus the ambiguous cases (bind-mount vs. managed-volume
  `-v`, boolean-vs-retry-count `--restart`) — no Incus dependency, runs
  everywhere `go test` does.
- **`run_test.go`**: exercises `Run`'s `--dry-run` path directly (it never
  calls `incusapi.Connect` or shells out, so it needs no live daemon) —
  matching this repo's own established convention of unit-testing pure
  logic only (see `internal/bootstrap`'s tests) and leaving real daemon
  interaction to live verification, not a hand-built fake
  `incus.InstanceServer`.
- **Live verification, done (2026-09-18, against a real, disposable test
  host)**: see "Verified live" below — this is what actually happened,
  not a plan for later.

## Verified live (2026-09-18, a real, disposable test host)

Built for `linux/amd64`, copied to a separate path (`/root/tink-run-test`,
not the live `/usr/local/bin/tink` `tink-daemon.service` runs) so as not
to disturb the host's real, already-running `authelia`/`incus-ui`/
`ingress` and the full live `nextcloud-*` rehearsal stack. All testing
used a separately-named scratch instance, deleted afterward along with
its managed volume — host confirmed back to its exact prior state
(`authelia`/`incus-ui`/`ingress` only) when done.

Exercised together in one instance (`docker-oci:caddy:2.11.4`): `-e`,
`-p`, both `-v` forms, `--restart=always`, and (in a second instance)
a `CMD` override. Confirmed correct: `boot.autorestart`/
`environment.*` set correctly; the published port genuinely reachable
(`curl` through the `proxy` device, HTTP 200); the bind-mounted host file
visible inside the container; the managed volume attached, writable, and
genuinely used by the running process (Caddy's own `XDG_DATA_HOME=/data`
wrote real data there); the `CMD` override genuinely replaced the image's
own entrypoint (confirmed via the actual command's real stdout in the
console log, not just the config key being set) and the instance exited
cleanly afterward, matching Docker's own behavior for a one-shot command.

**Two real bugs found this way, both fixed** (see the flag table and
Architecture section above for each): the missing-managed-volume
validation failure that silently dropped every other config/device change
along with it, and the missing post-config restart that left
already-applied `environment.*`/`oci.entrypoint` changes inert on an
already-running instance. Neither was reachable from `flags_test.go`
alone — both needed a real daemon to surface.

## Open questions, not blocking v1

- Docker Hub's bare-name-defaults-to-`library/`-namespace behavior
  (`docker run redis` → `library/redis`) — needs confirming whether
  Incus's `docker-oci:` remote resolves bare names the same way, or
  whether `tink run` needs to do that expansion itself before handing the
  image reference to `incus launch`.
- `--restart`'s accepted value set above is a first guess at the
  boolean-ish subset worth supporting; revisit once real usage surfaces
  which values actually get typed.
