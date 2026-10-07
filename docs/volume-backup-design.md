# Volume backup: design

**Status: design, not implemented.** [`volume-backup.md`](volume-backup.md) describes
what exists today (tier 1: scheduled local snapshots, and a warning when a volume
declares nothing). This document is where that goes next: a real 3-2-1 story, with
restore as a first-class half of it.

Throughout, claims are marked **[verified]** (tried live on a real host while writing
this), **[docs]** (read in upstream documentation, not tried), or **[hypothesis]**
(believed, must be checked before relying on it).

## Scope

**In scope for this design:** things Incus itself already does, driven by tink.
Replicating a volume to another Incus server, or to another storage pool on the same
server (including the `truenas` driver), and restoring from either.

**Designed but sequenced later:** an off-site engine for hosts with no second Incus
box to talk to (the "ephemeral restic job", see [below](#the-ephemeral-job-engine)).
The mechanism was prototyped, and its findings are recorded here so the Incus-native
path is shaped to leave room for it.

**Non-goals:** tink does not become a backup engine. No dedup, no encryption, no
storage format, no repository of its own. It declares the strategy, orchestrates
tools that already exist, and verifies that restores work.

## Vocabulary

- **Failure domain**: a set of things that die together. A pool, a disk, a host, a
  site. The point of a second copy is that it is in a different one.
- **Rollback aid**: a snapshot on the same pool as its volume. It protects against
  mistakes (bad upgrade, deleted file). It dies with the disk, so it is **not** a copy
  for 3-2-1 purposes. Tier 1 as shipped is a rollback aid.
- **Copy**: a full, independent, restorable replica of the volume's data in a
  different failure domain than the live volume.

## 3-2-1, concretely

3 copies of the data, on 2 different kinds of media, 1 of them off-site. Tink needs
this to be checkable from YAML, so it fixes a definition:

| Rule | Counted as |
|---|---|
| 3 copies | live volume + 2 *copies* (rollback aids don't count) |
| 2 media | the copies are in at least 2 distinct failure domains, neither being the live volume's pool |
| 1 off-site | at least one copy's target is declared `location: offsite` |

"Media" is deliberately interpreted as failure domain, not literal medium: a second
host's disk and a NAS are different media in the sense the rule is protecting against.
Tink can only check what the YAML declares; it cannot know that `location: offsite`
is true. The declaration is the user's claim.

## Declaring it

Targets are their own resource, since several volumes share one:

```yaml
kind: backup-target
name: macpro
location: other-host          # same-host | other-host | offsite
engine: incus                 # phase 1 supports only this
remote: macpro                # an Incus remote already configured on this host
pool: default                 # pool on the target; defaults to the remote's default
---
kind: backup-target
name: nas
location: other-host
engine: incus
pool: truenas                 # a LOCAL pool using the truenas driver; remote omitted
---
kind: storage-volume
name: immich-library
backup:
  snapshots: { schedule: "0 3 * * *", retain: 14d }     # tier 1, unchanged
  copies:
    - { target: macpro, schedule: "0 4 * * *", retain: 30d }
    - { target: nas,    schedule: "0 5 * * *", retain: 30d }
  verify: weekly
```

- `remote` set: the target is another Incus server. `remote` omitted with `pool` set:
  the target is another pool on this server. One of the two is required for
  `engine: incus`.
- `snapshots`, `copies` and `none` compose: `none` stays exclusive; `snapshots` +
  `copies` is the normal shape.
- `verify` is a cadence. See [Verify](#verify).
- 3-2-1 is the default evaluation for every volume that is not `none`. The evaluator
  needs no engine at all and is the first thing to ship (see [Phasing](#phasing)).

What `plan` says, for a volume that falls short:

```
storage-volume/immich-library: no changes
    warning: 3-2-1 not met (1 of 3 copies): snapshots share the live volume's failure
             domain. Missing: a copy in a second failure domain; an off-site copy.
```

Like the missing-`backup:` warning, this graduates to an error later, deliberately
after the feature has been lived with.

## Incus-native copies (phase 1 engine)

Incus can already replicate a custom volume, including its snapshots, to another
server or pool **[docs]**:

```
incus storage volume copy <src-pool>/<vol> <remote>:<dst-pool>/<vol> --refresh
```

`--refresh` updates an existing copy instead of failing, and `--refresh-exclude-older`
skips source snapshots older than the newest on the target **[docs]**. Incus's own docs
describe copying to a separate network-connected Incus server as the high-reliability
option for custom volumes **[docs]**.

Why this is the right first engine:

- Tink already speaks the Incus API; no new dependency, no new binary.
- Restore is the same operation in reverse, so restore and backup cannot drift apart.
- With the same storage driver on both ends (e.g. ZFS to ZFS), Incus can likely use
  optimized, incremental transfers, and across drivers fall back to a slower generic
  copy **[hypothesis: seen only in search results about ZFS refresh behaviour; neither
  path's cost has been measured here]**.

### Targets that are in scope

| Target | How | Status |
|---|---|---|
| Another Incus host on the LAN (e.g. the Mac Pro 5,1) | an Incus remote | [hypothesis] untested; needs the second host up and trusted |
| A VPS running Incus | an Incus remote over the internet (an SSH tunnel to its API works) | [verified] copy to and from a `dir`-pool VPS over a two-hop SSH tunnel, relay mode, round-tripped byte for byte; see step 3b below. Bandwidth, not mechanics, is the concern |
| A TrueNAS box | a *local* pool using Incus's `truenas` driver, copy across pools | [verified] against stock TrueNAS SCALE 25.10.7 and 25.04.2.6; see below, including what did **not** work |

**TrueNAS.** Tron's Incus (7.2) lists the `truenas` driver (v0.7.7) as supported
**[verified]**. Per upstream it is block-based: each Incus volume becomes a ZFS volume
on the NAS, reached over iSCSI **[docs]**; a NAS-backed pool is a different failure
domain from Tron's own disk, but **not** off-site unless the NAS is.

Tested against a TrueNAS SCALE 25.10.7 VM on Tron (stable at the time; 26 was beta),
first on Incus 7.2 and then on 7.5.1 (the newest stable; 7.4 builds had already been
removed from the package repo), with the Incus pool created by `incus storage create nas truenas source=tank/incus ...`:

| Question | Result |
|---|---|
| Does a cross-driver copy work (btrfs pool to `truenas` pool), snapshots included? | **Yes [verified].** 20 MB volume + 2 snapshots in ~3 s; both snapshots arrived with their original timestamps. |
| Is the data faithful? | **Yes [verified].** Identical sha256, uid/gid, symlink; the NAS volume is an ext4 filesystem on a ZFS zvol over iSCSI. |
| Does `copy --refresh` update an existing copy? | **Yes [verified].** A new snapshot and +20 MB of new data arrived with identical hashes in under a second. Whether it transfers only the delta (vs everything) is **unmeasured**: the volumes were too small to tell. |
| Does restore from the NAS work (NAS snapshot to a new local volume)? | **Yes [verified].** Correct point-in-time contents (a file added after the snapshot was absent), identical hash, ~4 s. |
| Do the snapshots exist on the NAS itself? | **Yes [verified].** As real ZFS snapshots (`tank/incus/custom/<project>_<vol>@snapshot-<name>`), so the NAS's own tooling can see them. |
| Does tier-1 (`snapshots.schedule`/`expiry`) work on a NAS-backed volume? | **Yes [verified].** Scheduled snapshots appeared with the expected expiry. |
| Can a **snapshot be cloned** on the NAS pool (needed for the ephemeral job engine's read-only clone)? | **No, through Incus [verified on 7.2 and 7.5.1].** With the bundled `truenas_incus_ctl` 0.7.7 (still the newest release) and TrueNAS 25.10.7, the default ZFS-clone path fails with `[EINVAL] properties.managedby ... cannot be inherited` (7.5.1 also names `properties.comments`). With `truenas.clone_copy=false` the fallback `replication start` call is rejected by the tool's own argument parser (it prints its usage). **TrueNAS itself can clone:** calling `truenas_incus_ctl snapshot clone <ds>@<snap> <dest>` directly produces a correct clone with the snapshot as its origin. The failure is the *next* step: right after a successful clone the driver runs `truenas_incus_ctl dataset update --user-props=incus:content_type=<type> <clone>` (to re-add a property clones don't keep), and TrueNAS 25.10.7 rejects it with `properties.comments/managedby: Property does not exist and cannot be inherited`; Incus then rolls the clone back, so nothing is left behind. Replayed by hand with only the ctl tool on an Incus-made volume, the same `--user-props` update fails on the clone **and on the original volume**, even when `--managedby`/`--comments` are passed explicitly, while `dataset update --comments x` on the same dataset succeeds. **It is a TrueNAS-version regression [verified].** Same host, same Incus 7.5.1, same `truenas_incus_ctl` 0.7.7, same script, two NAS VMs side by side: on **TrueNAS 25.04.2.6** the snapshot clone through Incus succeeds (~1.5 s) and the bare `dataset update --user-props=...` call succeeds on both the clone and the original; on **25.10.7** the clone fails and that same call fails on both.

**Root cause, found in TrueNAS's source (`truenas/middleware`) [verified by reading both releases and by a runtime check].** The trigger is not the clone and not really the ctl: it is TrueNAS's `pool.dataset.update` mishandling `user_properties` on any dataset that carries TrueNAS-internal properties (every Incus-made volume does, via `--managedby`/`--comments`):

1. When `user_properties` is sent on an update, TrueNAS treats it as the *complete* desired set and appends `{key, remove: true}` for every property currently on the dataset that is not in the list (`plugins/pool_/dataset.py`, identical in 25.04.2.6 and 25.10.7). `remove` becomes `{source: INHERIT}` on that key.
2. What counts as "currently on the dataset" is `pool.dataset.query`'s `user_properties`, which is supposed to hide TrueNAS-internal properties. On 25.04.2.6 it does: it returned only `['incus:content_type']` for an Incus-made volume.
3. 25.10 rewrote the query (`plugins/pool_/dataset_query_utils.py`, commit `65b75f946`, "Use truenas_pylibzfs in pool.dataset.query", present in the first 25.10 release `TS-25.10.0`). It first **renames** internal properties to bare names (`org.truenas:managedby` to `managedby`, `org.freenas:description` to `comments`, ...) and **then** filters out "internal" properties by comparing against the *original* namespaced names, so the filter matches nothing. It returned `['comments', 'incus:content_type', 'managedby']` for the same volume.
4. The update therefore asks to `INHERIT` the bare keys `managedby` and `comments`, which ZFS does not have (it only knows the namespaced names), and `zfs_.dataset.update_zfs_object_props` rejects them with `properties.<key>: Property does not exist and cannot be inherited`.

This explains every observation: it fails on originals as well as clones, it fails whether or not `--managedby`/`--comments` are passed, `dataset update --comments x` works (it does not send `user_properties`), and 25.04 is unaffected. The rename-before-filter code is **identical on `master` (checked 2026-10-06)** and on the `stable/goldeye` branch, so as far as the source shows it is unfixed. A fix would be to filter before renaming, or to filter against both name sets; it has not been tried against a patched TrueNAS. No bisecting is needed: the rewrite is in 25.10.0, so every 25.10.x is affected.

What this means for the design: **the replicate and restore legs work on a NAS pool
today; the job engine's clone-then-attach step does not**, so on a NAS pool the engine
would have to read from a *local* clone and push to the NAS, not the other way round.
That fits the direction anyway (copy first, engine later). It was re-checked on Incus
7.5.1 and still fails, so treat NAS-pool cloning as unavailable until the driver or the
ctl tool changes.

Setup facts that a real deployment has to account for, all hit while building the test:

- The host needs `open-iscsi` (`iscsiadm`); `truenas_incus_ctl` already ships in the
  Incus package at `/opt/incus/bin`. [verified]
- **TLS:** the driver connects to the TrueNAS API over HTTPS and rejects the NAS's
  default self-signed certificate (no IP SAN). A real setup needs a proper certificate
  and hostname; `truenas.allow_insecure=true` is a test-only workaround. [verified]
- **The NAS needs a static IP.** The iSCSI portal is created listening on the NAS's
  address and TrueNAS refuses an address that only came from DHCP. [verified]
- The iSCSI service must be started and enabled on the NAS
  (`truenas_incus_ctl service start --enable iscsitarget`). [verified]
- **The API key appears in plain text** in Incus's error output and in the process
  arguments of the `truenas_incus_ctl` calls the driver makes. Treat it as a secret
  that leaks to anyone who can read those; scope it narrowly. [verified]

### Who runs it

Incus has a snapshot scheduler (tier 1 relies on it) but no scheduler for copies.
Something has to run `copy --refresh` on `schedule`. The natural home is
`tink daemon`, which already runs a periodic loop for ingress reconciliation; or a
`tink backup run` one-shot driven by cron, like `ingress reconcile` is today. Either
way tink stays stateless: it reads each target's live state to decide what is due, and
records outcomes as `user.tink.backup.*` volume config keys, the same precedent as
`user.ingress.*` (see [Verify](#verify)).

### Consistency

Everything here is **crash-consistent**: an atomic snapshot knows nothing about the
application. A live Postgres restores as if the power was cut, which Postgres is built
to recover from **[docs]**. It is not a substitute for a logical dump where one exists
(Immich writes its own daily). A future `quiesce` hook (pre/post exec, reusing
`kind: exec`) is the way to do better; it is out of scope here.

## Restore and verify

A backup without a rehearsed restore is a hope. These are designed together with the
copies, not after them.

### Restore

```
tink backup restore <volume> [--from <target>] [--snapshot <name>] [--as <new-volume>]
```

- **Never in place by default.** Restore creates a *new* volume
  (`<volume>-restore-<UTC time>` unless `--as` is given). The user swaps it in, or
  repoints the instance's device. Overwriting live data is the one destructive step in
  the whole feature, so it is not the default and not silent.
- With no `--from`, restore uses the volume's own local snapshot (tier 1), which is
  native Incus: `incus storage volume copy default/<vol>/<snap> default/<new>`
  **[verified]**, instant and copy-on-write on btrfs.
- With `--from <target>`, it is the same copy in the other direction.

### Verify

```
tink backup verify <volume> [--from <target>]
```

1. Restore the latest copy to a scratch volume.
2. Optionally run a user-declared check inside a disposable instance with the scratch
   volume mounted read-only (e.g. `pg_controldata`, a file count, a checksum manifest).
3. Delete the scratch volume. Stamp the *source* volume with
   `user.tink.backup.verified-at` / `user.tink.backup.verified-from`.

`plan` then warns when a copy has never been verified, or the stamp is older than the
declared cadence (`verify: weekly`). That is the whole value of the feature: "untested
backup" becomes a visible state of the stack instead of a discovery during an outage.

The check step is the reason the ephemeral-instance mechanism below matters even for
the Incus-native path.

## The ephemeral job engine

This is the off-site engine, sequenced after the Incus-native work, and also the
mechanism `verify` wants. It was prototyped on Tron (Incus 7.2, btrfs pool) with the
real `restic/restic` image; findings below are the prototype's.

### The idea

Tink's world is Incus OCI app containers. A backup tool is just another OCI image, so
instead of requiring restic installed on every host, tink runs it as a short-lived
container whose only job is one backup, restore, or check:

```
volume ──snapshot──▶ read-only clone ──mounted ro──▶ [ restic container ] ──▶ repo
 (live)   (atomic)    (CoW, instant)                   (ephemeral, no NIC if the
                                                       repo is a local volume)
```

Per job, tink:

1. Takes (or picks) a snapshot of the volume.
2. Makes a temporary volume as a copy-on-write clone of that snapshot.
3. Creates an instance from the pinned restic image, with the clone attached
   **read-only** at `/data`, plus the repository (a volume, or a NIC and
   credentials for a remote repo) and an explicit `RESTIC_*` environment.
4. Runs the command and gets its exit code.
5. Always deletes the instance and the clone, including on failure.

What this buys, compared with a host-installed tool:

- **Tink stays one binary.** The restic version is a digest-pinned image reference in
  YAML, the same as every other image, and upgrades the same way.
- **The read-only attach is enforced by the kernel**, not by the tool behaving.
- **The source is never touched**: the job reads a clone of a snapshot, so a backup
  neither stalls the live workload nor sees it mid-write.
- **Backup, restore, verify and `check` are one mechanism** with different argv.
- It is the same pattern as `kind: exec`, which already runs commands in OCI
  containers via the Incus exec API.

### What the prototype verified

All **[verified]** on Tron, in a throwaway Incus project, since deleted:

| Question | Result |
|---|---|
| Can a snapshot be attached directly as a disk device (`source=<vol>/<snap>`, `readonly=true`)? | **No, by design.** For a custom volume, `source=<vol>/<x>` means *subpath `x` inside the live volume*, not snapshot `x`. The container saw an empty directory, and Incus created an empty `<x>` directory inside the **live** volume as a side effect. Reproduced identically on btrfs and on the `truenas` pool, which rules out a driver quirk. **Never use it for backups.** |
| Does a copy-on-write clone of the snapshot work instead? | Yes. `storage volume copy <vol>/<snap> <clone>` is instant, the clone mounts read-only (`touch` fails with EROFS), and it holds the point-in-time state (a file written after the snapshot is absent). |
| Does restic back up a clone and restore it to a *fresh volume* faithfully? | Yes: identical sha256, uid/gid 1000 preserved, symlink and timestamps preserved. |
| Do exit codes reach the caller? | Yes via exec: success was 0, a bad `restore` returned 1. |
| Does a repeat backup of unchanged data cost anything? | `Added to the repository: 0 B`. |
| Does `restic check` pass on the result? | Yes: "no errors were found". |
| Does a job run with no network? | Yes, with a local repo volume. A remote repo needs a NIC; unaddressed. |

### Gotchas it already found

- **Pin `--host`.** restic finds the parent snapshot by hostname, and the container's
  hostname is the instance name. The second backup recorded host `job` instead of
  `tron`, so it would not have been treated as incremental of the first on a real
  repo. Tink must pass `--host <volume>` (or similar) explicitly and never depend on
  the container's hostname.
- **Identity goes in tags.** `volume=<name>` and `snapshot=<name>` as restic tags let
  `restore`/`verify` find the right snapshot without tink keeping any state.
- **Job runs via exec, not the entrypoint.** The prototype set the container's
  entrypoint to `sleep` and used exec for the real commands, because that path returns
  exit codes and streams output. Whether an OCI container's own entrypoint exit code
  is observable is **untested**; exec is the proven route.

### Not yet answered

- Credentials for off-site repos (restic password, S3 keys): the secrets problem
  tink does not have an answer to yet. Likely its own small design.
- How `retain:` maps onto `restic forget --keep-*`, which is count/calendar-based
  rather than a single age. Probably a separate `keep:` block for this engine.
- Orphan cleanup if tink dies mid-job: a `user.tink.job=true` marker on the instance
  and clone, plus a reaper that deletes ones older than a bound.
- Which Incus project the job and clone live in, and whether concurrent jobs on one
  volume must serialise.

## Findings that shape everything else

1. Tier 1's behaviour is verified end to end (snapshot taken by the Incus scheduler
   from tink-set config, correct expiry).
2. The `truenas` driver works as a copy and restore target on a current Incus
   **[verified]**; snapshot cloning on it does not yet.
3. A disk device cannot attach a snapshot: `source=<vol>/<x>` is a subpath of the live
   volume (and silently creates it). Read-only access to a point in time needs a clone
   of the snapshot, which works on btrfs but currently fails on the `truenas` driver
   (see above).

## Phasing

Ordered so each step is useful alone, and the Incus-supported path comes first.

1. **`backup-target` kind, `copies:` declaration, and the 3-2-1 evaluator.** Pure
   plan logic: no engine, no new Incus calls. Delivers the guidance (the warning text
   above) and fixes the YAML shape. **Done** (see `volume-backup.md`): `kind: backup-target`
   (`location`, `engine: incus`, `remote`/`pool`), `copies:` and `verify:` on volumes, a hard error for
   an unknown copy target, and the 3-2-1 warning. Differences from the sketch above: `engine` is limited to
   `incus` and `location` is required; `verify` is parsed but not acted on; a volume with copies also gets a
   "declared only, nothing runs them" warning. Found while testing it live: the CLI plans one dependency
   level at a time, so the planner has to be handed the whole stack's targets, not the level it is on.
2. **Local restore and verify.** `tink backup restore` from tier-1 snapshots into a new
   volume, and `verify` with a scratch volume plus the user's check, with the stamp and
   the stale-verification warning. This exercises the disposable-instance mechanism
   with no new engine, and covers the restore half early. **Done** (see `volume-backup.md`).
   Differences from the design above: `--from <target>` is accepted but rejected until the copy
   engine exists; the check runs in an OCI instance whose entrypoint is replaced by `sleep`, with
   no network, and the restored volume mounted read-only; and the stale-verification warning also
   fires when a check is declared but the last verification was restore-only, because otherwise
   running `verify` without the stack file would refresh the stamp without running the check.
   Verified live: restore gives correct point-in-time contents and refuses to overwrite; a passing
   check stamps the volume; a failing check (run against a snapshot with the critical file
   deleted) leaves the stamp unchanged; nothing is left behind on either path.
3. **`engine: incus` copies.** **Done for pool targets and Incus remotes** (see `volume-backup.md`); a scheduler is not. Differences from the design above, and why:
   - **Not `copy --refresh`.** Two experiments on two TrueNAS-backed pools **[verified]**: (a) refreshing a volume makes the target
     mirror the source's snapshots, so a snapshot the source pruned is deleted from the target at the next refresh (also with
     `--refresh-exclude-older`), and target snapshots carry no expiry; (b) refreshing *from a snapshot* copies the right,
     consistent content but deletes the target's own snapshots. A mirror cannot keep longer history than its source and propagates
     deletion or damage into the backup. So each run copies a fresh snapshot into a **new, separately named volume** and tink prunes by
     `retain`. Cost: a full copy per run.
   - **Markers** on every restore point (`user.tink.backup.copy-of` = `project/pool/volume`, `-at`, `-target`, `-snapshot`) are what pruning
     and restore trust; a volume without the marker for exactly that volume is never listed, pruned or restored from.
   - **Restoring from a target does not require the source volume**, because that is the case it exists for; `verify` then cannot record
     the result and says so.
   - **No scheduler.** `tink backup run --due` is meant to be called from cron or a timer; the daemon is not wired in. `plan` warns when a
     copy has never run or is overdue, which is what makes the missing scheduler visible instead of silent.
   **TrueNAS pools, stock 25.10.7 [verified]** (a pristine install, none of the middleware fix applied; a control confirmed it still
   fails Incus's same-pool snapshot clone with `properties.managedby: Property does not exist and cannot be inherited`, which a patched
   25.10.7 does not):
   - Everything tink does across pools works: `backup run` from a local pool into the stock TrueNAS pool (including the Incus volume
     UPDATE that sets the restore-point markers), pruning, `restore`/`verify --from` it, the lost-source case, and a source volume that
     *lives* on the stock TrueNAS pool copied out to another pool. The real Immich stack was backed up to it and its DB restore point verified.
     Restoring a restore point *into* a TrueNAS pool works too (a copy from a volume, not from a snapshot).
   - Why: the bug is `pool.dataset.update` failing when sent `user_properties` for a dataset that has `comments`/`managedby`. Incus
     only does that after cloning a snapshot **within one TrueNAS pool**. Tink never does: copies between pools are not clones, and
     `UpdateVolume` touches TrueNAS only for `size`/`truenas.use_refquota`, everything else (including the `user.tink.backup.*`
     markers) lives in Incus's own database (read in the Incus 7.4 driver; confirmed live on 7.5.1).
   - **What does fail on stock 25.10:** the local, tier-1 `tink backup restore VOLUME --snapshot S` of a volume that *lives* on a TrueNAS
     pool, because restoring a snapshot into the same pool is exactly that clone. This is the upstream bug, not tink; the error is
     Incus's, and it includes the TrueNAS API key on the command line, so treat tink's output for that failure as sensitive.
   **3b, remote targets [verified]** on a VPS reached only through an SSH tunnel (Tron -> Mac -> VPS API):
   - Transfer modes with `incus storage volume copy`: **pull** is refused for a restricted project and could not dial back anyway;
     **push** and **relay** both worked in both directions. Tink always uses **relay**: the only machine known to reach both ends is tink.
   - `incus remote add` itself failed every time with `400 Bad Request {}` on its third request (with a token and with an already-trusted
     certificate), while `curl` with the same certificate through the same tunnel, and every later `incus` command, worked. The cause is not
     known; the remote entry was written into the client config by hand, which is all tink needs.
   - The remote server's `authorization.client.tls-restricted: scriptlet` made a *restricted* certificate see every project: scoping a
     certificate to a project is only as good as the server's authorization setting.

4. **Off-site restic job engine**, built on the mechanism from step 2, for hosts with
   no second Incus server.

## Open questions

- **TrueNAS target:** copy, refresh, restore and snapshot preservation work (above), and the
  clone failure has a known cause (TrueNAS 25.10's `pool.dataset.query` leaks internal user
  properties; see the root cause above). Still open: is `--refresh` truly incremental at
  realistic sizes; do copied snapshots carry the source's expiry; will TrueNAS fix the
  query (or Incus work around it); and how does Incus behave at boot or mid-copy when the
  NAS is unreachable (not tested).
- **Remote targets:** *resolved*: tink holds no credentials; it opens the remote from the Incus client configuration of the user
  running it (`incus remote add` did the trust). Still open: where that configuration lives for a daemon, and a way to keep a tunnel up.
- **Retention on a target:** do refreshed copies carry the source's snapshot expiry, or
  need their own `snapshots.expiry` on the target volume?
- **Same-pool clones and quotas/space:** a clone is cheap on btrfs/ZFS, but a `dir`
  pool has no copy-on-write; the job engine must refuse or warn there.
- **Promoting the warnings to errors:** both the missing-block and 3-2-1 warnings are
  meant to graduate. Deciding when is a product call, not a technical one, and needs
  the instance-skip logic described in `volume-backup.md` brought back.
