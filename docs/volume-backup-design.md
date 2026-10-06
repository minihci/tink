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
| A VPS running Incus | an Incus remote over the internet | [hypothesis] this is the off-site case; bandwidth and the API's exposure to the internet are the concerns, not mechanics |
| A TrueNAS box | a *local* pool using Incus's `truenas` driver, copy across pools | [verified] against a TrueNAS SCALE 25.10.7 VM; see below, including what did **not** work |

**TrueNAS.** Tron's Incus (7.2) lists the `truenas` driver (v0.7.7) as supported
**[verified]**. Per upstream it is block-based: each Incus volume becomes a ZFS volume
on the NAS, reached over iSCSI **[docs]**; a NAS-backed pool is a different failure
domain from Tron's own disk, but **not** off-site unless the NAS is.

Tested against a TrueNAS SCALE 25.10.7 VM on Tron (stable at the time; 26 was beta),
with the Incus pool created by `incus storage create nas truenas source=tank/incus ...`:

| Question | Result |
|---|---|
| Does a cross-driver copy work (btrfs pool to `truenas` pool), snapshots included? | **Yes [verified].** 20 MB volume + 2 snapshots in ~3 s; both snapshots arrived with their original timestamps. |
| Is the data faithful? | **Yes [verified].** Identical sha256, uid/gid, symlink; the NAS volume is an ext4 filesystem on a ZFS zvol over iSCSI. |
| Does `copy --refresh` update an existing copy? | **Yes [verified].** A new snapshot and +20 MB of new data arrived with identical hashes in under a second. Whether it transfers only the delta (vs everything) is **unmeasured**: the volumes were too small to tell. |
| Does restore from the NAS work (NAS snapshot to a new local volume)? | **Yes [verified].** Correct point-in-time contents (a file added after the snapshot was absent), identical hash, ~4 s. |
| Do the snapshots exist on the NAS itself? | **Yes [verified].** As real ZFS snapshots (`tank/incus/custom/<project>_<vol>@snapshot-<name>`), so the NAS's own tooling can see them. |
| Does tier-1 (`snapshots.schedule`/`expiry`) work on a NAS-backed volume? | **Yes [verified].** Scheduled snapshots appeared with the expected expiry. |
| Can a **snapshot be cloned** on the NAS pool (needed for the ephemeral job engine's read-only clone)? | **No, on this version combination [verified].** Incus 7.2 + bundled `truenas_incus_ctl` 0.7.7 + TrueNAS 25.10.7: the default ZFS-clone path fails with `[EINVAL] properties.managedby: Property does not exist and cannot be inherited`. With `truenas.clone_copy=false` the fallback `replication start` call is rejected by the tool's own argument parser (it prints its usage). Not yet tried on Incus 7.4. |

What this means for the design: **the replicate and restore legs work on a NAS pool
today; the job engine's clone-then-attach step does not**, so on a NAS pool the engine
would have to read from a *local* clone and push to the NAS, not the other way round.
That fits the direction anyway (copy first, engine later), but it should be re-checked
on a newer Incus before it is relied on.

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
   above) and fixes the YAML shape.
2. **Local restore and verify.** `tink backup restore` from tier-1 snapshots into a new
   volume, and `verify` with a scratch volume plus the user's check, with the stamp and
   the stale-verification warning. This exercises the disposable-instance mechanism
   with no new engine, and covers the restore half early.
3. **`engine: incus` copies.** `copy --refresh` to a remote or a second pool, scheduled
   by the daemon or `tink backup run`; restore and verify `--from` a target.
4. **Off-site restic job engine**, built on the mechanism from step 2, for hosts with
   no second Incus server.

## Open questions

- **TrueNAS target:** copy, refresh, restore and snapshot preservation work (above). Still
  open: is `--refresh` truly incremental at realistic sizes; do copied snapshots carry
  the source's expiry; does Incus 7.4 fix snapshot cloning on the NAS pool; and how does
  Incus behave at boot or mid-copy when the NAS is unreachable (not tested).
- **Remote targets:** how does tink hold the credentials (client cert + trust token)
  for an Incus remote? It already connects to the local socket only.
- **Retention on a target:** do refreshed copies carry the source's snapshot expiry, or
  need their own `snapshots.expiry` on the target volume?
- **Same-pool clones and quotas/space:** a clone is cheap on btrfs/ZFS, but a `dir`
  pool has no copy-on-write; the job engine must refuse or warn there.
- **Promoting the warnings to errors:** both the missing-block and 3-2-1 warnings are
  meant to graduate. Deciding when is a product call, not a technical one, and needs
  the instance-skip logic described in `volume-backup.md` brought back.
