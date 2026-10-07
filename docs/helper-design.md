# The tink helper: design

**Status: design only. Nothing here is built.** It records the direction agreed so far, the reasoning, and what is still open, so
the work can be phased and challenged before code is written. Claims are marked **[code]** (read in this repo), **[verified]**
(tried live), **[docs]** (upstream documentation) or **[hypothesis]** (believed, to be checked in a spike).

## What this is

A long-running **tink helper container**: an Incus instance, run from an image built from this repository, that does the work tink
should do on a schedule or near the data. It replaces `tink daemon install`. The first jobs are the backup scheduler
(`tink backup run --due`) and the ingress reconcile loop that `tink daemon run` already runs. It is a generic home for jobs defined
now or discovered later, not a backup feature.

The same direction makes tink independent of the host it manages: with the helper doing the host-local, scheduled work, the `tink`
CLI only needs the Incus API, so it can run from anywhere that can reach it, for example a laptop against the Incus server on Tron.

## Decisions already made

| Decision | Reason |
|---|---|
| The helper replaces `tink daemon install`. | One supervision mechanism (Incus starts instances) instead of a unit generator per init system plus cron. |
| The helper's image, Containerfile and build pipeline live **in this repository**. | It is tightly bound to tink by design; keeping them together keeps the version in step. It is a small amount of code and should not pull the repo off its direction. |
| A laptop `tink backup run` **triggers the helper**; it does not run the copy itself. | Backup data is relayed through whichever process runs the copy. From a laptop that would route a volume through the laptop. Heavy work belongs next to the data. |
| Credentials are **kept simple first**: the helper has full admin access to the Incus it manages. Hardening comes after the mechanism works. | One thing at a time. The hardening path is noted below. |

## Why: tink's host couplings today

Almost every command opens Incus through one function, `incusapi.Connect(socket)`, with the default socket
`/var/lib/incus/unix.socket` (about a dozen call sites). **[code]** Beyond that, the places tink still depends on the machine it runs on:

| Coupling | Where | Fate |
|---|---|---|
| `crontab` edits; writing `/etc/systemd/system/tink-daemon.service` and `/etc/init.d/tink-daemon` | `internal/bootstrap/reconciler_daemon.go`, `internal/daemon/units.go` (systemd and OpenRC only) | Goes away with the helper. |
| Shells out to the `incus` CLI | `internal/bootstrap/instances.go`, `internal/resolve/plan.go` (`r.Check`), `internal/resolve/apply.go` (`r.Command`) | Replace with the API; `incusapi.ExecInGuest` already exists. |
| `skopeo` for image-drift checks (optional, falls back when absent) | `internal/resolve/imagedrift.go` | Could be in the helper image; not required. |
| Ingress reads and writes a **path inside a storage pool on the host**: `/var/lib/incus/storage-pools/default/custom/default_ingress-routes/generated` | `internal/ingress/ingress.go` | The hard one. A remote tink or a containerised loop cannot reach that path; it must go through an instance that mounts the volume. Must be solved before the ingress job moves. |
| `tink deploy` provisioning (registries, profiles, instances, the ingress, the reconciler cron) | `internal/bootstrap/*` | Host provisioning. Stays host-local, or becomes its own thing; out of scope here. |
| Secrets identity in the user's `~/.config/tink` | `internal/secrets` | Per operator. The helper only needs it for a job that resolves `${secret:}`; backups do not today. |
| `buildVersion` reads Go's embedded VCS metadata, with a comment that a release script does not exist yet | `cmd/tink/main.go` | An image built without `.git` reports no version, so the pipeline must inject one. |

## The pieces

### 1. Connecting by remote (`--remote`)

One connect function, used by every command, that takes a remote name from the Incus client configuration (the file the `incus`
CLI uses, via `cliconfig`, as remote backup targets already do **[code]**) and falls back to the local socket. `--remote` /
`TINK_REMOTE`, defaulting to the Incus client's own default remote. Replacing the `incus` CLI shell-outs with API calls is part of this:
a laptop has no `incus` binary pointed at the right server.

Not everything can work from a laptop (anything that touches a host path), and the commands that cannot should say so, not fail
obscurely.

### 2. The helper instance

- **Image**: built from a `Containerfile` in this repository: the static tink binary (`CGO_ENABLED=0`), CA certificates and
  tzdata (schedules are evaluated in the instance's time zone), nothing else at first. Published to `ghcr.io` by a workflow in
  `.github/workflows/`, multi-arch, tagged with the tink version. Incus runs OCI images as app containers; this repository's own
  `verify` runs `docker-oci:` images that way. **[verified]**
- **Entry point**: `tink daemon run`, extended with the jobs below.
- **Reaching Incus**: a **proxy device** with `bind=container` that connects the container to the host's
  `/var/lib/incus/unix.socket`. **[hypothesis]** A plain bind of the socket file would be root-owned and unreadable from an
  unprivileged container; the proxy device runs on the host and creates the socket inside the container. Verify in a spike,
  including a restart. This is the "simple first" credential: it is host-root-equivalent, which is stated in the docs, not hidden.
- **Config**: a small volume holding the Incus client configuration (needed for remote backup targets: their certificates), mounted at the
  path the Incus client library reads. A second volume holds the synced stacks. Neither holds anything irreplaceable except the
  remote certificates, which can be re-issued.
- **Supervision**: `boot.autostart=true`, and `boot.autorestart` (present in Incus 7.4's instance driver **[code]**; exact semantics to confirm).
- **Identity**: found by a config key, `user.tink.helper=<version>`, not by name, so the CLI can discover it in a project.

### 3. The loop

`tink daemon run` gains jobs, each on its own interval, none able to stop the loop (the existing rule: a failed pass logs and keeps
going **[code]**):

- **backup**: every tick (default 1 minute), for each stack in the stacks volume, run what `tink backup run --due` would. The
  due logic already exists and catches up a missed run **once** (it is "next run after the last success has passed", not a count of
  missed runs). **[code]**
- **ingress**: the existing reconcile, once the host-path coupling is solved.

**Only one thing runs at a time inside the helper**, enforced with a file lock in the container, taken by the loop and by a triggered run.
This is what makes a manual trigger and the scheduler safe together, and why a stamp-based claim is not needed. There must be only one
helper per Incus server; `install` refuses a second.

Stacks are re-read every tick, so syncing a stack takes effect without a restart. Logs go to stdout (`incus console --show-log`).

### 4. The laptop trigger

`tink backup run [VOLUME...]` from a machine that is not the helper:

1. find the helper (by `user.tink.helper`) on the connected server;
2. **sync** the stack files given with `-f` into the helper's stacks volume through the instance file API;
3. **exec** `tink backup run ...` in the helper through the Incus API, streaming its output and exit code back.

No daemon API of its own is added: tink has deliberately had none, because the daemon owns no state a one-shot invocation cannot see **[code]**
(`internal/daemon` package doc), and Incus is already the transport and the access control. `--local` runs it in-process instead, with a
warning when a relayed remote copy would pass through the machine. With no helper installed, the command runs locally as it does today.

`restore` and `verify` do not need the helper: they copy within one server, which Incus does server-side.

### 5. Lifecycle commands

`tink helper install | upgrade | status | remove`. `install` creates the instance and volumes (idempotently, converging); `upgrade` moves the
helper to the CLI's own version; `status` shows the version, uptime, last tick and the last result per copy (from the volume stamps,
so it is truthful even when the log is gone). **Version rule:** the CLI manages a helper of exactly its own version and says so when they differ, instead
of silently driving an older helper. Whether the helper should also be declarable as a resource in the stack is open (below).

## Replacing `tink daemon install`

`tink daemon run` stays: it is the helper's entry point. `daemon install` is deprecated once the helper can do the ingress job, kept for
one release with a notice, and then removed. Existing hosts migrate by `tink helper install`, then removing the unit and the cron entry.

## Security: now and later

**Now:** the helper holds the host's Incus admin socket, so a compromise of the helper is a compromise of the host. That is no worse than
`tink daemon` running as root on the host today, and it is stated plainly. The client-config volume holds the certificates for remote targets.

**Later:** replace the socket with a client certificate over the Incus API, scoped to the projects the helper manages. Caveat found while
building remote backups: an Incus *restricted* certificate is only bounded if the server's authorization lets Incus's own check run. A
server that routes `authorization.client.tls-restricted` through a scriptlet returning `True` gives it everything. **[verified]**
The hardening step therefore includes checking what the certificate can actually see.

## Phasing

Each phase is useful on its own and ends in something checkable on the lab host.

1. **`--remote` and no shell-outs.** One connect function; the three `incus` CLI shell-outs become API calls. *Done when:* `plan`,
   `apply` and `backup restore` work from a laptop against Tron, and the code has no `exec.Command("incus", ...)`.
2. **The backup job in the loop.** `daemon run` takes a stacks path and runs the due backup copies with a lock. *Done when:* a loop
   run under any supervisor copies a volume on its schedule, catches up once after being down, and two runs never overlap.
3. **The helper.** Containerfile, the image workflow, `tink helper install|upgrade|status|remove`. *Done when:* killing the helper's
   process, and rebooting the host, both bring it back and the next due copy still runs.
4. **The laptop trigger.** Sync, then exec in the helper. *Done when:* `tink backup run` from a laptop runs the copy inside the helper
   and no volume data passes through the laptop.
5. **Ingress path, then retire `daemon install`.** The ingress job stops reading a host path; `daemon install` is deprecated.

## Open questions

- **Declarative or imperative?** `tink helper install` is imperative. A `kind: helper` resource would let the stack declare it, but the
  stack would need the helper to exist before it can be applied from anywhere else. Recommendation: start imperative; revisit.
- **Which stacks does the helper schedule?** Every stack synced to it? Syncing happens on a trigger today; should `apply` sync too, so the
  scheduler never lags the repository?
- **Where does the image come from before it is published?** A local build and `incus image import`, for the lab.
- **Unprivileged socket access** (the proxy device above) and **`boot.autorestart` semantics** are hypotheses for the first spike.
- **Alerting.** A failed scheduled copy only shows up in the log and as an "overdue" plan warning. A notification hook is not designed.
- **The ingress host path** (see above): the shape of the fix is not chosen.
- **Time zones.** Cron schedules use the container's time zone; the helper should be explicit about it and `status` should show it.

## Alternatives considered

- **Keep `daemon install` (init-system units).** Works, but needs a generator per init system, and ties tink to the host it runs on.
- **cron or a timer calling `tink backup run --due`.** Simplest, and `--due` was designed for it; it remains a supported way to run the same
  command. It does not give the helper's other benefits.
- **A tink daemon with its own API** (a Docker-style client and server). Rejected, as before: tink owns no state, and Incus already provides
  the transport and the access control.
- **The helper in its own repository.** Rejected: it is tightly bound to tink, and a separate repository would mean keeping two versions in step.
