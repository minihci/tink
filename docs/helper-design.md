# The tink helper: design

**Status: phases 0 to 2 are built, validated on the lab host, and merged** (#15, #17 and #18 to #22) (the [phase 0](#phase-0-findings) and [phase 2](#phase-2-findings) findings
changed the design below); phase 1 is merged. The container itself (phase 3), the laptop trigger (phase 4) and retiring `daemon install` (phase 5) are design only. This is the second revision: the first was reviewed adversarially against the code and
the Incus 7.4 source by a separate agent, and the design below was changed to answer what that found
([what changed](#what-the-review-changed)). Claims are marked **[code]** (read in this repo or in the Incus source; the three that
most changed the design were re-checked by hand), **[verified]** (tried live), **[docs]** or **[hypothesis]** (to be checked in
the phase 0 spike). Choices not yet confirmed by the user are marked **proposed**.

## What this is

A long-running **tink helper**: an Incus instance, run from an image built from this repository, that does the work tink should do on a
schedule or near the data. It replaces `tink daemon install`. The first jobs are backup copies on their schedule and the ingress
reconcile loop that `tink daemon run` already runs. It is a generic home for jobs defined now or discovered later.

The same direction makes the `tink` CLI independent of the host it manages: the host-local, scheduled work moves into the helper,
so the CLI needs only the Incus API and can run from anywhere that can reach it, such as a laptop against the server on Tron.

## Decisions already made (by the user)

| Decision | Reason |
|---|---|
| The helper replaces `tink daemon install`. | One supervision mechanism (Incus starts instances) instead of a unit generator per init system. |
| The helper's image, Containerfile and pipeline live **in this repository**. | Tightly bound to tink; keeps the version in step; small enough not to pull the repo off its direction. |
| A laptop `tink backup run` **triggers the helper** and does not run the copy itself. | Relayed backup data passes through whichever process runs the copy. Heavy work belongs next to the data. |
| Credentials are **simple first**: the helper has full admin access to the Incus it manages. Hardening comes after the mechanism works. | One thing at a time. |

## Principles the review made explicit

1. **A run must outlive whoever started it.** An Incus `exec` is killed when its websocket drops (see [the trigger](#4-triggering-from-a-laptop)).
   Anything long-running therefore cannot be an exec held open by a laptop.
2. **The helper is disposable, not stateless.** It owns three things: synced copies of stacks, a job directory, and a heartbeat. All are
   reconstructible (stacks from their repositories, jobs are history, the heartbeat is rewritten every tick). Backup state itself stays on
   the volumes as stamps and markers, never in the helper.
3. **A run is isolated from other runs.** One bad stack, one slow copy or one failing target must not stop anything else.
4. **Failures leave a mark.** Success stamps alone make a failing job look merely "not yet run", and a scheduler that retries it forever.
5. **The helper never executes stack-supplied commands itself.** The stack is data it reads, not code it runs (see [security](#security)).

## tink's host couplings today

Almost every command opens Incus through one function, `incusapi.Connect(socket)`, 12 call sites, default socket
`/var/lib/incus/unix.socket`; `Connect("")` honours `$INCUS_SOCKET` and `$INCUS_DIR`. **[code]** What else ties tink to the machine:

| Coupling | Where | Fate |
|---|---|---|
| `kind: incus` resources run **user-written argv for the `incus` CLI** (`check:` and `command:`, for example `incus image import /path/to.qcow2`), against the operator's default remote. Not replaceable by an API call: it is an escape hatch with local paths. | `internal/resolve/resource.go`, `plan.go:202`, `apply.go:64` | Refused (BLOCKED, with the reason) when tink is pointed at a remote with `--remote`; unchanged locally. |
| `tink deploy`: launches instances and pushes files and edits daemon config through `incus`, adds registries with `incus remote add`, **and installs the host daemon** (the "reconciler daemon" step writes the systemd unit and enables it). | `internal/bootstrap/*` (`instances.go:63,78,101`, `daemon.go:16`, `registries.go`, `reconciler_daemon.go:94`) | Host provisioning: stays host-local by design. Once the helper exists, `deploy` must **not** reinstall the host daemon (it would bring back a second ingress reconciler). |
| `tink daemon install` only **prints** a unit; the crontab code is legacy cleanup of an old `reconcile.sh`. | `main.go:526`, `reconciler_daemon.go:38` | Deprecated, then removed (see phase 5). |
| Ingress reads and writes a **host path inside a storage pool**: `/var/lib/incus/storage-pools/default/custom/default_ingress-routes/generated`, with the pool name `default` fixed. | `internal/ingress/ingress.go:29` | Fixed in phase 3 by giving the helper the `ingress-routes` volume as a disk device and making the directory and pool configurable. |
| `skopeo` for image-drift checks (optional), with `/opt/incus/bin` hard-coded. | `internal/resolve/imagedrift.go:434,494-499` | In the helper image if needed; the path becomes a lookup. |
| Files a stack reads at load: `source_path` and image `Source`, relative to the YAML's directory. | `internal/resolve/yaml.go:230,279` | The sync sends **every file the loader read**, not only the `-f` files. |
| Image remotes (`docker-oci:`, `ghcr:`, `images:`) are **names defined in the operator's Incus client config**, resolved client-side. **[verified]**: from a Mac with no Incus client config, `apply` of an OCI instance **fails** with `resolving local image "docker-oci:library/alpine:3": Image ... not found`; with a client config that defines `docker-oci`, it works. | `internal/run/run.go:195`, `internal/resolve` (image resolution) | **A phase 1 requirement:** tink falls back to built-in definitions for the registries a stack commonly uses (`docker-oci` to docker.io and `ghcr` to ghcr.io, both OCI; `images` to the linuxcontainers simplestreams server) when the client config lacks them. The client config wins when it defines them. |
| Secrets identity in `~/.config/tink`. | `internal/secrets/identity.go` | Per operator. `backup` never resolves secrets today **[code]**, so the helper needs no identity yet. |
| `buildVersion` reads Go's embedded VCS metadata; without `.git` it prints `(devel)` with unknown commit and date, so two such builds compare equal. | `cmd/tink/main.go:84` | The pipeline must inject the version (`-ldflags -X`); nothing else can tell two helper images apart. |
| Schedules are evaluated in the location of `now`; `plan` on a laptop evaluates the same stamp in laptop time. | `internal/resolve/schedule.go` | See [time zones](#time-zones). |

## The pieces

### 1. Connecting by remote

One connect function used by every command: a remote name from the Incus client configuration (the file the `incus` CLI uses, via
`cliconfig`, as remote backup targets already do **[code]**), else the local socket. `--remote` / `TINK_REMOTE`, **with no ambient default** (decided while building phase 1): the Incus
client's own default remote is deliberately ignored, because tink applies infrastructure, and a forgotten `incus remote switch` must not send a stack to the
wrong server. A command that cannot work against a remote (anything that touches a host path, `tink deploy`, `kind: incus`)
refuses with the reason, and does not fail obscurely.

### 2. The helper instance

- **Image**: a `Containerfile` in this repository: the static tink binary (`CGO_ENABLED=0` builds all of `cmd/tink` **[code]**), CA
  certificates, tzdata. Published to `ghcr.io` by a workflow in `.github/workflows/`, multi-arch, tagged with the tink version and **pinned by
  digest** when installed. v1 needs a registry the host can reach: the Incus OCI path pulls from a registry, and I found no way to import
  an OCI image from a local file. **[code]** A local-build route for a host with no registry access (a non-OCI image) is deferred.
- **Instance**: lives in its **own project** (`tink-helper`), so who can reach it is a project-level question; `boot.autostart=true`,
  `boot.autorestart=true`, `oci.entrypoint=tink daemon run ...`, discovered by `user.tink.helper=<protocol version>`.
- **Reaching Incus**: a **proxy device** with `bind=container` and `connect=unix:/var/lib/incus/unix.socket`, **[verified]** working
  from an OCI app container, with these constraints found in phase 0:
  - the listening socket must go in a directory that **exists in the image**: `listen=unix:/run/incus.sock`. The default
    `/var/lib/incus/unix.socket` fails because the proxy cannot create `/var/lib/incus/`, and the instance will not start;
  - the entrypoint passes **`--socket /run/incus.sock`**. It must **never set `environment.INCUS_SOCKET`** (or, presumably, `INCUS_DIR`):
    the instance's environment is inherited by Incus's own start hook, which then looks at the wrong socket and the instance **fails to
    start at all**. Setting any other variable is fine;
  - the forkproxy dials the host socket as host root for every client, so any process in the container has full admin;
  - the socket's default mode is `0644` root-owned, so **a non-root entrypoint gets `permission denied`**; with `oci.uid`/`oci.gid` set, give the
    proxy `uid`, `gid` and `mode=0660` (they are in-container ids). Simplest for now: run the entrypoint as root;
  - the socket **appears about a second after the entrypoint starts** (the proxy starts in a post-start hook): a first attempt at start-up
    fails with `no such file or directory` and a retry a second later succeeds. The loop retries with backoff and never exits on a connect failure;
  - the proxy is re-established on `incus restart` and on stop plus start. Not tested: a host reboot, and restarting the Incus daemon.
- **Supervision**: `boot.autorestart` **[verified]**: an entrypoint that exits, with status 1 **or 0**, is restarted **10 times and then left
  stopped**: a container that exits immediately ran 11 times in about 8 seconds, then stayed `STOPPED`. One that lives 15 seconds before
  exiting was restarted indefinitely (about 6 a minute, under the limit). A control without `boot.autorestart` ran once. So the entrypoint
  **must not exit**: a fatal condition (a bad config, an unreachable Incus) is logged and waited on with backoff, never an `exit`. If it does
  stop, the limit makes it stay down, and the helper writes a **heartbeat**: each tick it sets `user.tink.helper.tick` (an RFC3339 time) on
  its own instance config. `tink plan` and `tink helper status` warn when the heartbeat is stale or the instance is stopped, even though the
  logs are gone, because the console log is a bounded ring buffer. **[code]**
- **Volumes**: `tink-helper-config` (the Incus client config, holding certificates for remote backup targets) and `tink-helper-data`
  (stacks and jobs, below). The config volume is **sensitive**: it holds client keys, possibly admin keys on other servers. It is excluded
  from any backup that leaves the host, and re-issuing its contents is a manual trust-token exercise, not a free rebuild.

### 3. The loop and the job directory

The helper is one process, `tink daemon run`, with **independent workers**, each isolated from the others (principle 3):

- **backup scheduler**: each tick (default 1 minute), for each synced stack, find the copies that are due and queue them.
- **executor**: runs queued jobs.
- **ingress**: the existing reconcile, on its own interval, never behind a copy.
- **heartbeat**.

Every unit of work is a **job**, whether the schedule or a laptop created it, in `tink-helper-data/jobs/<id>/`:

```
request.json    what to do: {proto, kind, args, origin: schedule|trigger, stack: <name or bundled>}
bundle/         (optional) a stack, and every file it reads, sent with this request only
READY           written last; the executor ignores the directory until it exists
status.json     {proto, state: queued|running|succeeded|failed|cancelled, started, finished, per-copy results}
log             bounded, redacted best-effort
cancel          created by a client to ask the job to stop
```

Because the file API **truncates in place** (not atomic, **[code]**; **[verified]**: a reader polling a 300 MB file during its push saw 22
different sizes on the way up), nothing reads a directory until `READY` exists, and stack activation works the same way (below). **[verified]**:
with `READY` pushed last, a reader polling every 50 ms read all 40 test jobs (12 files of 10 KB to 3 MB each) intact, while a reader that acted as soon
as a directory had any file once saw only 7 of 12 files. `proto` is a small integer, the protocol version: it is what the CLI and the helper must agree on.

**Locks**: there is one executor, so jobs run in order, and a **per-(volume, target) guard** means the same copy never runs twice at once.
The scheduler skips anything queued or running. A triggered job for something already running is queued behind it, or refused with
"already running since T", chosen by the trigger's flag (default: refuse). Ingress and the heartbeat are never behind the executor.
A file lock in the container guards only against a second helper process by mistake.

**Failure and retry.** A copy that fails today leaves nothing, so the due check retries every tick forever (a full target means snapshot,
copy, delete, every minute). **[code]** The fix is a prerequisite for the scheduler: a failed copy stamps `user.tink.backup.copy.<target>.fail.at`
and `.fail.n` on the source volume (time and consecutive count, cleared by a success). A copy is retried after
`min(5 min x 2^(n-1), the copy's own interval)`. **The stamp holds no message**: Incus errors can echo credentials (the TrueNAS API
key appeared in one), and a volume's config is the wrong place for that. The reason lives in the job's `log` and `status.json`. `plan` and
`helper status` show failures; a run with a failed copy exits non-zero.

**A run that is cut off** (the helper restarts, a host reboots) leaves the same traces as any failed copy: the temporary snapshot has a
24-hour expiry, and a partial restore point has no markers so it can never be used or pruned. **[code]** On start the executor marks
any job still `running` as `failed (interrupted)` so it is retried by the schedule.

**Stacks** live in `tink-helper-data/stacks/<name>/`, one directory per stack (so two repositories' `tink.yaml` cannot collide).
They are loaded **one at a time**: a stack that no longer parses, or names a deleted volume, is reported in `status` and skipped, and does
not stop the others. Activation is atomic: `tink helper sync` writes `stacks/<name>.new/`, then `READY`, and the loop swaps it in. Only
`tink helper sync` changes what the **scheduler** runs; see the next section for why a trigger does not.

### 4. Triggering from a laptop

An Incus `exec` is **killed when its control or output websocket drops**, and tink's exec helper uses exactly that mode and buffers all output
until the end. **[code]** A laptop that sleeps or loses a tunnel would kill a multi-hour copy mid-flight. So the trigger does not hold a
run open:

1. **bundle**: load the stack and record every file the loader read (the YAML, `source_path`, image sources), with paths confined to a
   per-job directory (no absolute paths, no `..`; the helper re-validates when it reads);
2. **enqueue**: write the job directory with the file API and create `READY` last;
3. **follow** (optional): poll `status.json` and the bounded `log` and print them. `Ctrl-C` **detaches**; the job carries on.
   `tink helper jobs` lists recent jobs, `tink helper log ID` follows one, `tink helper cancel ID` creates `cancel`.

The job uses its **own bundle**, so a laptop checkout of a feature branch with a shortened `retain` cannot change what the scheduler does or
prune real restore points on the next tick. Persistent changes are an explicit `tink helper sync`. Whether `tink apply` should offer to
sync is open (below).

No daemon API of its own is added, but the earlier rationale ("tink owns no state") no longer holds in full: the helper owns reconstructible
state, and its interface is **Incus's file API plus a versioned job-directory protocol**, not an RPC server. Incus is still the transport and
the access control. Only the file API and (for `status`) reads of instance config are needed, **no exec**.

`--local` runs the command in-process, with a warning when a relayed remote copy would pass through this machine. With no helper installed, the
command runs locally as it does today. `restore` and `verify` copy within one server, which Incus does server-side, so they never need the helper.

### 5. Lifecycle commands

`tink helper install | upgrade | status | sync | jobs | log | cancel | remove`.

- **install**: creates the project, instance, volumes and devices (idempotent), sets `TZ`, resolves and records the image digest, and refuses
  if a helper already exists on that server in *any* project (it scans, since discovery is by `user.tink.helper`).
- **upgrade**: **drains first**: the scheduler stops queuing, the executor finishes the running job (a timeout, then it asks), then the helper is
  replaced. `--force` skips the wait and interrupts the job (which then retries by schedule). Incus's own `rebuild` is non-atomic, needs a stopped
  instance, and does not refresh `oci.*` keys **[code]**, so upgrade recreates the instance and keeps the volumes. It shows old and new digests.
- **Version rule (changed):** the CLI and the helper need to agree on the **protocol version**, not the exact tink version. The CLI **refuses
  only on a protocol mismatch** and warns when the versions differ. An exact-version rule would force a helper restart for every laptop
  `go install`, lock two operators with different versions out of each other, and cannot work at all until a version is injected.
- **status**: the instance state, image digest and version, **heartbeat age**, `TZ`, each stack (synced when, parse errors), the recent jobs, and
  for every copy its last success and its failure count (from the volume stamps, so it is true even when the log is gone).

## Time zones

Cron schedules are evaluated in the location of `now`. **[code]** `helper install` sets `environment.TZ` explicitly (default: the host's), `status`
shows it, and **schedules are evaluated in the Incus server's zone** so they line up with Incus's own snapshot schedules. `plan` evaluates in the
helper's zone when it finds a helper, else in local time. Due-ness is stamp-based, so a DST change cannot cause a repeat or a skip, only shift
the next run by an hour. **[hypothesis]** (robfig cron's handling of a non-existent local time is not read.)

## Backup engine changes this design needs

Small, separate from the helper, and prerequisites for phase 2:

1. **Failure stamps and backoff** (above).
2. **Per-(volume, target) guard** inside the process.
3. **Server identity in the restore-point marker. Proposed.** `copy-of` is `<project>/<pool>/<volume>`, so two servers copying a same-named
   volume into one remote pool are treated as one volume, and the newest wins, which can **prune the other server's restore points**.
   Add `user.tink.backup.copy-server=<server_name>`: **prune only touches points from this server**; restore considers all, shows the
   server, and defaults to the newest overall unless told otherwise. A rebuilt host with the same name continues its history; one with a
   new name still **finds** every point (only pruning is scoped), so the earlier "a rebuilt host must still find its backups" goal holds.
   Existing restore points without the marker are treated as this server's.

## Security

**Now.** The helper holds the host's Incus admin socket, so the helper is root on the host. That is no worse than `tink daemon` as root today, and
it is stated, not hidden. Consequences the design takes seriously:

- **Who can reach the helper is who holds host root**: anyone who can push files into it or exec in it, including a restricted or OIDC user
  granted access. It is in its own project for that reason; the docs say so.
- **The stack is data, never code.** The helper never runs stack-supplied commands itself: no `kind: incus`, no `apply`, no shell from a stack.
  `backup verify`'s check command runs only inside its throwaway sandbox instance (no network, volume mounted read-only), never in the helper
  or on the host. A future job that would run stack commands in the helper needs its own design.
- **Bundle paths are confined** (no absolute, no `..`, per-job directory), on the laptop when writing and in the helper when reading.
- **Supply chain**: the host's incusd pulls the image as root and hands it the socket. The image is **pinned by digest** at `install` and `upgrade`
  (the digest is recorded on the instance), and the doc names the publisher that is trusted.
- **Secrets**: resolved `${secret:}` values live in instance config, which the helper can read. No backup job resolves secrets today. A job that
  does gets the identity deliberately and says so.
- **Failure text can contain credentials** (an Incus error echoed an API key). Stamps never carry it; logs are redacted best-effort.

**Later (hardening).** Replace the socket with a client certificate scoped to the projects the helper manages. Two things to check first: an Incus
*restricted* certificate is bounded only if the server's authorization lets Incus's own check run (a server routing
`authorization.client.tls-restricted` through a scriptlet that returns `True` gives it everything, **[verified]** on a lab VPS); and whether
the proxy's `security.uid`/`security.gid` can be set to a host user in the `incus` group for a restricted socket **[hypothesis, untested]**.

## Phasing

Each phase is useful alone and ends in something checkable on the lab host.

0. **Spike. Done**, see [Phase 0 findings](#phase-0-findings); the one thing it could not do is a host reboot or an Incus daemon restart. On Tron,
   with a throwaway container: (a) the proxy-device socket from an unprivileged container, including the entrypoint racing the proxy and a
   restart; (b) `boot.autorestart` behaviour in a crash loop; (c) the job-directory protocol over the file API; (d) `plan`, `apply`, `backup restore`,
   `backup verify` and `backup run` from a **macOS** client.
1. **Remote-capable tink. Done and merged** (#15, #17): `--remote` / `$TINK_REMOTE` with no ambient default, one connect function, built-in image remotes,
   refusals for host-local commands, `kind: incus` BLOCKED, a relay note in `backup run`, and **`tink remote add|list|remove`** so a machine with no Incus
   client can be set up (TLS only; the server is verified by the token's fingerprint, `--fingerprint` or an explicit acceptance before anything secret is
   sent; the token is never on argv). Validated from a Mac over TLS with no tunnel (`plan` 0.36 s, `apply` of a project, volume and OCI instance 6.6 s,
   `backup restore`, `verify`, `run` to a pool target, `restore --from` it) and, for `remote add`, from a blank config directory with a real trust token.
   Network latency over a real WAN link was not measured.
*Done when:* `plan`, `apply` (an OCI instance), `backup restore` and `backup verify` run from a laptop
   **that has no Incus client config** against Tron, over `--remote` (TLS, a trusted certificate), and a stack with `kind: incus` is refused there with
   the reason.
2. **The scheduler and the engine changes. Built, validated in the lab, and merged** (#18 to #22) (see [Phase 2 findings](#phase-2-findings)): failure stamps and backoff, the
   server marker, the per-copy guard, the job directory and executor, stack sync with atomic activation, the backup scheduler and heartbeat in `daemon run`
   (`--stacks`, `--jobs`, `--timezone`, `--no-ingress`), supervised workers, and local `daemon sync|enqueue|jobs|cancel`. It runs under any supervisor (a transient
   systemd unit on the lab host). **2e** adds the in-progress mark and the sweep of abandoned copies (below).
3. **The helper.** Containerfile and image workflow (version injection, multi-arch, digest), `tink helper install|upgrade|status|sync|remove`,
   the ingress volume device and configurable paths, `deploy` no longer reinstalling the host daemon. *Done when:* killing the process brings it
   back, a crash loop is detected by `status` and `plan`, a host reboot brings it back with the next due copy still running, and `upgrade`
   waits for a running copy.
4. **The laptop trigger.** Bundle, enqueue, follow, detach, `jobs`, `log`, `cancel`, `--local`. *Done when:* a laptop that sleeps mid-copy
   does not interrupt it, and no volume data passes through the laptop.
5. **Retire `daemon install`.** Deprecated with a notice, kept for a release, then removed; migration is `tink helper install` plus removing the
   unit; `deploy` is updated. *Done when:* a host runs only the helper.

## Open questions

- **Should `tink apply` offer to sync the stack to the helper?** Explicit `helper sync` first is the safe default; syncing on apply keeps the scheduler
  from lagging the repository. Revisit after phase 4.
- **A host with no registry access** has no route to the image (v1 needs one). A non-OCI image published as a release asset is the likely answer.
- **The helper's own project** (`tink-helper`) is proposed; if the volumes it must reach live in several projects the socket still allows it, but
  that is worth confirming in phase 0.
- **Alerting.** A failed copy is visible in `status`, `plan` and the exit code, and a stale heartbeat is visible; nothing *sends* anything. A
  notification hook is not designed.
- **Server name as identity.** `server_name` is the host name by default; hosts that share a name would collide. Whether the Incus server UUID is
  available and stable enough is to be checked in phase 2.
- **Several helpers or a cluster.** One helper per Incus server is enforced at `install`. Incus clustering is not considered.

## Phase 0 findings

Run on the lab host (Incus 7.5.1, an alpine OCI app container, the tink binary mounted read-only, a throwaway project, all deleted afterwards).

| Question | Result |
|---|---|
| Does `bind=container` proxy the host's Incus socket to an OCI app container? | **Yes.** `tink plan` ran through it. The default listen path `/var/lib/incus/unix.socket` **fails** (the proxy cannot create that directory in the image); `/run/incus.sock` works. |
| Can the helper set `INCUS_SOCKET` for tink? | **No, and it is a trap.** `environment.INCUS_SOCKET` on an instance stops the instance from starting (Incus's own start hook inherits it). Use `--socket`. |
| Does the entrypoint race the proxy? | **Yes, by about a second.** The first attempt fails, a retry succeeds. |
| Non-root entrypoint? | `permission denied` with the default mode; works with proxy `uid`/`gid`/`mode=0660` set to the in-container ids. |
| Survives `incus restart` and stop plus start? | **Yes**, and the retry loop reconnected each time. A host reboot and an Incus daemon restart were **not tested**: the lab host runs other services. |
| `boot.autorestart` limits? | 10 restarts then it stays stopped, whether the exit is 0 or 1. A slow loop (15 s per cycle) is restarted forever. |
| Is a half-pushed file visible to a reader? | **Yes** (22 sizes seen while pushing 300 MB). A job directory is therefore read only once `READY` exists, which worked 40 of 40 with a concurrent reader; the same reader without `READY` once saw a partial job. |
| File API cost, over the local socket | 300 MB push 0.5 s; pull of 1, 5, 20 MB: 34, 49, 107 ms; 200 x 1 KB files with `-r`: 0.16 s; one small file push or pull about 30 ms. **Network latency from a laptop was not measured.** |
| `plan`, `apply` of an OCI instance, `backup restore`, `backup verify` and `backup run` (a pool target) from a **macOS arm64** client | **All work**, through an SSH-forwarded unix socket, after the image-remote problem above. `apply` took 6.7 s. **No `skopeo` on the Mac was needed** (the server pulls the image). The instance came up **`x86_64`**, the server's architecture, not the client's arm64. Restore and `backup run` copy server-side. |
| How the Mac reached Tron's Incus for the spike | An SSH **unix-socket forward** (`ssh -L /tmp/x.sock:/var/lib/incus/unix.socket user@host`) with `--socket`: no certificate, no new network exposure. It needs the SSH user to be in `incus-admin`, and **a group change only reaches an SSH session opened after it** (a tunnel opened before gave `EOF`). The HTTPS path (`--remote`, a trusted certificate, `:8443`) was **not** tested. |
| Tron's API exposure | Already listening on `:8443`; the SSH user is **not** in `incus-admin`, so forwarding the unix socket over SSH does not work for it, but a laptop can reach `:8443` if it is trusted. |

**Not done in phase 0:** a host reboot and an Incus daemon restart (the lab host runs other services), and anything over a real network: the file API's
latency from a laptop, and the HTTPS `--remote` path. Both belong to phase 1.

## Phase 2 findings

Run on the lab host (Incus 7.5.1) under a transient systemd unit (`Restart=always`), with a real stack: `lib` (150 MB, every minute) copied to a TrueNAS-backed pool, and
`docs` copied to that pool and to a pool that does not exist, on a `*/10` schedule. Everything was deleted afterwards.

| Question | Result |
|---|---|
| Do copies run on their schedule, unattended? | **Yes**: a job per minute for `lib`; each job's log is what `tink backup run` prints. |
| Does a failing copy back off? | **Yes**: `docs -> broken` failed once, was marked, then every later job logged `backing off after 1 failed attempt(s), next try after 01:29 MDT` and did not retry it; it retried at exactly 5 minutes (07:29:46 after 07:24:46) and no sooner. |
| Does one failing copy stop the others in its job? | **No**: the job is `failed`, but `lib -> nas` and `docs -> nas` in the same job completed. |
| `kill -9` in the middle of a copy? | systemd restarted the daemon in 2 s (`NRestarts=1`); the job was marked `failed: interrupted`; the next scheduled run (12 s later) succeeded and made a proper restore point. |
| What did the kill leave behind? | **A partial volume on the target with no marker** (tink will never use or prune it, as designed) and the source's temporary snapshot (24 h expiry). **The partial volume is a space leak**: nothing ever removes it. |
| A missed run (daemon down 170 s, schedule every minute)? | **Caught up once**: one job at start-up, then the next regular slot, not one per missed slot. |
| A job made by dropping plain files (`request.json`, then `READY`), no tink involved? | **Runs.** So does one sent with its own bundle by a command that exits immediately. |
| A malformed job? | `failed: unreadable request`, and the daemon carried on. |
| Does a long copy stall the scheduler? | Not in the lab (copies were seconds), so a unit test pins it instead: with a copy blocked, the heartbeat keeps advancing; mutation-checked against a serialised design. |
| Not tested live | Ingress running beside it (the lab daemon ran `--no-ingress`), a copy to a remote server, the job `cancel` of a copy in flight (cancel takes effect between copies; an operation in flight finishes), and a host reboot. |

**Resolved (phase 2e): partial copies.** A copy is marked **in progress** (`copy-partial-of`/`-at`, a different key from a restore point's) from the moment its
volume is created, and the mark is swapped for the restore point's markers when the copy completes; `backup run` then removes volumes that carry this tink's
in-progress mark for this volume, started by this server, more than 7 days ago, and never anything else (not a volume that is also a restore point, not an
unmarked look-alike, not another server's, not a young one). Checked live, which corrected an assumption: Incus **does** apply the config given at creation, for a
copy from a snapshot into a TrueNAS pool and into a local pool; and a copy made *inside* one Incus server **keeps running when tink is killed**, so what a
`kill -9` leaves is usually a *complete* copy that was never marked, not a half-written one. It is removed anyway (a newer copy exists by then, and an
unverified one is not a backup). Still unchecked: whether a *remote* target applies the in-progress mark to a relayed copy (the VPS tunnel was down); if it does
not, the sweep finds nothing there and a leak from a relayed copy cut off by tink being killed stays until someone removes it.

## What the review changed

The first revision was reviewed against the code and the Incus 7.4 source. What changed in this one:

| Finding | Change |
|---|---|
| A laptop exec is killed when its websocket drops (and tink's exec buffers everything). | The trigger became **detached**: bundle, enqueue by file API, follow, detach. No exec. |
| A failed copy is retried every tick; no failure is recorded anywhere. | **Failure stamps and backoff**, as a phase 2 prerequisite; stamps hold no message. |
| One lock would stall ingress behind a long copy. | **Independent workers**, a per-copy guard, ingress never behind the executor. |
| `upgrade` or a restart kills a running copy; "exactly its own version" forces restarts and locks operators out. | **Drain before upgrade**; the rule is the **protocol version**. |
| The trigger persisted the laptop's stack into the scheduler. | A trigger uses **its own bundle**; only `helper sync` changes the scheduler. |
| `kind: incus` and the other shell-outs are not API calls; `deploy` reinstalls the daemon. | `kind: incus` **refused under `--remote`**; `deploy` host-local and no longer reinstalls the daemon once a helper exists. |
| Time zones differ between laptop and helper. | **`TZ` set explicitly**, schedules evaluated in the server's zone, shown in `status`. |
| Two servers can prune each other's restore points. | **Server marker**; prune only touches this server's points. |
| Failure visibility was weaker than described; autorestart gives up after 10 per minute. | **Heartbeat** on instance config, shown by `plan` and `status`; an entrypoint that never exits on a connect failure. |
| Stacks: a bad stack, filename collisions, files outside the `-f` files, non-atomic file push. | **One directory per stack, loaded one at a time**, the sync sends every file the loader read, and activation by `READY` last. |
| Ingress path was a long pole in phase 5. | Solved with a **disk device** for the `ingress-routes` volume, moved into phase 3. |
| Phase 0 gated phase 3 but did not exist; phase 1's done-when passed by grep; phase 5 hid the real work. | A **phase 0 spike**; done-whens that run commands from a laptop; the ingress and `deploy` work moved up. |
| Security items the doc did not state (stack is code, who can reach the helper, traversal, supply chain, secrets, the config volume). | Stated under [Security](#security), with cheap mitigations built in (own project, confined paths, digest pin, never run stack commands). |
| The helper adds the first state tink owns, which weakened the "no daemon API" rationale. | The doc says so, and gives the real rationale: the interface is Incus's file API plus a versioned job protocol. |

## Alternatives considered

- **Keep `daemon install` (init-system units).** Works, but needs a generator per init system and ties tink to the host.
- **cron or a timer calling `tink backup run --due`.** Simplest, and `--due` was designed for it; it stays a supported way to run the same command. It
  lacks the failure backoff, the heartbeat and the helper's other benefits unless phase 2's engine changes are in.
- **A trigger by `exec`.** The first revision. Rejected: the server kills the command when the connection drops.
- **A tink daemon with its own API** (a Docker-style client and server). Rejected: Incus's file API plus a versioned job directory already provides
  transport and access control, with nothing new to authenticate or listen on.
- **The helper in its own repository.** Rejected: tightly bound to tink; two versions to keep in step.
