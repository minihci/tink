# The tink helper: design, and what was built

**Status (2026-10-08, tink v0.1.4): built through phase 3, and released.** The helper exists and runs on the lab host: `tink helper install|upgrade|remove|status|remote`,
the image `ghcr.io/minihci/tink-helper:<tag>` (published by a workflow, alongside release binaries for macOS and Linux), the backup scheduler and job executor, the ingress half,
a status document that `plan` reads, and the helper's own revocable certificate. **Not built:** handing a `tink backup run` started on another machine to the helper (phase 4, which
the build has made much smaller: see [the hand-off](#4-handing-a-run-to-the-helper-not-built)), and retiring `tink daemon install` (phase 5).

**Where to read what.** [`helper.md`](helper.md) is how to use what exists; [`daemon-jobs.md`](daemon-jobs.md) is the scheduler and its jobs; [`volume-backup.md`](volume-backup.md) is the
policy and the copies. **This document is the reasoning and the evidence behind them**: what was decided and why, what was measured, what was tried and replaced, and what is left.
Where it and `helper.md` disagree, `helper.md` describes the code.

Claims are marked **[code]** (read in this repository or in the Incus source), **[verified]** (tried live on the lab host), **[docs]** (upstream documentation) or **[hypothesis]** (believed,
not checked).

## How the design changed

This document was revised four times while the helper was built. The layers are folded into one table so that nothing below needs a "revision" label to be understood. Each row is something
the design said that was **replaced**, with what replaced it, and why. The sections after it describe the design as it now stands.

| The design said | As built | Why |
|---|---|---|
| The scheduler runs the stacks **synced** into the helper (`daemon sync`, `--stacks`, a stack store with atomic activation). Built in phase 2. | The scheduler **lists volumes** and reads the copy policy each one carries (`user.tink.backup.policy`, written by `apply`) (#23). `daemon sync`, `--stacks`, `daemon enqueue --stack` and the stack store were **removed, not deprecated**. | A synced stack is a second copy of the intent that `plan` cannot compare, can lag the repository without a word, and a lost helper meant re-syncing everything. See [the policy on the volume](#the-policy-on-the-volume). |
| A triggered job **carries a bundle**: the stack and every file its load read, confined, in `bundle/`. | **No bundle.** A job works from the volumes' policies (#41); a request that still carries a stack is **refused**, not run as something else. | Once the policy is on the volumes a job needs no stack, and what the bundle protected (path confinement, secrets at rest, a second copy of the intent) had nothing left to protect. -303 lines of production code and -265 of tests. |
| The helper reaches Incus through a proxy device to the host's **root unix socket**. | A TCP proxy to the host's **HTTPS API** on the helper's own loopback, with **its own certificate**, enrolled from inside the instance with a single-use token (#27). The socket is described as a fallback and **not built**. | A credential that can be revoked and attributed costs little and beats a shared root socket. It is **not confined**, and the design says so. See [the credential](#the-credential). |
| Liveness is a key written every tick (`user.tink.helper.tick`). | **One status document**, `user.tink.helper.status`, written on change plus a slow heartbeat, readable when the helper is dead (#26). `plan` and `tink helper status` read it; `apply` refuses to write a policy newer than the helper reads (#30). | A tick says only that the helper ticked, and costs an event a minute. See [the status document](#the-status-document). |
| The ingress half gets the `ingress-routes` **volume as a disk device**. | The reconcile has an **API mode**: it reads and writes the routes inside the ingress instance through its file API and reloads Caddy there (#31). `deploy` then does not install the host's `tink-daemon`. | Volumes are per project and the helper has a project of its own; it needs no mount, and it works under `--remote`. |
| A host with no registry access has no route to the image (deferred). | `tink helper install --binary` runs a binary you built in a stock alpine image; a release build installs the image published for its own version (#27, #28). | Release binaries and the image come from the same tag, built by the same workflow. |
| `daemon run` refuses a remote. | It **accepts one**, refusing only the ingress path that needs the host's filesystem (#25). | The helper runs `daemon run` as a client of its own host. |

**Added beyond what the design foresaw:** `tink helper remote add|list|remove`, because a copy to another server needs a remote the *helper* has, not you (#32); `plan` says what will run the
copies and shows the policy in words (#33); a backup target may say where its remote is, as an opt-in (#34); `kind: stack` and `user.tink.stack` so a volume points back at its stack, and
`tink backup forget` (#23); an in-process registry client, so `plan` and `apply` need no `skopeo` (#53); an OCI remote's `credentials_helper` (#55); release binaries (#38, #40); and the
backup vocabulary moved into `internal/backupmeta` (#43 to #45).

## What this is

A long-running **tink helper**: an Incus instance, run from an image built from this repository, that does the work tink should do on a
schedule or near the data. It is meant to replace `tink daemon install` (phase 5). The first jobs are backup copies on their schedule and the ingress
reconcile loop that `tink daemon run` already runs. It is a generic home for jobs defined now or discovered later.

The same direction makes the `tink` CLI independent of the host it manages: the host-local, scheduled work moves into the helper,
so the CLI needs only the Incus API and can run from anywhere that can reach it, such as a laptop against the server on Tron.

## Decisions already made (by the user)

| Decision | Reason |
|---|---|
| The helper replaces `tink daemon install`. | One supervision mechanism (Incus starts instances) instead of a unit generator per init system. |
| The helper's image, Containerfile and pipeline live **in this repository**. | Tightly bound to tink; keeps the version in step; small enough not to pull the repo off its direction. |
| A laptop `tink backup run` **triggers the helper** and does not run the copy itself. | Relayed backup data passes through whichever process runs the copy. Heavy work belongs next to the data. |
| The helper authenticates with **its own client certificate**, trusted by the host: revocable and auditable. It is **not confined**: it has the same reach as the root socket, and the design says so rather than claiming a boundary. (Revision 4; replaces "simple first: the socket".) | A credential that can be revoked and attributed costs little and is a better default than a shared root socket. Confinement was not promised because Incus 7.5.1 does not deliver it ([security](#security)). |

**Where each stands.** The helper's image and pipeline are in this repository (built, #28). Its own certificate is built (#27). That it replaces `tink daemon install` is **phase 5, not
yet done**. That a laptop `tink backup run` triggers the helper is **phase 4, not yet built**: today a run you start yourself runs where you start it.

## Principles the review made explicit

1. **A run must outlive whoever started it.** An Incus `exec` is killed when its websocket drops (see [the trigger](#4-handing-a-run-to-the-helper-not-built)).
   Anything long-running therefore cannot be an exec held open by a laptop.
2. **The helper is disposable, not stateless.** It owns two things: a job directory and its status document. Both are reconstructible (jobs are
   history, the status is rewritten as things change). **Revision 3:** backup state *and the backup policy* stay on the volumes, as the
   policy, stamps and markers, never in the helper. A helper that is lost is replaced and finds its work by listing volumes; there is nothing to
   re-sync. (An earlier design also kept synced stacks here: see [how the design changed](#how-the-design-changed).) Its own credential is the one thing it cannot rebuild alone: re-enrolling needs the
   operator ([the credential](#the-credential)).
3. **A run is isolated from other runs.** One bad volume, one slow copy or one failing target must not stop anything else.
4. **Failures leave a mark.** Success stamps alone make a failing job look merely "not yet run", and a scheduler that retries it forever.
5. **The helper never executes stack-supplied commands itself.** It no longer receives a stack at all; the policy it reads from a volume is data it reads, not code it
   runs (see [security](#security)).
6. **One place says what should happen, and `plan` can see it.** The YAML is the intent; what is applied is on the volume; the difference is an
   ordinary `plan` update. A copy of the intent that `plan` cannot compare against (a synced stack) is drift waiting to happen.
7. **A helper that has stopped can still be read.** What it was doing, and that it stopped, must be visible from outside without the helper's help: its status is
   on its instance config, which is readable whether the instance runs or not. A backup system fails by going quiet, so the quiet has to be detectable.
8. **Say what is claimed.** The helper's credential is revocable and auditable and is not a boundary. Where a property is not delivered, the design says so
   (see [security](#security)).

## tink's host couplings, and how each was resolved

Almost every command opened Incus through one function, `incusapi.Connect(socket)` (12 call sites, default socket `/var/lib/incus/unix.socket`). **[code]** What else tied tink to the
machine it ran on, and what became of it:

| Coupling | Outcome |
|---|---|
| One connect function, a unix socket. | **Built** (#15): `--remote` / `$TINK_REMOTE`, with no ambient default, so a forgotten `incus remote switch` cannot send a stack to the wrong server. |
| `kind: incus` resources run user-written argv for the local `incus` CLI, with local paths, against that CLI's own default remote. Not replaceable by an API call. | **Built** (#15): BLOCKED with the reason under `--remote`; unchanged locally. |
| `tink deploy` provisions the machine it runs on, **and installs the host daemon**. | Stays host-local, and **no longer installs a second ingress reconciler** when a helper runs the ingress (#31). |
| `tink daemon install` only **prints** a unit, for the old ingress-only invocation. | **Still there. Retiring it is phase 5.** |
| Ingress reads and writes a **host path inside a storage pool**, the pool name fixed. | **Resolved differently from the design**: through the ingress instance's file API, no mount at all (#31). The host path is unchanged and still the default. |
| `skopeo` for image checks. | Replaced by an **in-process registry client** (#53), so `plan` and `apply` work from anywhere; one note, not one per image, when images cannot be checked (#50). |
| Files a stack reads at load (`source_path`, image `Source`). | **Moot**: a job never carries a stack (#41). |
| Image remotes (`docker-oci:` and friends) are names in the operator's Incus client config. **[verified]**: from a Mac with none, `apply` of an OCI instance failed with `Image ... not found`. | **Built** (#15): built-in definitions for `docker-oci`, `ghcr` and `images`, only where the client config lacks them; the client's own always wins. |
| Secrets identity in `~/.config/tink`. | Unchanged. `backup` never resolves secrets **[code]**, so the helper needs no identity. |
| `buildVersion` reads Go's embedded VCS metadata; without `.git` two builds compare equal. | **Built** (#28): the release workflows compile the version in (`-X main.injectedVersion`). |
| Schedules are evaluated in the location of `now`; `plan` on a laptop evaluates the same stamp in laptop time. | **Partly built**: the helper's zone is set explicitly (`install --timezone`) and published in its status, and `daemon run --timezone` evaluates its schedules there. **`plan` does not yet use the helper's zone**: see [time zones](#time-zones). |

## The pieces

### 1. Connecting by remote

One connect function used by every command: a remote name from the Incus client configuration (the file the `incus` CLI uses, via
`cliconfig`, as remote backup targets already do **[code]**), else the local socket. `--remote` / `TINK_REMOTE`, **with no ambient default** (decided while building phase 1): the Incus
client's own default remote is deliberately ignored, because tink applies infrastructure, and a forgotten `incus remote switch` must not send a stack to the
wrong server. A command that cannot work against a remote (anything that touches a host path, `tink deploy`, `kind: incus`)
refuses with the reason, and does not fail obscurely. **Built** (#15, #17, #25).

The helper is this same machinery pointed at its own host: it holds a client certificate that the host trusts, and dials the host's API as a remote
(`tink --remote host ...`). **[verified]** from inside a container on the lab host ([findings](#credential-and-status-document-findings)). That is what makes the
credential its own, rather than the host's.

### 2. The helper instance

- **Image** (**built**, #28): `build/helper/Containerfile`: the static tink binary (`CGO_ENABLED=0` builds all of `cmd/tink` **[code]**) in a small alpine base (pinned by digest) with CA certificates and a
  shell, about 27 MB. `.github/workflows/helper-image.yml` builds it for amd64 and arm64 with the version compiled in, and **on a tag publishes** `ghcr.io/minihci/tink-helper:<tag>`; the repository is
  public and the package was pullable without a login. The instance records the image's **fingerprint** on itself (`user.tink.helper.image-fingerprint`), because a tag can move and a fingerprint cannot.
  A host with no registry access uses `install --binary` (see [how the design changed](#how-the-design-changed)).
- **Instance**: lives in its **own project** (`tink-helper`), so who can reach it is a project-level question; `boot.autostart=true`,
  `boot.autorestart=true`, `oci.entrypoint=tink daemon run ...`, discovered by `user.tink.helper` (any project).
- **Reaching Incus**: the host's HTTPS API, with [the helper's own certificate](#the-credential).
  - A **TCP proxy device** with `bind=container`: `listen=tcp:127.0.0.1:8443` inside the container, `connect=tcp:<the host's API address>` on the host. The container
    reaches the API without the API being exposed to anything it was not already exposed to. **[verified]** on the lab host.
  - The instance needs **a NIC**. An OCI app container with no network has its loopback down, and the proxy's listener fails with `Network unreachable`
    **[verified]**; with the bridge attached it works. A helper that copies to a remote target needs the network for that anyway.
  - **`core.https_address` must be set** (`tink deploy` sets it; the lab host listens on `:8443`). `helper install` reads it and points the proxy at it. A host with no
    HTTPS listener is the case for the fallback below, not a reason to turn one on silently.
  - **Fallback: the unix socket** (**not built**: `install` refuses a host whose API is not listening). It is how phase 0 did it and what the first design used. It is root, it cannot be revoked short of removing the device, and
    its requests are indistinguishable from yours at a terminal (`unix`/`root`). Constraints found in phase 0, which apply to this fallback only:
    - the listening socket must go in a directory that **exists in the image**: `listen=unix:/run/incus.sock`. The default
      `/var/lib/incus/unix.socket` fails because the proxy cannot create `/var/lib/incus/`, and the instance will not start;
    - the entrypoint passes **`--socket /run/incus.sock`**. It must **never set `environment.INCUS_SOCKET`** (or, presumably, `INCUS_DIR`):
      the instance's environment is inherited by Incus's own start hook, which then looks at the wrong socket and the instance **fails to
      start at all**. Setting any other variable is fine;
    - the socket's default mode is `0644` root-owned, so **a non-root entrypoint gets `permission denied`**; with `oci.uid`/`oci.gid` set, give the
      proxy `uid`, `gid` and `mode=0660` (they are in-container ids). Simplest: run the entrypoint as root;
    - the socket **appears about a second after the entrypoint starts** (the proxy starts in a post-start hook): a first attempt at start-up
      fails with `no such file or directory` and a retry a second later succeeds;
    - the proxy is re-established on `incus restart` and on stop plus start. Not tested: a host reboot, and restarting the Incus daemon.
  - Either way the loop retries with backoff and **never exits on a connect failure**: until the helper is enrolled, or while the host is starting, it waits.
- **Supervision**: `boot.autorestart` **[verified]**: an entrypoint that exits, with status 1 **or 0**, is restarted **10 times and then left
  stopped**: a container that exits immediately ran 11 times in about 8 seconds, then stayed `STOPPED`. One that lives 15 seconds before
  exiting was restarted indefinitely (about 6 a minute, under the limit). A control without `boot.autorestart` ran once. So the entrypoint
  **must not exit**: a fatal condition (a bad config, an unreachable Incus, a revoked certificate) is logged and waited on with backoff, never an `exit`. If it does
  stop anyway, the limit makes it stay down, and **its last [status document](#the-status-document) is still on its instance config, going stale**: `tink plan`,
  `tink helper status` and `tink helper status --check` say the helper is stale or stopped, even though its logs are gone (the console log is a bounded ring
  buffer). **[code]**
- **Volumes**: `tink-helper-config`, mounted where the Incus client looks for its configuration (the path the client reads, so no environment variable is needed), holds
  **the helper's own client key and certificate** and the certificates for remote backup targets; and `tink-helper-data` (jobs, below). The config volume is **sensitive**: it holds client keys, one of them the helper's own, possibly admin keys on other servers. It is excluded
  from any backup that leaves the host, and re-issuing its contents is an operator action ([`install --reissue`](#5-lifecycle-commands)), not a free rebuild.

### 3. The loop and the job directory

The helper is one process, `tink daemon run`, with **independent workers**, each isolated from the others (principle 3):

- **backup scheduler**: each tick (default 1 minute), for each volume carrying a backup policy, find the copies that are due and queue them.
- **executor**: runs queued jobs.
- **ingress**: the existing reconcile, on its own interval, never behind a copy.
- **status publisher**: writes the [status document](#the-status-document) when something changes, and a heartbeat every few minutes. (The heartbeat *file* in
  the jobs directory, which `tink daemon jobs` shows, stays: it is the daemon's own liveness on the machine that has the directory. The status document is what can
  be read from anywhere.)

All of this is **built**: #21 and #22 (scheduler, executor, supervised workers, the sweep), #23 (volumes, not stacks), #26 (the status publisher), #31 (ingress).

Every unit of work is a **job**, whether the schedule or a person created it, in `tink-helper-data/jobs/<id>/`:

```
request.json    what to do: {proto, kind, origin: schedule|trigger, created, args}
READY           written last; the executor ignores the directory until it exists
status.json     {proto, state: queued|running|succeeded|failed|cancelled, started, finished, per-copy results}
log             bounded, redacted best-effort
cancel          created by a client to ask the job to stop
```

Because the file API **truncates in place** (not atomic, **[code]**; **[verified]**: a reader polling a 300 MB file during its push saw 22
different sizes on the way up), nothing reads a directory until `READY` exists. **[verified]**:
with `READY` pushed last, a reader polling every 50 ms read all 40 test jobs (12 files of 10 KB to 3 MB each) intact, while a reader that acted as soon
as a directory had any file once saw only 7 of 12 files. `proto` is a small integer, the protocol version: it is what the CLI and the helper must agree on.

**Locks**: there is one executor, so jobs run in order, and a **per-(volume, target) guard** means the same copy never runs twice at once.
The scheduler skips anything queued or running. A triggered job for something already running is queued behind it, or refused with
"already running since T", chosen by the trigger's flag (default: refuse). Ingress and the status publisher are never behind the executor.
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

**What the scheduler reads:** the policy on each volume, found by listing volumes (see [the policy on the volume](#the-policy-on-the-volume)). Each volume is read **one at a time**: a policy that does not
parse, that carries a protocol the helper does not speak, or that sits in a pool that cannot be listed, is reported in the status document and skipped, and does not stop the others. Only `apply`
changes what the **scheduler** runs. (The first design kept synced stacks here instead; see [how the design changed](#how-the-design-changed).)

### 4. Handing a run to the helper (not built)

**Where it stands.** A `tink backup run` started on any machine runs **where it is started**, and a copy to another server is relayed through that machine ([remote.md](remote.md)). From a laptop
that means a volume's data passes through the laptop, which is the reason the helper exists. Handing the run to the helper, and following it, is the part that is **not built**.

**Why it is much smaller than it was designed to be.** An Incus `exec` is **killed when its control or output websocket drops**, and tink's exec helper uses exactly that mode and buffers all output until
the end **[code]**, so a laptop that sleeps would kill a multi-hour copy mid-flight: the hand-off must not hold a run open, and that has not changed. What has changed is everything about the *stack*. The first
design bundled the stack and every file it read, because the helper had no other way to know what to copy. The helper now reads each volume's **policy** (#23), so a hand-off carries **no stack at all**
(the bundle was deleted in #41). It is one job:

1. **find** the helper (`user.tink.helper`, any project) and check, from its [status document](#the-status-document), that it speaks the job protocol, is not draining and is not down;
2. **enqueue** a `backup-run` job in its jobs directory with the instance file API: `request.json`, then `READY` last. The arguments already exist (`volumes`, `due`, `dry_run`); no exec is needed;
3. **follow** (optional): read `status.json` and the bounded `log` through the same API and print them. `Ctrl-C` **detaches**; the job carries on. `tink helper jobs` lists recent jobs, `tink helper log ID`
   follows one, `tink helper cancel ID` creates `cancel`.

**What has to be settled, because the stack is gone:**

- **Which volumes.** `tink backup run -f FILE` selects the volumes the stack declares. The helper does not have the stack, so the hand-off must **name those volumes**, as `project/name` (the engine already
  accepts that form, and a bare name is ambiguous across projects).
- **A policy that has not been applied.** The helper runs the volumes' *policies*, not the YAML. If the stack's declaration differs from what is on the volume, a hand-off would copy something other than what the
  operator is looking at. It should say so and stop ("apply first"), as `plan` would show an `update`.
- **A target the helper cannot reach.** The status document lists the remotes the helper can reach (`remotes`): a hand-off for a copy to another one should refuse early, naming `tink helper remote add`.
- **No helper, or an unwell one.** Run locally exactly as today, with the relay note; `--local` forces it.
- **Two runs of the same copy.** The per-copy guard is per process, so a run handed to the helper and one the helper's scheduler queued are in the same process and the guard applies; a *local* run beside the
  helper's is a different process and nothing stops both (wasteful, not unsafe: each makes its own restore point). The default for a hand-off of something already running should be to refuse, saying since when.

No daemon API of its own is added: the interface is **Incus's file API plus a versioned job-directory protocol**, with Incus as the transport and the access control, and only the file API and reads of instance
config are needed, **no exec**. (The earlier rationale for having no daemon API, that tink owns no state, no longer holds in full, because the helper owns its job directory; this is the real one.)

### 5. Lifecycle commands

**Built** (#26, #27, #29, #32): `tink helper install | upgrade | remove | status | remote add|list|remove`; **not built**: `jobs | log | cancel` (phase 4). [`helper.md`](helper.md) has the flags and the
behaviour; what follows is the reasoning.

- **install**: creates the project, instance, volumes and devices (a NIC, the loopback API proxy, the config and data volumes; idempotent), sets `TZ`, records the image fingerprint, starts it, and
  **enrols it** ([the credential](#the-credential)). It refuses, before creating anything, if the host's API is not listening or if a helper already exists on that server in *any* project (discovery is by
  `user.tink.helper`). `--reissue` enrols it again with a fresh key pair and removes the old certificate. `--binary` and `--ingress` are described in `helper.md`.
- **upgrade**: **drains first**, then replaces the helper. A `DRAIN` file on the data volume (put there through the instance file API) tells it to queue and start nothing while a running job finishes; the
  instance is recreated with the **same configuration and devices, keeping its volumes**, so its certificate stays valid and nothing is enrolled again. After `--drain-timeout` it lifts the drain and stops
  rather than interrupting the job; `--force` goes on and the job is marked `failed (interrupted)`. Incus's own `rebuild` is non-atomic, needs a stopped instance and does not refresh `oci.*` keys **[code]**,
  which is why upgrade recreates the instance. **[verified]** with a 2 GB copy in flight, and, once an image existed, from the `v0.1.0` image to `v0.1.1` keeping the certificate and the job history.
- **Version rule:** the CLI and the helper agree on the **protocol versions** (jobs, copy policy, status), not the exact tink version. An exact-version rule would force a helper restart for every laptop
  `go install`, lock two operators with different versions out of each other, and could not work before a version was compiled in.
- **status**: reads the instance and its [status document](#the-status-document) and judges it **healthy, degraded or down** (exit 0, 1, 2 with `--check`); `--json` gives everything. It is what cron, a monitor or
  anything else polls.
- **remove**: revokes the certificate, then stops and deletes the instance; the volumes (job history; the client configuration, which holds the keys of any remote backup target) stay unless `--purge`.

## The credential

**Built** (#26, #27, #29; `docs/helper.md` is the how-to). The helper authenticates to Incus with **its own client certificate**, trusted by the host, instead of the host's root socket.

**What it is.** A TLS client certificate, named `tink-helper` in the host's trust store, generated by the helper itself. It is **unrestricted**: the helper must reach
every project that holds volumes it copies, and a restricted certificate is not a boundary on Incus 7.5.1 in any case (see [security](#security)).

**Enrolment.** `tink helper install` starts the instance, mints a single-use trust token on the host (`incus config trust add`), and runs the enrolment **inside the
container with the token on standard input**, so the token is never on a command line or written to disk:

```
incus config trust add tink-helper --quiet | incus exec <helper> -- tink remote add host https://127.0.0.1:8443 --token-file -
```

`tink remote add` generates the key pair **inside the container**, where the private key (mode 0600) stays in `tink-helper-config` and never travels; verifies the server by
the fingerprint the token carries before anything secret is sent; pins it; and redeems the token. **[verified]** on the lab host, from a container with the loopback proxy.
This is phase 1's `tink remote add`, unchanged. The helper's entrypoint then runs `tink daemon run --remote host ...`; until the remote exists it waits and retries. Two things the build found that this text did not: install reads the token from the token operation and **never waits on it** (the operation does not end until the token is used, and waiting hung for hours), and `remote add` had to learn to replace an existing remote so that `--reissue` works.

**The three properties, and the one that is not claimed.**

| Property | Claimed? | How, and what was checked |
|---|---|---|
| **Revocable** | yes | `incus config trust remove` ends its access at once; the container keeps running and its next call fails. No shared secret to rotate. **[verified]** |
| **Auditable** | yes, for what it does to data | What the helper does to your data (a volume update such as a copy stamp, a snapshot create and delete, an instance update) reaches Incus as `tls` with the certificate's fingerprint as the username, where the root socket is `unix` / `root`, indistinguishable from you at a terminal; the trust store names the fingerprint. **One exception, measured on 7.5.1:** the lifecycle event for an instance **PATCH**, which is how the status document is written, carries no requestor at all, so the helper's status writes are not attributed. **[verified]** |
| **Confined** | **no** | The certificate is unrestricted, so it has the reach of the socket: it can add trust, change server config, and exec in any instance. A compromised helper is a compromised host. |

**Re-issue.** `tink helper install --reissue` removes the old trust entry and enrols a fresh key pair. Run it if the config volume might have leaked, or after a revocation
you want to undo.

**Fallback: the socket. Not built.** A host with no HTTPS listener could run the helper on the unix-socket proxy as phase 0 did ([constraints](#2-the-helper-instance)); it would work, and it
gives up the first two properties. `install` refuses such a host instead.

**Still not settled** (the build did not need either). How long the certificate is valid and how it is renewed (the server advertises `certificate_self_renewal`, **[hypothesis]**, not read); whether the host's
API address is stable enough to pin in the proxy device, or `install` should record it and `upgrade` refresh it. A revoked helper is reported **down at once**: `tink helper status` also reads the trust store when it may.

## The status document

**Built** (#26; the shape is `internal/helper/status.go`). One JSON document, `user.tink.helper.status`, on the helper's own instance config. It replaces the per-tick
`user.tink.helper.tick` of earlier revisions, and it is the one place the helper says what it is doing.

**What is in it.** `proto`; the helper's tink `version`; the job `protocol` and the **policy protocols it can read**; `tz`; `started`; `tick` (the last heartbeat, RFC 3339);
the `running_jobs`, `queued_jobs` and `draining` counts; the `remotes` it can reach; the volumes it has **skipped, with the reason** (a policy of a protocol it does not speak, one that does not parse, a pool it cannot list); the copies that are **failing**
(volume, target, consecutive count, since when); the last job's id, state and finish time; and the ingress reconcile's last result. **It holds no error text**: errors from
Incus and its drivers can echo credentials (the TrueNAS API key appeared in one), and a config value is the wrong place for them. Reasons are short, fixed phrases; the
detail stays in the job's `log`.

**How it is written.** With a **PATCH of that one key** (`PATCH /1.0/instances/<name>`), not a read-modify-write of the whole instance. A whole-instance PUT needs the etag,
conflicts with an operator's concurrent edit and retries, and could drop a field a newer server added if the helper's client library is older than the server. A PATCH
merges only the key it names, and answers synchronously (there is no operation to wait on). Neither side lost an edit in the test ([findings](#credential-and-status-document-findings)).

**When.** **On change**, and a **heartbeat every 10 minutes** (configurable). Every write costs one `instance-updated` lifecycle event and about 90 ms of `incusd` CPU, so
writing every minute would put 1,440 events a day in the log of anything listening and writing on change plus a slow heartbeat puts in about 150. A reader treats the
heartbeat as **stale after 2.5 intervals**. The slow heartbeat is enough because the real dead-man's switch for backups is on the volumes: a copy that stops showing up
makes `plan` say it is overdue, whatever the helper does or does not say.

**Why instance config, not a file.** Measured on the lab host:

- an instance **file** push *and every file read* each emit a lifecycle event, so every `plan` would leave one in the log; a **config read emits none**;
- both can be read while the instance is **stopped**, which is the case that matters;
- the file is cheaper to write (about 2.5 ms against 21 ms) and is no cheaper in events, which is the cost that shows;
- it needs no extra mechanism to read: `GetInstance`, over the same connection `plan` already has.

**Who reads it.**

- **`tink plan`** finds the helper (by `user.tink.helper`) and ends with a note when it is degraded or down. It also reads the policy protocols
  the helper understands, so **`apply` refuses to write a policy of a newer protocol than the helper can read** instead of silently stopping that volume's copies. That check is latent (only protocol 1 exists) and is proven by a test that writes a protocol 2.
- **`tink helper status`** shows all of it; **`tink helper status --check`** is the exit-code form ([lifecycle](#5-lifecycle-commands)).
- **Alerts**, below.

**One rule the build added.** The helper publishes **nothing until its scheduler has looked at the volumes once**, so a helper that cannot reach Incus never publishes a clean bill, and it retries a failed first pass after 5 seconds (the proxy comes up about a second after the process).

**What it is not.** It is not the record of what has been copied: that is the stamps on the volumes, true when the helper is gone. It is not secret and not a log. If it
is missing, the helper is old, new or absent, and `plan` says which it can tell.

## Alerting

Nothing sends a notification, and nothing in the lab receives one, so this is designed in layers, and **only the first is built**.

1. **Pull (built, #26).** `tink helper status --check` and `plan` read the status document and the volume stamps. It needs no notification code in tink: a cron job, a
   monitor or Home Assistant can poll the exit code. It is the one layer that works with nothing configured.
2. **Dead-man ping (optional, not designed in detail).** The helper requests a URL after each healthy heartbeat, and a service that alerts when the requests stop. This is
   the only layer that notices the helper, the host or the network going away, because every other layer is reported *by* the thing that died. One setting, a URL; the URL
   is a secret of the same kind as any other.
3. **Push on change (not planned).** A webhook when a copy starts or stops failing. Worth building only once something is there to receive it.

An Incus lifecycle event is emitted when the helper instance stops; whether that is enough for an existing logging target to alert on **has not been checked**.

## Time zones

Cron schedules are evaluated in the location of `now`. `helper install` sets `environment.TZ` explicitly (default: the host's), `status` shows it, and the helper evaluates schedules in
that zone so they line up with Incus's own snapshot schedules. The binary embeds the zone database (`time/tzdata`), so an image with no zoneinfo still honours it. Due-ness is stamp-based, so a
DST change cannot cause a repeat or a skip, only shift the next run by an hour. **[hypothesis]** (robfig cron's handling of a non-existent local time is not read.)

**Not built:** this design said `plan` would evaluate in the helper's zone when it finds a helper. It does not; `plan` uses local time, so a laptop in another zone can show a different
"next run" than the helper will keep. Nothing is lost (the stamps decide what is due), but the displayed time can be wrong, and the status document carries the zone for when it is wanted.

## The policy on the volume

**Built and merged (#23).** Before it, the copy policy existed in one place, the stack YAML, and the volume carried only what Incus enforces
(`snapshots.schedule`, `snapshots.expiry`) and tink's stamps and markers (`user.tink.backup.*`). That forced the helper to hold a copy of every stack,
which can drift from the repository and which `plan` cannot see, and it leaves `incus storage volume show` telling only part of the story.

**The key.** `apply` writes one tink-owned key on each volume that has a `backup:` block with copies or verification:

```
user.tink.backup.policy = {proto: 1, copies: [...], verify: {...}}     # one document, YAML or JSON, versioned
```

It is **resolved**: each copy carries its target inline (`location`, `engine`, `remote`, `pool`) rather than naming a `kind: backup-target` the
scheduler would have to find. The stack's `backup-target` resources remain the way to *write* the intent; the volume holds what was applied. One key,
not a dozen scalars, so it is applied and compared as a unit, and an older helper can refuse a newer `proto` instead of misreading it. The snapshot
`schedule` and `retain` stay on Incus's own keys, as today.

**Discovery.** The scheduler lists custom volumes (every project the helper can see, every pool) and reads the key, the way the ingress reconcile
reads `user.ingress.*` on instances. **[code]** for the pattern (`internal/ingress`); that a helper can list volumes across projects is **[verified]**
on the lab host (see [what the discovery test found](#what-the-discovery-test-found)). A volume with no key has no scheduled copies. The listing skips any volume that carries a restore-point or
in-progress marker (`copy-of`, `copy-partial-of`), so a backup is never scheduled for a backup.

**Drift becomes a `plan` update.** `plan` compares the key's content with what the YAML would write, exactly as it already does for the two
snapshot keys, and shows `update` when they differ. There is no second copy of the intent for it to miss. A change to the YAML takes effect when it is
**applied**, which is stricter than a sync but is how every other tink resource already behaves.

**Removal.** The doc for volume backups says tink never removes keys it no longer sets. This key is the exception: it is owned outright, so removing a
`backup:` block, switching to `none:`, or removing every copy makes `apply` **delete** it. Otherwise the scheduler would keep copying a volume the
operator opted out of. A volume deleted from the YAML but left on the server keeps its key, and the scheduler keeps copying it: `plan` ends with a note about it, and
`tink backup forget` lets it go (see [open questions](#open-questions)).

**Copies carry config; the policy does not travel. Built.** A restore point, a restored volume and a verify scratch volume are made by copying a snapshot, and a copy carries the
volume's `user.*` config, so the policy would have come along and a backup would have been scheduled for a backup. the policy is now removed in three places (`internal/volbackup`): a **restore point** loses it when its markers are written (`ensureMarkers`, in `copy.go`), a volume made from a restore point loses it with the `copy-` markers
(`scrubMarkers`), and a volume restored from a snapshot loses it (`scrubPolicy`). The scheduler's marker check above is the second line of defence. The `verified-*` and per-target `copy.<target>.*` stamps are a smaller instance of the same question; they were not re-examined for this revision.

**Size.** **[verified]** on Incus 7.5.1 (the lab host): a 64 KiB `user.*` value on a custom volume is accepted. A policy with several copies and a
verify check is a few hundred bytes. Whether Incus caps a value above 64 KiB was not established (the larger probes failed in the client's shell
argument limit, not in Incus).

**What it does not change.** `restore` and `verify` still take their target from the stack (`--from TARGET` names a `kind: backup-target`), because
restoring is for the case where the source volume, and so its key, is gone. Failure stamps, markers, the server marker and the sweep are unchanged.

## What the discovery test found

Run on the lab host (Incus 7.5.1, two storage pools, one of them TrueNAS-backed) with the helper's own client library (`GetStoragePoolVolumesAllProjects`, Incus
client v7.4.0 from `go.mod`) and throwaway projects, volumes, a container and a trust entry, all deleted afterwards.

| Question | Result |
|---|---|
| Can one call list the custom volumes of every project in a pool, with their `user.*` config? | **Yes.** One call per pool returns all projects, with the full config, so no per-volume fetch is needed. **[verified]** |
| Does the project the client is scoped to matter? | **No.** A client scoped to a project with no volumes of its own got the same list. **[verified]** |
| Same volume name in two projects? | Distinguishable by the `project` field. **[verified]** |
| Does it work from inside a container, through the proxy-device socket, as the helper would run? | **Yes**, same result. **[verified]** |
| Cost | 46 volumes (all types) across 8 projects in about 70 ms on the default pool, 10 ms on the TrueNAS pool. One call per pool per tick is cheap here; larger hosts not measured. **[verified]** |
| Is a restore point's inherited policy visible, so it can be skipped? | **Yes**: a volume with both the policy key and `user.tink.backup.copy-of` was listed with both. The key is carried by a copy unless scrubbed, as [the policy on the volume](#the-policy-on-the-volume) says. **[verified]** |
| A 64 KiB `user.*` value on a volume | Accepted. **[verified]** |
| A certificate **restricted to one project**, listing all projects | **Not bounded.** It saw its own project's volumes and also the **default project's** (custom, container, image and VM volumes, with config) on both pools, but **not** another throwaway project's. A direct `GET` of a default-project custom volume returned its full config; a `PUT` to it was refused (`User does not have permission for project "default"`). The same certificate's direct list of the default project returned nothing. **[verified]**, cause not established. |

**What the last row does and does not show.** The lab host has **no authorization scriptlet and no other authorizer configured**, so the leak is not the
`authorization.star` problem described under [Security](#security); it is Incus's own behaviour on 7.5.1 with a plain restricted certificate: reads of the
default project are possible through `all-projects` and through a direct `GET`, while writes are refused. It was read-only, and the data read was
configuration, not volume contents. It is a reason to treat a restricted helper identity as an open question, not as a boundary, and to re-test on each Incus version.

## Credential and status-document findings

Run on the lab host (Incus 7.5.1, Debian 13) in throwaway projects and containers, with a probe built for the purpose; everything, including the trust entries, was deleted
afterwards.

**The status document: instance config or a file.** 150 writes at 5 a second to a running OCI app container, an idle 33-second baseline for comparison (20 ms of `incusd`
CPU), then the same with an operator editing the instance's config at the same time.

| | Config, whole-instance PUT | Config, key-only PATCH | File in the container |
|---|---|---|---|
| Write, p50 / p95 | 21 / 24 ms | 15 / 17 ms | 2.4 / 3.2 ms |
| `incusd` CPU per write | ~95 ms | ~89 ms | ~78 ms |
| Lifecycle events per write | 1 (`instance-updated`) | 1 | 1 (`instance-file-pushed`) |
| Read, p50, and events per read | 1.6 ms, **none** | 1.6 ms, **none** | 2.8 ms, **1** (`instance-file-retrieved`) |
| Database growth | raft segment rollovers of 8 MiB, about 8 to 15 KB per update | the same | none |
| With an operator editing concurrently | 24 of 150 writes hit a 412 and were retried; the operator's 174 edits were all kept | no conflicts; the operator's 172 edits were all kept | n/a |
| Read while the instance is stopped | yes | yes | yes (and the file survived a stop and start) |

Conclusions: config wins, because its reads are silent and a file's are not; PATCH is better than PUT because it cannot conflict and cannot drop fields; and at the cadence
chosen (on change plus every 10 minutes) the cost is about 150 events and about 15 seconds of `incusd` CPU a day. The PATCH probe first reported "not found" on every write
because it waited for an operation: an instance PATCH answers synchronously. The writes had landed (the document read back intact and Incus logged them); the fault was the probe's.

**The helper's own certificate.** From a throwaway OCI app container on the lab host, with the tink binary mounted in.

| Question | Result |
|---|---|
| Can a container reach the host's API through a TCP proxy to loopback? | **Yes, with a NIC.** With no network attached the container's loopback is down and the listener fails with `Network unreachable`; attaching the bridge fixed it. **[verified]** |
| Can the helper enrol from inside, with the token on standard input? | **Yes.** `tink remote add host https://127.0.0.1:8443 --token-file -` generated the key pair in the container (`client.key` mode 0600), verified the server by the token's fingerprint, redeemed the token, and the host's trust store gained the entry. **[verified]** |
| Does the host attribute its requests to the certificate? | **Yes.** A volume created through the certificate produced `storage-volume-created` with `requestor = {protocol: tls, address: 127.0.0.1, username: <the certificate's fingerprint>}`; the same action through the root socket produced `{protocol: unix, address: @, username: root}`. The fingerprint matches the trust-store entry. **[verified]** |
| Does revocation work? | **Yes.** After `incus config trust remove`, the container's next call was refused; the container itself kept running. **[verified]** |
| Anything else turned up? | **A separate defect:** after revocation, `tink plan` printed `would create` for a volume that exists. Every planner treats *any* failed read as "does not exist". It does not affect the helper's own volume listing, which returns errors as errors, but it makes `plan` misleading over a revoked, expired or dropped remote, and for a `kind: file` with `restart: true` it could cause a needless re-push and restart. Filed as its own task. |

## Backup engine changes this design needed

Small, separate from the helper, and prerequisites for phase 2. **All three are built** (#18 to #22):

1. **Failure stamps and backoff** (above).
2. **Per-(volume, target) guard** inside the process.
3. **Server identity in the restore-point marker.** `copy-of` is `<project>/<pool>/<volume>`, so two servers copying a same-named
   volume into one remote pool were treated as one volume, and the newest won, which could **prune the other server's restore points**.
   `user.tink.backup.copy-server=<server_name>` fixes it: **prune only touches points from this server**; restore considers all, shows the
   server, and defaults to the newest overall unless told otherwise. A rebuilt host with the same name continues its history; one with a
   new name still **finds** every point (only pruning is scoped), so the goal "a rebuilt host must still find its backups" holds.
   Existing restore points without the marker are treated as this server's.

## Security

**What is claimed.** The helper's credential is **revocable** and **auditable** ([the credential](#the-credential)). It is **not confined**, and nothing here promises that it is.

**As built.** The helper holds an **unrestricted client certificate** of the host's Incus, so it has the reach of root on the host: it can add trust, change server config, and
exec in any instance. That is no worse than `tink daemon` as root today, and no worse than the root socket it replaces; what changes is that the access can be ended and
attributed. Consequences the design takes seriously:

- **Who can reach the helper is who holds host root**: anyone who can push files into it, exec in it, or read its `tink-helper-config` volume (which holds the
  key), including a restricted or OIDC user granted access. It is in its own project for that reason; the docs say so.
- **The helper never runs a stack.** It holds no stack at all (the stack store was removed, #41), so there is no `kind: incus`, no `apply` and no shell from a stack in it.
  `backup verify`'s check command comes from a volume's policy and runs only inside its throwaway sandbox instance (no network, volume mounted read-only), never in the helper
  or on the host. A future job that would run stack commands in the helper needs its own design.
- **Supply chain**: the host's incusd pulls the image as root and the container then enrols itself with a certificate the host trusts. The instance records the image's **fingerprint** at
  `install` and `upgrade`, and the image is published from this repository's workflow to `ghcr.io/minihci/tink-helper`, which is the publisher that is trusted. (The base image is pinned by digest in the Containerfile.) Whoever can change what runs in the helper can act as it.
- **Secrets**: resolved `${secret:}` values live in instance config, which the helper can read. No backup job resolves secrets today. A job that
  does gets the identity deliberately and says so. The policy key is plaintext volume config, so it can never carry a secret; the status document is plaintext
  instance config and holds none either. A target that needs one (a restic repository password, say) must name it and have the helper resolve it, which is the
  design that job needs anyway.
- **Who can write a policy.** The verify check is an image and a command, and the helper reads it from volume config. Anyone who can
  write a custom volume's config can therefore choose what runs in the verify sandbox, and choose where a volume is copied to. They already hold
  the Incus API, which is root on the host, so this adds little, but it is stated. The command still runs only in the throwaway
  instance (no network, volume read-only), never in the helper.
- **Failure text can contain credentials** (an Incus error echoed an API key). Stamps and the status document never carry it; logs are redacted best-effort.
- **A revoked or stolen certificate.** Revoking it makes the helper's calls fail; it keeps running, logs the refusal, stops updating its status, and goes stale to
  `plan` and `status --check`. If the key may have leaked, revoke and `install --reissue`. Requests made with the key are attributed to its fingerprint, and the proxy
  makes the legitimate ones arrive from `127.0.0.1`, so one from another address stands out. Someone on the host can still use it from the host.

**Why no confinement is claimed.** A restricted certificate would be the way to confine the helper, and on Incus 7.5.1 it does not deliver one:

- **On a host configured by `tink deploy`, it is not bounded.** `configs/daemon/authorization.star` returns `True` for every TLS client, and an Incus
  *restricted* certificate is bounded only if the server's authorization lets Incus's own check run (a server routing the decision through a scriptlet
  that returns `True` gives it everything, **[verified]** on a lab VPS). The comment at the top of that file says the opposite and should be corrected.
- **On the lab host, with no scriptlet at all, it was bounded only for writes and for other projects' volumes**, see
  [what the discovery test found](#what-the-discovery-test-found). So a scriptlet is not the only way a restricted certificate leaks.
- **The helper needs every project that holds volumes it copies**, so the most a restricted certificate could do is narrow "the host" to "the projects named", which is
  not a boundary worth promising.

If that changes, the helper's certificate could be restricted without a redesign, because it is already its own. Re-test on each Incus version first.

## Phase 3 as built

Phase 3 is built and merged in slices (#25 to #31), then extended (#32 to #34, #38, #40, #53, #55) and released as v0.1.0 to v0.1.4; `docs/helper.md` describes what exists and is the thing to read for how to use it. What follows is only where the build **differs from, or settles, what this document said**:

| Design said | As built |
|---|---|
| The helper reaches the host through a TCP proxy and is enrolled with a token | Same, but the **NIC is required**, not optional: an OCI app container with no network has its loopback down, so the proxy cannot listen. Enrolment is `tink remote add` **run inside the instance with the token on standard input** (never on a command line or disk); install reads the token from the token operation and **never waits on it** (the operation does not end until the token is used: waiting hung for hours) and cancels it afterwards. |
| A revoked helper goes stale after 2.5 heartbeats | `tink helper status` also checks the **trust store** (when it may read it): a helper whose certificate is gone is **down at once**. |
| The helper publishes a status document | It publishes **nothing until its scheduler has looked at the volumes once** (a helper that cannot reach Incus must not publish a clean bill), and retries a failed first pass in **5 seconds** (the proxy comes up about a second after the process, and the pass races its own enrolment). The document also carries `running_jobs`, `queued_jobs` and `draining`. |
| `upgrade` drains first | A **`DRAIN` file on the data volume** (put there through the instance file API): the scheduler queues nothing, the executor starts nothing, a running job finishes. A draining helper shows as degraded. An image upgrade recreates the instance with the **same config and devices and keeps its volumes**, so the certificate stays valid and there is no new enrolment. Verified live with a 2 GB copy in flight, and from the published `v0.1.0` image to `v0.1.1` keeping the certificate and the job history. |
| The ingress half needs the `ingress-routes` volume as a disk device | **No mount at all.** Volumes are per project and the helper has a project of its own, so the reconcile has an **API mode**: it reads and writes `/etc/caddy/routes/generated/` inside the ingress instance through its file API and reloads Caddy there. Off by default; the host path in production is unchanged. `deploy` does not install the host's `tink-daemon` when a helper has `user.tink.helper.ingress`. |
| `plan` reads the helper | It ends with a note when the helper is degraded or down, and **`apply` refuses to write a copy policy of a newer protocol than the helper reads**. Latent today (only protocol 1 exists): it is proven by a test writing a protocol 2. |
| Audit: the helper's requests are attributed | True for what it does to data; **not** for its status writes (a PATCH event has no requestor). See [the credential](#the-credential). |
| The image is pulled by the host (skopeo for anything tink pushes) | tink pulls OCI images with an **in-process registry client**, with `credentials_helper` for private registries; no `skopeo` is needed. |
| The helper image is a v1 prerequisite | Published: `ghcr.io/minihci/tink-helper:<tag>` (multi-arch) by the workflow on a tag, plus release binaries; `install --binary` is the way for a host with no registry access. |

Found by running it live, every one of them invisible to a test that used a fake which answers instantly: the token operation that never ends; a root disk read as a managed volume named `""`; `remote add` refusing to replace an existing remote on re-enrolment; a restarting helper publishing "healthy" before it had looked at anything.

**Not done:** the socket fallback (described, not built); a host reboot or an Incus daemon restart with the helper running; a copy by the helper to a second physical server; whether a *remote* target applies the in-progress mark to a relayed copy; phases 4 and 5.

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
2f. **The policy on the volume. Built, merged (#23) and validated in the lab, before phase 3.** `apply` writes `user.tink.backup.policy`, `plan` compares it, the
   scheduler lists volumes instead of reading stacks, restore points and restored volumes are scrubbed of it, removal clears it. The stack store is **removed
   outright, with no deprecation period**, since nothing deploys it (it had only been exercised while building the feature): `daemon sync`, `daemon run --stacks`,
   `daemon enqueue --stack`, `jobs.Stacks` and the request's `stack` field are gone, and a job with no bundle works from the volumes. *Checked on the lab host
   (Incus 7.5.1, a throwaway project and a throwaway `dir` pool):* a stack applied once is picked up by a daemon started with only `--jobs`, which queued one job
   and made both copies; editing the YAML, or the key by hand, shows an `update` in `plan` until applied; the restore points, a volume restored from a
   snapshot and one restored from a target carry no policy and are not scheduled; opting a volume out, or removing its block, removes the key (and the daemon logs
   the recovery); a policy of another `proto`, or one that does not parse, is logged once by `project/name` and skipped while the other volumes still run; later
   ticks queue nothing while no copy is due.
3. **The helper. Built, merged and released (v0.1.0 to v0.1.4), running on the lab host** (see [Phase 3 as built](#phase-3-as-built)). Containerfile and image workflow (version injection, multi-arch, digest), `tink helper install|upgrade|status|remove`
   (no `sync`), and the pieces added by the credential and status designs: **the helper's own certificate** (the loopback API proxy, a NIC, enrolment from inside the container with
   the token on standard input, `install --reissue`), **`daemon run` accepting its own host as a remote** (it used to refuse a remote, because its ingress half read a host
   path), **the status document** (published by PATCH, on change plus a heartbeat) and **`status --check`**, `plan` reading the status and `apply` refusing a policy the
   helper cannot read, the ingress half over the instance file API, `deploy` no longer reinstalling the host daemon. *Done when:* killing the process brings it
   back, a crash loop is detected by `status` and `plan`, **revoking the certificate stops the helper and `status --check` says it is stale**, a host reboot brings it back
   with the next due copy still running, and `upgrade` waits for a running copy. *Met, except the host reboot, which has not been tried.*
4. **Hand a run to the helper. Redesigned, not built.** With no bundle (the stack is no longer sent), this is: find the helper, enqueue one `backup-run` job through the file API, follow
   or detach, and `tink helper jobs|log|cancel` ([section 4](#4-handing-a-run-to-the-helper-not-built)). `--local` forces a local run. *Done when:* a laptop that sleeps mid-copy
   does not interrupt it, and no volume data passes through the laptop.
5. **Retire `daemon install`. Not built.** Deprecated with a notice, kept for a release, then removed; migration is `tink helper install` plus removing the
   unit. `deploy` already skips the host daemon when the helper carries the ingress (#31); what is left is the notice and the removal. *Done when:* a host runs only the helper.

## Open questions

- **Can the helper list volumes in every project it needs?** **Yes, answered on the lab host**; see
  [what the discovery test found](#what-the-discovery-test-found). The helper's certificate is unrestricted, so this holds for it.
- **A volume removed from the YAML but still on the server. Answered (built, #23).** Its key persists and the scheduler keeps copying it, by design: tink
  never removes what it is no longer told about, and cannot tell a volume dropped from this stack from another stack's. Two additions make that visible and fixable. A stack
  can name itself (`kind: stack`), and `apply` stamps each volume with `user.tink.stack`, so the volume points back at the stack to edit and the stack can find its own
  volumes exactly. `plan` and `plan apply` end with a note listing volumes that carry a policy the stack does not declare: certain when they carry this stack's name, a guess
  (in the stack's projects and pools) when they carry none, never when they carry another's. `tink backup forget` clears the policy and nothing else. Checked on the lab host
  with two stacks sharing a project.
- **Per-volume cost of discovery.** Listing every custom volume every tick is cheap on one server; on a large one it may want a longer interval than
  the due check, or a cached listing refreshed on `apply`. Not measured.
- **Does a copy to a remote target apply config at creation?** Unchanged from phase 2e, and now it matters twice: for the in-progress mark and for
  whether the policy key is already scrubbed on a relayed restore point.
- **A host with no registry access. Answered (built).** `install --binary` takes a tink binary (the release assets are published) and builds the instance from it.
- **The helper's own project. Answered (built).** `tink-helper`, with the volumes `tink-helper-config` and `tink-helper-data`. Its reach across projects comes from the certificate, so the project is a naming and access question, not a reach one.
- **Alerting.** Layered, and only the first layer is built ([alerting](#alerting)). Whether an Incus lifecycle event for the helper instance stopping is enough for an
  existing logging target has not been checked, and nothing in the lab receives notifications yet.
- **The certificate's lifetime and renewal**, and whether the host's API address is stable enough to put in the proxy device ([the credential](#the-credential)).
- **A host with no HTTPS listener** is refused by `install` today; the socket fallback would give up revocation and attribution. Whether `install` should offer to turn the listener on, on the
  loopback address only, is open.
- **Status cadence.** 10 minutes and stale after 2.5 intervals are a starting point, chosen from the measured cost (one event and about 90 ms per write) and from copies being
  overdue-checked on their own. The ingress reconcile has no stamps, so a dead helper is only noticed there by the heartbeat; whether 25 minutes is too long is a call to make.
- **Finding the helper from `plan`.** Built (`internal/helper/find.go`): by `user.tink.helper`, across projects.
- **Do the reads of a helper's status create events elsewhere?** Reading instance config emitted none on the lab host; a different Incus version or an authorization
  scriptlet is not covered.
- **Server name as identity.** `server_name` is the host name by default; hosts that share a name would collide. Whether the Incus server UUID is
  available and stable enough is still to be checked.
- **What a hand-off should do in the cases listed in [section 4](#4-handing-a-run-to-the-helper-not-built)** (a policy not applied, a target the helper cannot reach, no healthy helper, a copy already running) is proposed there and open until phase 4 is built.
- **Several helpers or a cluster.** One helper per Incus server is enforced at `install`. Incus clustering is not considered.

## Phase 0 findings

These are about the **unix-socket proxy**, which was the fallback and is **not built**; the helper uses [the credential](#the-credential).

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

Some rows below describe stack sync, bundles and `daemon enqueue --stack`, which were built for phase 2 and **removed in #23 and #41**. They are kept as evidence for the job directory, the executor and the backoff, which remain.

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
| A laptop exec is killed when its websocket drops (and tink's exec buffers everything). | The trigger became **detached**: enqueue by file API, follow, detach. No exec (the bundle part was dropped later, #41). |
| A failed copy is retried every tick; no failure is recorded anywhere. | **Failure stamps and backoff**, as a phase 2 prerequisite; stamps hold no message. |
| One lock would stall ingress behind a long copy. | **Independent workers**, a per-copy guard, ingress never behind the executor. |
| `upgrade` or a restart kills a running copy; "exactly its own version" forces restarts and locks operators out. | **Drain before upgrade**; the rule is the **protocol version**. |
| The trigger persisted the laptop's stack into the scheduler. | *Superseded.* A trigger used its own bundle, and only `helper sync` changed the scheduler. Now there are no stacks in the helper: only `apply` does. |
| `kind: incus` and the other shell-outs are not API calls; `deploy` reinstalls the daemon. | `kind: incus` **refused under `--remote`**; `deploy` host-local and no longer reinstalls the daemon once a helper exists. |
| Time zones differ between laptop and helper. | **`TZ` set explicitly**, schedules evaluated in the server's zone, shown in `status`. |
| Two servers can prune each other's restore points. | **Server marker**; prune only touches this server's points. |
| Failure visibility was weaker than described; autorestart gives up after 10 per minute. | **Heartbeat** on instance config, shown by `plan` and `status`; an entrypoint that never exits on a connect failure. |
| Stacks: a bad stack, filename collisions, files outside the `-f` files, non-atomic file push. | *Superseded.* One directory per stack, loaded one at a time, activated by `READY` last. Replaced by reading each volume's policy one at a time. |
| Ingress path was a long pole in phase 5. | *Superseded.* A disk device was the plan; the build uses the ingress instance's file API instead (see [Phase 3 as built](#phase-3-as-built)). |
| Phase 0 gated phase 3 but did not exist; phase 1's done-when passed by grep; phase 5 hid the real work. | A **phase 0 spike**; done-whens that run commands from a laptop; the ingress and `deploy` work moved up. |
| Security items the doc did not state (stack is code, who can reach the helper, traversal, supply chain, secrets, the config volume). | Stated under [Security](#security), with cheap mitigations built in (own project, image fingerprint, the helper holds no stack). |
| The helper's credential was the host's root socket: not revocable, not attributable, and every request looked like the operator's. | **Its own certificate** (revision 4): revocable and auditable, and not claimed to be confined. |
| A heartbeat key written every tick says only that the helper ticked, and costs an event a minute; the file alternative emits an event on every read. | **A status document** (revision 4): on change plus a slow heartbeat, written by PATCH, readable when the helper is stopped. |
| The helper adds the first state tink owns, which weakened the "no daemon API" rationale. | The doc says so, and gives the real rationale: the interface is Incus's file API plus a versioned job protocol. |

## Alternatives considered

- **Mount the ingress volume into the helper.** The first plan for the ingress half. Volumes are per project and the helper has a project of its own, so it could not be mounted; the reconcile uses the ingress instance's file API instead.
- **Send a bundle with each hand-off (the first trigger design).** A hand-off carried the stack and every file it read, because the helper did not know what to copy. Once the policy was on the volume
  the bundle carried nothing the helper needed, and it was removed with the stack store (#41).
- **Keep the root socket (revisions 1 to 3).** The simplest thing, and it works. Rejected as the default because it cannot be revoked short of removing the device and its
  requests cannot be told from yours. Described as the fallback for a host with no HTTPS listener, and not built.
- **A restricted certificate for the helper.** Would narrow the reach, but it needs every project the helper copies, and on Incus 7.5.1 a restricted certificate is not a
  boundary (on a `tink deploy` host because of the scriptlet; on the lab host with none, it still reads the default project). Not claimed; revisit per Incus version.
- **The status as a file in the helper.** Cheaper to write, and no cheaper in events: every read emits one too, so every `plan` would leave a line in the log.
- **A heartbeat key written every minute.** One event a minute, 1,440 a day, for a fact the volume stamps already carry for copies. Replaced by a status document written on
  change plus a heartbeat every 10 minutes.

- **Keep synced stacks (revision 2, built).** Works, and a stack is the whole truth. Rejected as the long-term shape: it is a second copy of the
  intent that `plan` cannot compare, it can lag the repository silently, a lost helper means re-syncing everything, and `incus storage volume show`
  does not show the policy. Removed outright, not deprecated: nothing deploys it.
- **One scalar key per setting** (`user.tink.backup.copy.nas.schedule`, ...). Easy to read one by one, but not applied or compared atomically, awkward
  for a list of copies, and nothing to version. Rejected for one versioned document.
- **Name the targets and keep them in the stack.** A volume would carry only `copies: [{target: nas}]`. Then the scheduler still needs the stack to
  resolve `nas`, which is the thing being removed. Rejected: targets are resolved into the key.

- **Keep `daemon install` (init-system units).** Works, but needs a generator per init system and ties tink to the host.
- **cron or a timer calling `tink backup run --due`.** Simplest, and `--due` was designed for it; it stays a supported way to run the same command. It
  lacks the failure backoff, the heartbeat and the helper's other benefits unless phase 2's engine changes are in.
- **A trigger by `exec`.** The first revision. Rejected: the server kills the command when the connection drops.
- **A tink daemon with its own API** (a Docker-style client and server). Rejected: Incus's file API plus a versioned job directory already provides
  transport and access control, with nothing new to authenticate or listen on.
- **The helper in its own repository.** Rejected: tightly bound to tink; two versions to keep in step.
