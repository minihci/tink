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
| `-v name:/container/path` (repeatable) | `disk` device pointing at a managed storage volume | Distinguished from the bind-mount form by whether the source contains a `/` — a bare name is a managed volume, a path is a bind mount, matching Docker's own disambiguation rule |
| `IMAGE [CMD...]` (positional, after flags) | `oci.entrypoint` config key | Exactly what scoped `nextcloud-mcp` to `webdav`+`calendar` |
| `--network NAME` | NIC device's `network:` field | |
| `--name NAME` | the Incus instance name (positional in Incus, a flag in Docker) | **Required** — unlike Docker, `tink run` does not invent a random name when omitted; Incus instance names are meaningful and persistent on this platform (ingress registration, profiles), so an unnamed instance is a mistake to catch, not a default to paper over |
| `--restart=on-failure:5` | `boot.autorestart` (plain boolean) | **No clean map** — Incus has no retry-count concept. `tink run` accepts `--restart` as a boolean-ish flag (`always`/`unless-stopped` → `true`, `no` → `false`) and errors on a retry-count value rather than silently discarding it |

## Architecture

New package `internal/run`, following this repo's existing split (a
plain importable package, not logic embedded in `cmd/tink`):

- **`flags.go`** — pure parsing and mapping, no Incus dependency at all:
  Docker-flag strings in, an Incus `Spec` (image, instance name,
  `map[string]string` config, `[]api.Device`-shaped devices) out. This is
  the part worth the most test coverage, and the only part that's
  meaningfully testable without a live daemon.
- **`run.go`** — orchestration, using `internal/incusapi.Connect` like
  every other capability:
  1. `incus launch <image> <name>` (shells out — same justification
     `internal/bootstrap/instances.go` already documents: remotes and OCI
     image resolution are a client-config concept with no daemon API to
     call instead, so reimplementing that resolution here would be new,
     under-verified code standing in front of a live launch for no
     benefit).
  2. `server.UpdateInstance(name, ...)` via Incus's real Go client to set
     the translated `Config`/`Devices` — this part *is* modeled cleanly
     by the daemon API, unlike image resolution, so it doesn't need a
     second shell-out. This mirrors exactly the manual sequence used by
     hand today (launch bare, then a run of `config set`/`config device
     add` calls), just automated.
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
  --restart string      always|unless-stopped|no (boolean autorestart only)
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
- **`run_test.go`**: exercises the orchestration logic (launch call
  composed correctly, `UpdateInstance` called with the exact `Config`/
  `Devices` the parser produced, `--dry-run` performs neither) against a
  fake `incus.InstanceServer`, following the same seam
  `internal/bootstrap` already tests through.
- **Live verification is a separate, later step**, consistent with how
  every other capability here reached "verified live" status — not
  claimed here just because unit tests pass.

## Open questions, not blocking v1

- Docker Hub's bare-name-defaults-to-`library/`-namespace behavior
  (`docker run redis` → `library/redis`) — needs confirming whether
  Incus's `docker-oci:` remote resolves bare names the same way, or
  whether `tink run` needs to do that expansion itself before handing the
  image reference to `incus launch`.
- `--restart`'s accepted value set above is a first guess at the
  boolean-ish subset worth supporting; revisit once real usage surfaces
  which values actually get typed.
