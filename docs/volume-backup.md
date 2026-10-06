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

## Copies and 3-2-1 (declared, not yet run)

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

**What is not real yet.** This is the declaration and the check only. **Nothing executes `copies`**: tink
has no copy engine yet, so `plan` adds a warning to every volume with copies saying so, and a passing
3-2-1 check means "the declaration is sound", not "the data is there". `verify` is likewise only parsed.
The runner, restore and verify are the next slices (see `volume-backup-design.md`).

Like the missing-`backup:` warning, the 3-2-1 warning is meant to become an error later.

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
- **No restore command.** Restore is
  `incus storage volume snapshot restore <pool> <volume> <snapshot>`.
- **Unproven restores.** Nothing yet verifies that a snapshot is usable.
