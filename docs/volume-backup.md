# Volume backup

Every `kind: storage-volume` should answer one question in its YAML: **how is
this backed up?** The point is to ask the question while writing the stack, not
at restore time.

**Status: a volume that doesn't answer gets a warning, not an error.** That keeps
stacks written before this field applying unchanged. The intent is to promote it
to BLOCKED (the way image drift is) once the feature has matured.

This is **tier 1**: local, Incus-native snapshots. See [Not covered](#not-covered)
for what that does and doesn't protect against.

## Declaring it

Scheduled snapshots:

```yaml
kind: storage-volume
name: immich-library
backup:
  snapshots:
    schedule: "0 3 * * *"   # cron (5 fields) or @hourly/@daily/@midnight/@weekly/@monthly/@annually/@yearly
    retain: 14d             # Incus expiry syntax: "14d", "1w 3d", "6m"; units S M H d w m y
```

Or an explicit opt-out, with the reason (required, so a skipped backup is a
visible decision rather than an omission):

```yaml
kind: storage-volume
name: immich-model-cache
backup:
  none: regenerable -- models re-download on first use
```

`snapshots` and `none` are mutually exclusive, and one of them is required
*inside* a `backup:` block. Both `schedule` and `retain` are required:
a schedule with no expiry fills the pool forever, and Incus reads a zero expiry
as "never expires", so `0d` is rejected too. Malformed blocks fail at load time.

## Copies and 3-2-1

Snapshots on the live pool are a rollback aid, not a backup: they die with the disk. A real backup
is **3-2-1**: three copies of the data (the live volume plus two), in at least two distinct failure
domains, at least one of them off-site. You declare where copies go as `kind: backup-target`
resources, and each volume says which targets it copies to:

```yaml
kind: backup-target
name: macpro
location: other-host        # same-host | other-host | offsite -- your claim; tink cannot verify it
engine: incus               # the only engine so far: replicate with Incus itself
remote: macpro              # an Incus remote (another server)...
---
kind: backup-target
name: nas
location: other-host
engine: incus
pool: nas                   # ...or another storage pool on this server (e.g. the truenas driver)
---
kind: storage-volume
name: immich-library
backup:
  snapshots: {schedule: "0 3 * * *", retain: 14d}    # rollback aid, does not count as a copy
  copies:
    - {target: macpro, schedule: "0 4 * * *", retain: 30d}
    - {target: nas,    schedule: "0 5 * * *", retain: 30d}
  verify: weekly            # daily | weekly | monthly; parsed and validated, not acted on yet
```

`copies` can be used with or without `snapshots`; `none` excludes all of them. A target needs `remote`
(another Incus server) or `pool` (another pool on this server), and may name both.

**What `plan` says.** For each volume that is not `none`, `plan` counts the copies and warns when 3-2-1
is not met, naming the missing leg:

```
warning: 3-2-1 not met (2 of 3 copies; snapshots on the live volume's pool do not count). Missing: 1 more copy in another failure domain; an off-site copy (no copy's target is declared location: offsite)
```

A *failure domain* is the live volume's pool, or a target's remote server and pool, or its other local
pool. A copy on the live volume's own pool is flagged and not counted. Two copies on the same remote and
pool count as one domain. A reference to a `target` that does not exist is a **hard error** at load time,
because a typo would otherwise silently drop a backup leg.

The 3-2-1 check judges the *declaration*. Whether copies are actually happening is a separate signal: see
[Running the copies](#running-the-copies) below, and the "has never run" / "is overdue" warnings.

Like the missing-`backup:` warning, the 3-2-1 warning is meant to become an error later.

## Running the copies

```
tink backup run [VOLUME...] [--due] [--dry-run] [-f FILE]
```

For each volume that declares `copies` (or only the named ones), and each of its targets, `run`:

1. takes a **snapshot** of the volume: a consistent point in time (`tink-copy-<UTC time>`, with a 24h expiry as a safety
   net, and removed again when the copy is done);
2. copies **that snapshot** into a **new volume** on the target pool, `VOLUME-bk-<UTC time>`: a *restore point*;
3. stamps the source volume (`user.tink.backup.copy.<target>.at` and `.volume`);
4. **prunes** that volume's restore points older than the copy's `retain`, always keeping the newest.

A target is either another **storage pool on this server** (another disk, or the Incus `truenas` driver) or an **Incus remote**
(another server). If a copy fails, the others still run and the exit status is non-zero.

### Remote targets

`remote:` is the name `incus remote add` gave the other server. Tink does **not** store credentials or addresses: it opens that
remote the way the `incus` command does, from the Incus client configuration of the user running tink (`~/.config/incus`, or
`$INCUS_CONF`; under `sudo` that is *root's*, so add the remote as root). The remote's **project** is the one configured for the
remote (`incus remote add NAME URL --project tink-backup`), and its pool is `pool:` on the target, default `default`.

```
incus remote add homelabvps https://127.0.0.1:18444 --project tink-backup   # as the user that runs tink
```

- **The data is relayed through tink.** Neither server has to reach the other, only the machine running tink has to reach the
  remote, so an SSH tunnel is enough (`ssh -N -L 18444:<remote's Incus address>:9443 host`, with the remote added at `127.0.0.1:18444`).
  Pull mode needs the far server to dial back to this one and push mode needs this one to reach the far one's own advertised
  address; both break behind NAT or through a tunnel, and a restricted project refuses pull anyway.
- **A cut-off copy is never a backup.** The restore point carries its markers only once the copy has *completed*. A copy that dies
  part way (the tunnel drops, the disk fills) is deleted; if even that fails, the error names the volume to delete by hand, and
  because it has no markers tink will never list, prune, restore or verify from it.
- **Several source servers can share a target.** Each restore point records the server it was made on (`user.tink.backup.copy-server`, the
  server's name: its host name unless configured otherwise), and **pruning only ever removes points the pruning server made** (points from
  before this marker existed count as that server's own). Two servers copying a volume of the same name into one remote pool therefore cannot
  delete each other's backups, and `backup run` says when it sees another server's points and leaves them alone. Restore still sees **all**
  of them, so a rebuilt host, under whatever name, finds its predecessor's backups; it says when the point it used was made by another server.
  Separate projects are still tidier, but no longer needed for safety.
- **Restore and verify `--from` a remote** pull the restore point back through tink the same way, and need nothing but the remote.
- **Trust scoped to a project may not be enforced.** An Incus *restricted* client certificate is limited to its projects only if the
  server's authorization lets Incus's own check run. A server that routes `authorization.client.tls-restricted` through a custom
  scriptlet that returns `True` (as an `incus-ui` setup might) gives that certificate full access. Check what the remote lets the
  certificate see (`incus project list REMOTE:`) before relying on it.

**Why a new volume each time, and not one target volume refreshed with `incus storage volume copy --refresh`?** Because a refresh
makes the target *mirror* the source's snapshots. Tested on two TrueNAS-backed pools: when the source pruned a snapshot, the next
refresh deleted it from the target as well (even with `--refresh-exclude-older`), and refreshing from a snapshot deleted the target's
own snapshots. A mirror cannot keep a longer history than its source, and, worse, it **propagates a deletion or damage on the source into
the backup**. With one independent volume per run, nothing that happens to the source can reach an existing restore point. The price
is a **full copy per run** (space and time proportional to the volume); incremental transfer is future work.

**What tink will and won't delete.** Restore points carry markers (`user.tink.backup.copy-of`, `-at`, `-target`, `-server`) naming
exactly the volume (project, pool and name) they back up and the server it lived on. Pruning and restoring consider **only** volumes with
the marker for the volume in question: a volume tink did not make, a lookalike name, another volume's restore point, or one whose marker it
cannot read is never touched. **Pruning goes further: only points made by the server doing the pruning**, and "never the newest" means the
newest of that server's own.

**One copy at a time.** Within a process, the same copy (the same volume to the same target) never runs twice at once: the second is
refused as "already running" and is not counted as a failed copy. This guard is per process: a `tink backup run` on a laptop and a scheduled
run on the server are different processes, and nothing yet stops both copying the same volume at the same moment (it is wasteful, not unsafe:
each makes its own restore point).

**When a copy fails.** The source volume is marked with when the last attempt failed and how many in a row
(`user.tink.backup.copy.<target>.fail.at` and `.fail.n`); a success removes both. **No reason is stored**: errors from Incus and its
drivers can echo credentials, and a volume's config is the wrong place for that, so the reason is only in the output of the run that
failed. `plan` says the copy is failing (or "has never succeeded"). With `--due`, a failing copy is **not retried every time the command
is called**: it waits `5 minutes x 2^(failures-1)`, but never longer than the copy's own schedule interval (an hourly copy retries at
least hourly), and says so:

```
lib -> dead: backing off after 2 failed attempt(s), next try after 2026-10-07 00:56 MDT
lib -> nas: not yet due, next at 2026-10-07 01:00 MDT
```

A run you ask for by name (`tink backup run lib`) ignores the backoff: you asking is not a scheduler retrying. A copy that succeeded but
could not prune older restore points is **not** a failed copy (the restore point exists and the source is stamped); its error says so and
the copy is not put into backoff.

**`--due`** runs only the copies whose `schedule` has come round since their last success (by the stamp), so cron or a timer can call
`tink backup run --due` every few minutes. **Tink does not schedule copies itself yet**, so until something calls it, `plan` warns
that a copy "has never run" or "is overdue" (the schedule's next time after the last success plus a grace of a quarter of the
interval, between 10 minutes and 6 hours). **`--dry-run`** says what would happen and changes nothing.

## Restoring from a target

```
tink backup restore VOLUME --from TARGET [--snapshot STAMP]
tink backup verify  VOLUME --from TARGET [--snapshot STAMP]
```

`--from` names a `kind: backup-target` in the stack; `--snapshot` picks a restore point (its volume name, or the timestamp in it),
default the newest. Restore makes a new local volume (scrubbed of the copy markers, so it can never be mistaken for a restore point);
verify restores to a scratch volume and runs the declared check as before, and stamps `verified-from` with the target's name.

**The source volume does not have to exist.** Restoring from a target is for the case where it is gone, so neither command looks for
it. If it is gone, `verify` still runs the check but cannot record the result, and says "NOT recorded".

## Restore and verify

A backup you have never restored is a hope. Two commands, local snapshots only for now (restoring from
a backup *target* needs the copy engine, a later slice; `--from` says so rather than pretending):

```
tink backup restore VOLUME [--snapshot S] [--as NAME]
tink backup verify  VOLUME [--snapshot S]
```

Both read the volume's pool, project and verify settings from the stack file (`-f FILE`, default
`./tink.yaml` if it exists); `--pool` and `--project` override or stand in for it. `--snapshot` defaults
to the most recent.

**Restore** copies a snapshot to a **new** volume, `VOLUME-restore-<UTC time>` unless `--as` is given. On
btrfs and ZFS that is a copy-on-write clone: fast, and it takes almost no space. It **never** restores in
place and refuses to overwrite any existing volume: replacing live data is the one destructive step in the
whole feature, so swapping the restored volume in (attaching it, or repointing the instance's disk device)
is left to you.

**Verify** proves a backup is usable:

1. restore the snapshot to a scratch volume,
2. run the declared check against it in a throwaway instance, with the volume mounted **read-only**,
3. delete the instance and the scratch volume (on every path, including failure; if cleanup itself fails the
   error names what to delete by hand),
4. if it all passed, stamp the source volume.

```yaml
backup:
  snapshots: {schedule: "0 3 * * *", retain: 14d}
  verify:
    every: weekly                        # daily | weekly | monthly; omit to verify only on demand
    check:
      image: docker-oci:library/alpine:3 # an OCI image that has `sleep`
      command: [sh, -c, "test -s /data/important.db"]   # argv; exit 0 means the data is good
      mount: /data                       # where the restored volume appears (default /data)
```

`verify: weekly` on its own is still accepted. The throwaway instance has no network and its entrypoint is
replaced by a `sleep` so it can be exec'd into, which is why the image must be an OCI image with `sleep`.

**The stamp.** A passing verify records `user.tink.backup.verified-at` (UTC), `verified-snapshot` and
`verified-with` (`check` or `restore`) on the source volume, as ordinary Incus volume config. There is no
state file of tink's own; `plan` reads these. A failed check leaves the stamp untouched, so a failed
verification never looks fresh.

**What `plan` says.** Only when a `verify` cadence is declared and the volume exists:

```
warning: verify: weekly is declared but this volume has never been verified -- run `tink backup verify lib`
warning: last verified 12d ago, older than the declared verify: weekly -- run `tink backup verify lib`
warning: a verify check is declared, but the last verification only proved the snapshot restores -- ...
```

The last one closes a hole: running `verify` somewhere the stack file is not found would otherwise
restore-only and make the volume look freshly verified while the check that matters never ran.

**What it does and does not prove.** With a check, it proves the snapshot restores *and* that your check
passes against the restored data, which is only as good as the check. Without one it proves only that the
snapshot can be restored to a volume, and says so in its output. Neither proves the application would
start on it; a check that does the real thing (e.g. `pg_controldata`, or opening the database) is the way
to get closer. Verification reads a crash-consistent snapshot, like everything here.

## What tink does with it

`schedule` and `retain` map one-to-one onto Incus's own volume keys
`snapshots.schedule` and `snapshots.expiry`. Tink only converges that config;
Incus takes and prunes the snapshots. There is no tink daemon in the loop and
no state of its own, consistent with the rest of `plan`.

| Situation | `plan` says |
|---|---|
| No `backup:` block | converged exactly as before, plus a warning -- for existing volumes too, since the one that predates the field most needs the question asked |
| Snapshot policy, volume absent | create, with the config |
| Snapshot policy, config differs live | update |
| `none`, volume absent or present | nothing to do |
| `none`, but the live volume still has `snapshots.schedule` | nothing to do, plus a warning: tink never removes keys it no longer sets |

Storage volumes used to be create-only; they are now updatable, but only for
these two keys. Other live config on the volume is left alone.

### When the warning becomes an error

Promoting it means returning BLOCKED from `decideVolume` instead of a warning, and
bringing back one piece of apply logic that was deliberately left out while nothing
can block: `apply` must also skip any instance that mounts a BLOCKED volume (and
whatever is inside it). Otherwise the instance's disk device auto-creates the
missing volume via `run.ApplyConfig` and routes around the check.

## Interaction with other features

- **`on_image_change: rebuild` / `snapshot_volumes`**: unaffected. Rebuild's
  `tink-pre-rebuild-*` snapshots are taken in addition. Whether the volume's
  `snapshots.expiry` also reaps those has not been checked.
- **Instance-level `snapshots.*` config** is still rejected with `rebuild`
  (unchanged); volume snapshots never block a rebuild. This feature is
  volume-level for exactly that reason.

## Not covered

The direction for everything below is in [`volume-backup-design.md`](volume-backup-design.md).

- **It is one disk.** Snapshots live in the same pool as the volume, so they
  guard against a bad upgrade or a deleted file, not a failed disk or a lost
  host. Off-site copies (tier 2) are not implemented.
- **Crash-consistent only.** Snapshots are atomic per volume but know nothing
  about the application. A database volume snapshotted live restores as if
  power was cut -- fine for Postgres (it replays WAL), not a substitute for a
  logical dump. Nothing coordinates *two* volumes: a volume pair snapshotted on
  the same schedule is not captured at the same instant.
- **A volume that lives on a TrueNAS pool cannot restore its own snapshots on stock TrueNAS 25.10** (an upstream middleware bug, fixed
  in truenas/middleware#19962 and #19963): the same-pool clone Incus uses fails. Copies to and from TrueNAS pools are not affected.
- **Each run is a full copy**, over the network for a remote target; there is no incremental transfer yet.
- **Nothing schedules copies**: call `tink backup run --due` from cron or a timer.
- **A restore point is crash-consistent**, like the snapshot it is copied from.
- **Verify is only as strong as its check**, and an unchecked verify only proves the snapshot restores.
