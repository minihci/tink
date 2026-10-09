# Exploration notes: Uptime Kuma on Tron via `tink run`, then back out to Tink YAML

Started 2026-10-09. A path of discovery, not a race. Throwaway project `tink-play` on tron
(Incus 7.5.1); everything here is deletable. Raw dumps live in the session scratchpad
(`tron-meta.txt`).

Why Uptime Kuma: popular, one image (`louislam/uptime-kuma:2`), and its README's whole install is
one command: `docker run -d --restart=always -p 3001:3001 -v uptime-kuma:/app/data --name uptime-kuma louislam/uptime-kuma:2`.
Port + named volume + restart policy is exactly the docker-run surface `tink run` claims to cover.

## Log

### 1. Getting it running

```
tink --remote tron run --name kuma-play --project tink-play --restart unless-stopped \
  --network incusbr0 -p 3101:3001 -v kuma-data:/app/data docker-oci:louislam/uptime-kuma:2
```

Observations (O) and suggestions (S):

- **O1. `docker-oci:` prefix required.** The README's `louislam/uptime-kuma:2` is not accepted as-is.
  **S:** if the image has no `remote:` prefix, default to `docker-oci:` (or print a hint). The whole point
  of `run` is to paste a docker-run line.
- **O2. Project must already exist.** `Failed instance creation: Failed loading project: Project not found`.
  The message does not say "create it". **S:** either create it (it is a one-liner, but features like
  `features.profiles=false` are a real decision, see O4) or say how.
- **O3. Dry run is useful but vague about where.** It says `ensure managed volume default/kuma-data exists`
  (pool/name), not that it will be created *in project tink-play* (features.storage.volumes=true makes
  that so).
- **O4. A project with its own profiles has an empty `default` profile** (no root disk, no NIC). I made the
  project with `features.profiles=false` to inherit tron's default. Worth a sentence in `run` docs.
- **O5 (important). The default profile on tron has no NIC, so the first run produced a RUNNING instance
  with no IPv4 at all and a proxy device that points at nothing.** `curl :3101` returned nothing;
  `incus list` showed the IPV4 column empty. Docker users expect networking by default. **S:** at plan
  time, compute the expanded devices (instance + profiles); if there is no `nic`, either warn loudly
  ("no network device: published ports will not work") or refuse unless `--network` / `--no-network`.
- **O6. `tink run` is one-shot.** Re-running the same name: `Failed instance creation: Instance "kuma-play" already exists`.
  Correct and clear. (This is the "midpoint, not end state" idea from the run-then-distill note.)
- **O7. Output is terse, and silent about the volume.** Three lines: created / applied config and devices /
  started. The managed volume `kuma-data` was created on the first run but nothing printed it (the dry run
  does mention it). On the second run the volume already existed and, again, nothing said so.
  **S:** print `created volume kuma-data` / `reusing volume kuma-data`.
- **O8. Timing:** first run 72 s (image pull of ~600 MB into the project), second run a few seconds (project
  image cache). No progress output during the 72 s. **S:** a "pulling image..." line.
- **O9. `-v name:/path` deleted instance keeps the volume** (as Docker does). Good, unsurprising.
- After the fix: HTTP 302 on `:3101`, Uptime Kuma 2.5.6 on its first-run database-setup page.

### 2. The metadata Incus holds (see `tron-meta.txt` for the raw dump)

Four objects came out of one `tink run`: **project** (made by hand), **instance**, **custom volume**, and a
**cached image** (600 MB, `auto_update: true`, `update_source: docker.io/louislam/uptime-kuma:2`).
`project.used_by` lists all three of the latter, which is a handy "what is in this stack" query.

What is on the instance, sorted by who wrote it:

| Source | Keys | Export? |
|---|---|---|
| a person / `tink run` | `boot.autorestart`, `environment.*`, `limits.*`, `user.*`, `devices` (local only), `profiles` | **yes** |
| Incus runtime | 17 `volatile.*` (uuid, hwaddr, idmap, last_state.power...) | no |
| the image, copied at init | `image.*` (5), `oci.cwd/uid/gid/entrypoint` | no, unless overridden |
| the image's own ENV | `environment.*` that equal the image's ENV (seen on mosquitto, not on kuma) | no |

- **O10. Nothing records that tink made it.** No `user.tink.*` key, no description, no label. After the fact
  Incus cannot tell a `tink run` instance from a hand-made one. (Volumes get `user.tink.backup.policy` only
  when a stack declares one.) **S:** `tink run` could stamp `user.tink.run.command` (the original flags, or
  better, the resolved Tink YAML) and a `description`. That is also the cheapest possible "export": read
  back what you wrote. Costs one config key; and by the Incus lifecycle-event measurement in the tron notes
  (a config PATCH ~20 ms), the price is nothing.
- **O11. `devices` vs `expanded_devices`.** The instance's *local* `devices` is exactly what was added
  (eth0, proxy0, volume0); `expanded_devices` also has `root` from the profile. Export from local only.
  Device names are tink-generated (`proxy0`, `volume0`) and say nothing; an export could rename
  (`web`, `data`) from the port and path, as the hand-written mosquitto YAML does (`mqtt`, `config`, `data`).
- **O12. `image.id` lies.** `imagedrift.go` already knows this: `image.id` is whatever string first
  populated the cached image record. For export it is the best *starting guess* only. The ground truth is
  the fingerprint in `volatile.base_image`, and that cannot be inverted to a reference. Mosquitto's says
  `eclipse-mosquitto:2`, the hand-written YAML says `library/eclipse-mosquitto:2@sha256:38c0...`.
- **O13. `oci.entrypoint` = image `Entrypoint` + `Cmd`, space-joined** (verified with skopeo against the
  registry config: `["/usr/bin/dumb-init","--"]` + `["node","server/server.js"]`). So "was it overridden by
  `tink run IMAGE CMD`?" is answerable by comparing with the image config. The image config also has
  `ExposedPorts: {3001/tcp}`, which `tink run` could use to say "this image listens on 3001, you did not
  publish it" (a cousin of O5).
- **O14. Incus env injection is inconsistent.** Mosquitto's config carries `environment.HOME=/root` and
  `TERM=xterm`, plus the full image ENV (PATH, VERSION, GPG_KEYS, DOWNLOAD_SHA256...). Kuma's, created today
  on the same server, carries none. Probably a creation-path difference (older instance / a different Incus
  version). An exporter must therefore diff `environment.*` against the image ENV every time, not assume.
- **O15. The state API is the interesting runtime metadata**: IP, memory 266 MB, CPU seconds, per-device
  disk usage (`volume0: 16 KiB`), network counters. None of it belongs in YAML; all of it belongs in a
  `tink status` if there ever is one.

### 3. Round trip: live instance -> Tink YAML -> `tink plan`

A prototype exporter (a ~100-line read-only Python script over `incus query`; since replaced by `tink export`, round two below) turns a live instance into a stack file. Rules, as learned:

1. **Project** (if not `default`): `kind: project` with its config.
2. **Volumes**: every local `disk` device whose `source` is not a path becomes `kind: storage-volume`
   (config minus `volatile.*`), plus a `backup:` block that says `none: "exported: nobody has decided yet"`.
3. **Instance**: `image:` = `docker-oci:` + `image.id`; `profiles`; config minus `volatile.*`, `image.*`,
   and minus `oci.*` / `environment.*` that equal the image's own config (read from the registry); devices as-is.
4. **Secrets**: an `environment.*` whose name looks sensitive is written `${secret:name}` and a note says
   to add it. (Never a literal value.)

Results:

| Export of | `tink plan` on the exported file |
|---|---|
| `tink-play/kuma-play` (3 objects) | `project`, `storage-volume`, `instance`: **no changes** at every level |
| `default/mosquitto` (3 objects, real) | volumes **no changes**; instance BLOCKED only because of the secret it invented (see O16) |

- **O16. The sensitive-name heuristic is wrong for `GPG_KEYS`** (a public key ID the image ships). The fix that
  worked: drop image-default env *first*, then apply the heuristic to what is left. A good example of
  why the registry config is part of the export input.
- **O17. Export misses what is not an Incus object.** Compared with the user's hand-written mosquitto YAML:
  `kind: exec` (the chown that makes the data volume writable by uid 1883) is gone, as are the comments
  explaining why `limits.memory` is 256MiB, the digest pin, `library/`, and `depends_on`. An export is a
  *starting point to review*, not the stack; say so in its header comment.
- **O18. Digest pin recovery is plausible.** `plan` already asks the registry what a tag resolves to and compares
  it to `volatile.base_image`. An exporter could do the same: resolve the candidate reference, confirm it equals
  the instance's fingerprint, then append `@sha256:<manifest digest>`. If it does not match, say
  "image.id says X but the instance was built from something else" instead of exporting a wrong answer.
- **O19. Volumes with a copy policy** (Immich) keep it in `user.tink.backup.policy`, a raw config key. A naive
  export would write that key as `config:`; the right export reads it back through `backupmeta` into the
  `backup:` block. (Not prototyped.)

### 4. What `plan` says when things differ

Tried: moved the port and changed the tag in the YAML.

```
level 2:
  instance/kuma-play: BLOCKED
      device.proxy0: map[connect:tcp:127.0.0.1:3001 listen:tcp:0.0.0.0:3101 type:proxy] -> map[... listen:tcp:0.0.0.0:3201 ...]
      drift: image: "docker.io/louislam/uptime-kuma:1" resolves to e183295053e4, but the instance was built from bc73ff045084 (docker.io/louislam/uptime-kuma:2)
      blocked: image drift with on_image_change: report -- nothing on this instance is changed (...): <the same drift line again>
      blocked: 1 config/device change(s) are withheld until this instance matches the YAML
```

- **O20. The drift sentence is printed twice** (once as `drift:`, once inside `blocked:`).
- **O21. Device changes are printed as Go `map[...]`**, with the whole device on both sides. The only field that
  changed is `listen`; a field-level `device.proxy0.listen: tcp:0.0.0.0:3101 -> tcp:0.0.0.0:3201` would be
  much easier to read.
- **O22. `plan` exits 0 while an instance is BLOCKED.** Fine for a read-only command (the docs say `apply` is
  what exits non-zero), but a CI use would want `plan --exit-code` or similar.
- **O23. A typo is a hard error with a good message** ("tink rejects fields it does not know ... check the
  spelling"): the strictness is the right call.
- **O24. Running `tink plan` on the laptop without `--remote`** says `dial unix /var/lib/incus/unix.socket: no such
  file`. A hint ("use --remote NAME or $TINK_REMOTE; remotes: tron...") would be kind.

## Where this points (suggestions, in priority order)

1. **`tink run` should refuse or warn when the instance would have no NIC** (O5). This is the one that bit.
2. **Stamp provenance on what `run` creates** (O10): a `user.tink.*` key holding the resolved YAML. It makes
   "export" trivially lossless for run-built instances, and lossy-but-useful (the rules above) for the rest.
3. **`tink export [--project P] INSTANCE...`** (name provisional): read-only, prints a stack YAML to stdout,
   and verifies itself by running the plan comparison on its own output, saying plainly what it could not know
   (O12, O17, O18). It would use the same `diffConfig`/`checkImage` code that `plan` uses, so "export then plan
   shows no changes" is a test, not a hope. This is the "distill" step from the run-then-distill note, as a verb.
4. Small: `docker-oci:` default (O1), volume/pull progress lines (O7, O8), project-missing hint (O2), plan
   output polish (O20, O21).

## Round two: the suggestions built, the experiment repeated (branch `run-export-from-exploration`)

Built: the no-NIC guard, a `user.tink.run.command` stamp, `tink export` (docs/export.md), `docker-oci:` fallback, progress and volume lines,
a project-missing hint, and three plan-output fixes. Then the experiment again from scratch on the lab host, starting with the *naive* README attempt.

| # | Round one | Round two |
|---|---|---|
| O1 | bare image name rejected | `"louislam/uptime-kuma:2" is not a local image or a configured remote: using docker-oci:...` |
| O2 | `Failed loading project: Project not found` | `project "nope-play" does not exist: tink run does not create projects ... for example incus project create ...` |
| O5 | RUNNING, no IP, dead proxy | refused before anything is created, naming `--network`; verified nothing was left behind |
| O7 | silent about the volume | `creating volume default/kuma-data` (and `reusing volume` on a second run) |
| O8 | 72 silent seconds | `creating kuma-play from ... (a first pull of the image can take a minute)` printed first |
| O10 | no trace of how it was made | `user.tink.run.command` stored; password masked as `'KUMA_ADMIN_PASSWORD=***'` |
| O12, O16-O18 | prototype only | `tink export`: image checked against `volatile.base_image`, then pinned; image-derived env and `oci.*` dropped; no false secret on `GPG_KEYS` |
| O20 | drift sentence twice | once |
| O21 | Go `map[...]` | `device.proxy0.listen: "tcp:0.0.0.0:3101" -> "tcp:0.0.0.0:3201"` |
| O24 | bare socket error | adds "use --remote NAME or $TINK_REMOTE" |

The loop that matters, run for real: `tink run` (with a password in `-e`) -> `tink export` -> the note says `tink secret set kuma-play-kuma_admin_password`
-> `tink plan` on the exported file: **no changes**, with the plaintext in neither file. On the real Mosquitto (read-only) the exporter recovered the
same `@sha256:` digest the hand-written stack pins.

Found while building it (so, not in round one):
- **The exporter has to reuse the planner's own comparisons** (`checkImage`, `runtimeConfigDiff`) or it disagrees with `plan` about what "the same" means. It lives in `internal/resolve` for that reason.
- **Omit `backup:`, do not invent `none:`.** My prototype wrote a placeholder opt-out, which silences the one warning that tells the person a decision is missing.
- **`image.id` can name a different image than the one running.** The exporter says so (a `# image: WARNING` comment) instead of exporting it. Not exercised against a real mismatch, only by the plan's own tests; worth one deliberate try.

Still open: O3 (dry run does not say which project a volume lands in), O4 (project profile semantics deserve a docs paragraph), O6 and O9 (fine as they are), O11 (device
names `proxy0`/`volume0` are exported as they are, which is lossless but uninformative), O14 (why Kuma's instance has no copied `environment.*` while Mosquitto's does, unexplained),
O15 (a `tink status` is not designed), O19 (a volume's copy policy is noted, not rebuilt into `backup:` + `backup-target`), O22 (`plan` exits 0 when BLOCKED).
A dry run still shows the image as typed, because resolving the fallback needs the server.

## State left behind on tron

Project `tink-play` holds: instance `kuma-play` (running, http://tron.local:3101, made by the branch's build), volume `kuma-data`, cached
image `bc73ff04...`. To remove: `tink`-less, `ssh claude@tron.local 'sudo incus delete -f kuma-play --project tink-play < /dev/null; sudo incus storage volume delete default kuma-data --project tink-play < /dev/null; sudo incus image delete bc73ff045084eddb4c7250e186ecdd102bc7da1d01bceea0828e418863d1df15 --project tink-play < /dev/null; sudo incus project delete tink-play < /dev/null'`.
