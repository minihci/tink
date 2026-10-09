# `tink export`: from live objects back to a stack file

`tink plan` and `tink plan apply` take a YAML stack to live Incus objects. `tink export` goes the other way: it reads live instances (with
the project and storage volumes they use) and writes the stack YAML that describes them. It is the step that follows
[`tink run`](../internal/run/DESIGN.md): `run` is the quick way to find a stack's shape without knowing Incus's device vocabulary, and the
file `export` writes is what you keep and apply from then on.

```
tink export [--project P] [--offline] [--no-pin] INSTANCE...        # YAML on stdout, notes and the check on stderr
tink --remote tron export --project tink-play kuma-play > tink.yaml
```

It only reads, and it works under `--remote` ([remote.md](remote.md)).

## What it writes, and what it leaves out

An Incus instance carries about twenty-five config keys for every one a person set. The exporter keeps what a person (or `tink run`) wrote:

| On the live object | In the export |
|---|---|
| `volatile.*` (uuid, hwaddr, idmap, last state) | left out: Incus's own |
| `image.*` | left out; the image becomes `image:` (below) |
| `oci.entrypoint`, `oci.cwd`, `oci.uid`, `oci.gid`, `environment.*` that **equal what the image says** | left out: the image's own, copied at creation |
| the same keys when they **differ** from the image | kept: an override (`tink run IMAGE CMD` sets `oci.entrypoint`) |
| `environment.HOME=/root`, `environment.TERM=xterm` | left out: set by Incus at creation |
| the instance's own `devices`, `profiles`, every other `config` key | kept (`root` comes from the profile and is not the instance's) |
| `user.tink.run.command` | not config: written as a `# created by:` comment |
| a custom volume an instance's disk device attaches | a `kind: storage-volume`, in the same project and pool |
| a volume's other config (`initial.uid`, `size`...) | the volume's `config:` ([resolver-architecture.md](resolver-architecture.md#update-2026-10-09-a-volumes-own-settings)); tink's own keys and `volatile.*` stay out |
| a volume's `snapshots.schedule` / `snapshots.expiry` | a `backup: {snapshots: ...}` block |
| a volume with no snapshot policy | **no `backup:` block**, and a comment: `plan` warns until someone decides. It does not invent `none:` |
| the instance's project, if not the default | a `kind: project` with its config |

"Equal what the image says" is read from the registry, the way a rebuild reads it ([image-updates.md](image-updates.md)). With `--offline`, or
when the registry cannot be reached, nothing can be compared: everything is kept and a note says that some of it is the image's.

### The image

`image.id` is **not** reliable: it is whichever string first filled the cached image record, so two references for the same bytes can differ
and an unrelated one can inherit another's id (the top of `internal/resolve/imagedrift.go`). So the exporter treats it as a candidate and checks it
with the same comparison `plan` uses, against the fingerprint the instance was built from (`volatile.base_image`):

- **matches**: the reference is written pinned, `docker-oci:louislam/uptime-kuma:2@sha256:...`, with the digest the registry reports for it
  (`--no-pin` keeps the bare reference). Checked on a real Mosquitto: it recovered the same digest the stack's author had pinned by hand.
- **does not match**: written anyway, with a `# image: WARNING` comment and a note, instead of passing a wrong answer off as a right one.
  `plan` on the file reports it as drift.
- **cannot be checked** (offline, registry down): written as recorded, with a note.
- **not an OCI instance** (a VM from an image alias): `image:` is left for you to fill in, with a comment.

### Secrets

After the image's own environment is gone, an `environment.*` whose name looks like a secret (`secrets.SensitiveKey`) is **never written**.
It becomes `${secret:INSTANCE-NAME}` and a note gives the command to add it, `tink secret set INSTANCE-NAME`. (Checking the image
first matters: Mosquitto's `GPG_KEYS` is a public key id the image ships, and is not a secret.) `tink run` also masks such values in the command it records.

## What it cannot know

A live object says what is, not why. These stay out, and the file says so in its first lines: comments explaining a value, `kind: exec` steps
(Mosquitto's `chown` of its data volume), `depends_on`, which volumes copy to which `kind: backup-target` (a volume's copy policy is noted, not rebuilt),
and what a volume's backup should be. Treat the file as a starting point to review.

## The check

Finally the exporter plans what it wrote against the live server, with the same code `tink plan` runs, and prints the result to stderr:

```
checked against the live server with tink plan:
  project/tink-play: no changes
  storage-volume/kuma-data: no changes
      warning: no backup declared -- ...
  instance/kuma-play: no changes
```

"No changes" for every resource is the goal, and the command exits non-zero when it is not. The warning about the backup is the file being honest
about a decision it left to you.

## `tink run` leaves a trace

An instance made by `tink run` records the command it was given in `user.tink.run.command` (environment values that look like secrets are
masked). Incus otherwise keeps nothing that says how an instance came to be. `export` shows it as a `# created by:` comment, so the file
carries the command it was distilled from. Plan and apply never read or diff it.

## Verified live (2026-10-09, the lab host)

- `kuma-play` (Uptime Kuma 2, made with `tink run` in a throwaway project): project, volume and instance exported; the image pinned; `tink secret set` added the
  one secret the note named; `tink plan` on the result: no changes at every level, and the secret's value is in neither the file nor the store's plaintext.
- `mosquitto` (the real one, default project, read only): ten image-derived keys left out, no false secret, the digest equal to the hand-written stack's.
  What remained different from the hand-written stack was exactly what a live object cannot say (the `exec` step, comments, `library/`, the backup decision).

- `abs-play` (Audiobookshelf, image on `ghcr.io`, two managed volumes and two bind mounts): exported, pinned, then the instance and both volumes were deleted and `tink plan apply` rebuilt
  everything from the exported file alone in 8 seconds; the app answered with the same version and a second `plan` said no changes (notes).

- `navi-play` (Navidrome, made with `tink run --user 1000:1000 --incus-config limits.memory=256MiB`): `oci.uid`/`oci.gid` kept as overrides of the image, both volumes exported with
  `initial.uid`/`initial.gid`, then the instance and volumes deleted and rebuilt with `tink plan apply` from the file alone: the process ran as 1000, `/data` was owned by 1000:1000, a second `plan` said no changes.
  (Before volume `config:` existed the same rebuild produced root-owned volumes and the app failed to open its database.)

Not tried: VMs, instances with several profiles, volumes carrying a copy policy (noted but not rebuilt), a registry that needs a login, an `image.id` that does not match what the instance runs.
