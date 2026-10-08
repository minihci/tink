# Image updates: drift, blocking, and `rebuild`

`tink plan` and `tink plan apply` used to treat an instance's `image:` as
create-only. Change the tag in the YAML and `plan` said "no changes",
`apply` exited 0, and the old image kept running. Worse, if the same edit
also changed `config:`, `apply` wrote the new config onto the old image
and, with `restart: true`, restarted it into that mismatch.

Now `plan` compares the image each instance was built from with the image
its YAML asks for, and each instance says what `apply` may do about a
difference. This document is the user-facing description. For the
reasoning behind the shape of it, see [Why these rules](#why-these-rules).

## What `plan` reports

An instance whose image matches its YAML plans exactly as before. One whose
image does not match is reported as drift, and its disposition depends on
its `on_image_change` policy (below):

```
instance/ha: BLOCKED []
    drift: image: "ghcr.io/home-assistant/home-assistant:2026.9.4" resolves to abc1634ab347, but the instance was built from 7d7a19271ad2 (ghcr.io/home-assistant/home-assistant:2026.9.1)
    blocked: image drift with on_image_change: report -- nothing on this instance is changed (set on_image_change: ignore to accept it, or on_image_change: rebuild to converge it): ...
```

The four dispositions an instance can have are `no changes`, `would update`
(config or devices only), `would REBUILD`, and `BLOCKED`.

**How drift is decided.** The comparison is between *fingerprints*, not
between the text of two image references. For an OCI image Incus derives
the fingerprint from the image's layer digests, so it identifies content,
and it is recorded on the instance as `volatile.base_image`. `plan` asks the
registry what the YAML's reference resolves to now (tink asks the registry
itself, for the architecture the *server* runs, so a laptop gets the server's answer:
[remote.md](remote.md#image-remotes-need-no-setup)) and compares the result. Two references for
the same bytes (`:2` and `:2.1.2-alpine`, or `library/x` and `x`, or a tag
and a digest) are therefore not drift, and a floating tag such as `:stable`
whose content has moved *is*.

One shortcut avoids the lookup: a digest-pinned reference whose digest the
instance already records is a conclusive match.

`--offline` (on both `plan` and `plan apply`) skips every registry lookup.
An image that cannot then be verified is handled by policy: a warning under
`report` and `ignore`, a block under `rebuild`. The same applies when a
lookup fails for any other reason. Local aliases (images imported with
`kind: image`) are compared by alias fingerprint and never need the
registry. Images from simplestreams or other non-OCI remotes are not
compared at all.

## `on_image_change`

An instance-only field. It says what `apply` may do when the instance's
image no longer matches the YAML.

| Value | Confirmed drift | Cannot verify the image |
|---|---|---|
| `report` (default) | The instance is **BLOCKED**. Nothing on it is changed, including config, devices and restarts. | Warning only. The instance is otherwise applied as usual. |
| `ignore` | Shown as a warning. Config and devices still apply. | Warning only. |
| `rebuild` | Converged by rebuilding the root filesystem (next section). | **BLOCKED.** `apply` will not rebuild an image it cannot verify. |

The default is `report` because a half-applied edit is the worst outcome:
with `report`, a YAML change either takes effect on an instance entirely or
not at all. Set `ignore` on an instance whose image you deliberately manage
some other way.

A second field, `snapshot_volumes`, applies only with `rebuild`; see below.

## Pinning an image by digest

`rebuild` requires a digest-pinned image: `remote:repo:tag@sha256:...`. The
tag is cosmetic (Incus drops it when a digest is present); the digest is what
is pulled. Get one with any tool that shows an image's digest. tink does not need any of them (it asks the registry itself), but one is the
easiest way to read the digest to paste in, for example `skopeo`, which ships in the Incus package:

```
/opt/incus/bin/skopeo inspect --format '{{.Digest}}' docker://ghcr.io/home-assistant/home-assistant:2026.9.4
sha256:3e6710a7ab2a61311d9d899b719f6c3657791c63e8f4942cec4ebc42401d6b76
```

or `crane digest ghcr.io/home-assistant/home-assistant:2026.9.4`.

For a multi-arch image this is the digest of the index, which is what you
want. A pin cannot move, so the target of a rebuild is exactly what you
reviewed.

## Rebuilding: `on_image_change: rebuild`

```yaml
kind: storage-volume
name: ha-config
pool: default
---
kind: instance
name: ha
image: ghcr:home-assistant/home-assistant:2026.9.4@sha256:3e6710a7ab2a61311d9d899b719f6c3657791c63e8f4942cec4ebc42401d6b76
on_image_change: rebuild
snapshot_volumes: true
profiles:
  - default
devices:
  config:
    type: disk
    pool: default
    source: ha-config
    path: /config
```

`rebuild` replaces the instance's root filesystem with the new image and
keeps everything else: its config, its devices (so its static address), and
every custom volume attached to it. It is only for instances whose state
lives in volumes.

**What it is limited to.** Containers built from OCI images. A VM is refused
at load time, because for a VM the root disk is the whole operating system and
its data. The image must be digest-pinned, and the instance must not set any
`snapshots.*` key in `config:` (see below).

**The sequence.** Everything that can be checked read-only is checked first,
before anything is pulled, stopped or deleted:

1. **Preflight.** The instance is a container built from an OCI image; it has
   no instance snapshots; the new image's runtime config does not change in a
   way a rebuild would leave stale (see next section); the pool has room
   (a heuristic of three times the compressed image plus 256 MiB). Any failure
   here is a block, and `plan` shows it.
2. **Pre-pull.** The pinned image is copied into the local image store while
   the instance is still running, so downtime does not include the download.
3. **Stop** the instance, if it was running.
4. **Snapshot volumes**, if `snapshot_volumes: true`: every custom volume
   attached to the instance is snapshotted as `tink-pre-rebuild-<UTC time>`.
   This is after the stop, so the copy is consistent.
5. **Rebuild** from the local image.
6. **Apply config and devices** from the YAML, so new config is always paired
   with the new image.
7. **Start**, only if it was running before.

Rebuilds run one at a time across a whole apply, so two instances are never
down for a rebuild at once. A rebuild does not restart or touch other
instances, including ones that depend on it.

**What a rebuild discards.** Everything written to the instance's root
filesystem. When a rebuild is planned for an instance with no attached data
volume, `plan` warns, since the rebuild would then lose all of its state. Files placed by `kind: file`
resources are put back in the same `apply`: the file resource is planned after
the instance, finds the file missing or reverted, and pushes it again.

**Measured** on a Mac Pro 6,1, rebuilding Home Assistant 2026.9.1 to 2026.9.4
and back with the image already pulled: about 13 seconds of unavailability
end to end, including Home Assistant's own start.

### The runtime-config check

When Incus creates an instance from an OCI image it copies the image's
entrypoint, working directory, user and environment into the instance's config
(`oci.entrypoint`, `oci.cwd`, `oci.uid`, `oci.gid`, `environment.*`). `incus
rebuild` does **not** refresh them. After a rebuild they would still describe the
old image.

So before a rebuild, tink reads the new image's runtime config and compares it
with the instance. For any difference in a key the YAML does not declare, the
instance is **BLOCKED**:

```
instance/mqtt: BLOCKED: the new image changes runtime config that a rebuild does not
refresh; declare these keys under config: so they are written after the rebuild
(environment.VERSION, environment.DOWNLOAD_SHA256) -- environment.VERSION: instance has
"2.1.1", new image wants "2.1.2"; ...
```

To proceed, declare those keys under `config:`. tink applies config after the
rebuild, so they take effect:

```yaml
config:
  environment.VERSION: "2.1.2"
  environment.DOWNLOAD_SHA256: "fd905380691ac65ea5a93779e8214941829e3d6e038d5edff9eac5fd74cbed02"
```

Images that stamp a version into the environment (the official Mosquitto image
does) will need this on every upgrade. Home Assistant 2026.9.1 and 2026.9.4
have identical runtime config, so nothing is needed there. An image that runs
as a named, non-numeric user cannot be checked without its filesystem; declare
`oci.uid` and `oci.gid` to proceed.

### When it goes wrong

A rebuild is not atomic: Incus deletes the old root volume before creating the
new one. Because `apply` is stateless and `image.*` is only updated after a
successful rebuild, every failure below is recovered by fixing the cause and
running `apply` again.

| Fails at | What is left | What tink does |
|---|---|---|
| Preflight, or pre-pull | Nothing changed | Error; the instance keeps running |
| Stop | Nothing changed | Error |
| Volume snapshot | Instance intact, on the old image | Restarts it; error |
| Rebuild | Usually intact (Incus refused before deleting anything). If it failed after the old root was deleted, the **root filesystem is gone**; volumes are intact | Tries to restore the instance's previous run state. If it cannot restart, the error says the root may have been destroyed. Either way, re-run `apply` to retry the rebuild |
| Config update | New image, config incomplete | Left stopped; error. Fix and re-run `apply` |
| Start | New image and config | Error |

### Rolling back

Rollback is a procedure, not a command. With `snapshot_volumes: true` the volumes
were snapshotted immediately before the rebuild:

```
incus stop <instance>
incus storage volume snapshot restore <pool> <volume> tink-pre-rebuild-<time>
```

then put the previous digest back in the YAML and run `tink plan apply`. That is
another rebuild, and the runtime-config check applies in the reverse direction
too. Restoring discards whatever the volume received since the snapshot.

Snapshots are the reason `rebuild` rejects `snapshots.*` config. Incus refuses to
rebuild an instance that has *instance* snapshots, and a scheduled instance
snapshot would put one there again. Snapshot the data volumes instead;
volume snapshots do not block a rebuild.

## Exit status

`tink plan apply` ends with one summary line and exits non-zero if it left
anything unconverged:

```
summary: 3 converged, 0 changed, 1 blocked, 0 failed
Error: 1 resource(s) not converged (BLOCKED or SKIPPED, see above)
```

An exit status of 0 therefore means converged. Specifically:

- A **blocked** instance is not changed at all, and `apply` carries on with
  everything else.
- A `file` or `exec` resource that targets a blocked instance is **SKIPPED**
  and counted as blocked: it lives inside something that is not converged.
- A resource that merely `depends_on` a blocked instance still runs; that
  dependency means "must exist first", not "must be current".
- A resource that **fails** stops the apply after its level, as before.
- `tink plan` is read-only and exits 0 even when it shows BLOCKED.

## Limits

- Containers from OCI images only; no VMs.
- Profiles are not covered. A changed `kind: profile` still alters the
  effective config of every instance that uses it, blocked ones included.
- tink does not restart dependents of a rebuilt instance, and does not expire
  or prune `tink-pre-rebuild-*` snapshots.
- The runtime-config read is anonymous. A registry that needs credentials
  produces an error, which blocks a rebuild.
- Mutable tags: for `report` and `ignore`, `plan` sees content that moved under
  a floating tag; `rebuild` deliberately requires a digest instead.

## Why these rules

Each of these was checked against the Incus source or a live daemon.

- **`image.id` is not an identity.** It is a property of the cached image
  *record*, set by whichever pull first created that content, as typed then.
  Three instances created from the same bytes by a tag, a digest, and a tag plus
  digest all report the same `image.id`, and it can keep or drop Docker Hub's
  `library/` prefix. Comparing it as text gives false positives and false
  negatives. The fingerprint (`volatile.base_image`) is what identifies content.
- **`incus rebuild` needs a stopped instance and downloads inside the
  operation.** Without a pre-pull, downtime includes the whole download.
- **It deletes before it creates** (`rebuildCommon`), hence the failure table.
- **It does not refresh the `oci.*` and `environment.*` keys** copied at creation,
  hence the runtime-config check.
- **Instance snapshots make it fail; custom-volume snapshots do not.**
- **Files pushed to the root filesystem do not survive it.**
