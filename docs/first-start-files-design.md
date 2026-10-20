# Design: put an instance's files in place before its first start

**Status: describes what is built, 2026-10-13. Built** (#78) and measured in the lab (see "Measured"). It fixes a race found in the Headscale lab (the 2026-10-12 assessment, in #131). A gap found in the next lab, files under a disk-device mount, is closed by "Files under a mount" below: **built, and run against a real server on 2026-10-13** (the Home Assistant stack that found it); two cases are still only covered by fakes, listed there. The same hazard on a later *update*, while the instance is not running, was found in a third lab and is handled by "Later applies: the instance is not running" below: **built, and so far run only against fakes**. Claims are marked **[lab]**, **[code]** or **[verify]**.

## The problem

When tink creates an instance, it creates it, applies its config, and **starts it**; only afterwards do the `kind: file` resources that target it push their files **[code: `createInstance`, then the file level]**. A service that needs one of those files at start has a window in which the file is not there yet.

Seen on a throwaway Headscale 0.29.4 (it needs `/etc/headscale.yaml`) **[lab]**:

- The app exits within about a second (`FTL … error loading config file /etc/headscale.yaml`), the container is `RUNNING` for roughly eleven seconds and then `STOPPED`. In that window, 3 of 15 file reads failed.
- On the first rebuild from nothing, tink's own calls landed in it: `file/headscale-policy: reading current content … bad file descriptor`, then `Failed to retrieve PID of executing child process`. The apply failed. A second apply converged, because every step is idempotent.
- It is a race, so it is not reliable in either direction: the first creation of the same Headscale a few hours earlier, and of Authelia, went through.

The same first-start sequence is how any OCI service with a config file is brought up with tink: Authelia, Mosquitto, Headscale, the helper's own configuration.

## What Incus allows **[lab]**

An instance that was created and never started accepts file pushes (the call tink already uses) and gives them back, and the first start then finds them: a throwaway Headscale, created with `init`, given its config while `STOPPED`, started once, came up cleanly. So the ordering "create, place the files, start" is available; it is only not what tink does.

## The shape **[code]**

**When tink creates an instance, it places the instance's files before starting it.** Inside `createInstance`: create, apply config and devices, push the files that belong to this instance, start. Nothing else about the graph changes.

It composes with what is already there, because `applyOne` plans each resource immediately before acting on it **[code]**: when the file resource's own turn comes, the planner reads the content, finds it equal, and says `no changes`. No second push, and no restart (a file's `restart: true` only fires when it pushes). The only visible difference is that those files say `no changes` in the apply that created their instance, and the instance's line names them.

### Which files

A file is placed with its instance when:

1. its `instance:` is an instance this stack declares, **and that instance is a container**. A virtual machine needs its agent running to take a file **[code]**, so it keeps today's behaviour;
2. its declared content is known now (`content` or `source_path`, read at load): always true for `kind: file`;
3. everything it waits for is the instance or another such file. A file that `depends_on` an exec, or on anything else that must have happened first, is left to its own turn. This is worked out to a fixed point: removing one file can make another ineligible;
4. (added later) its path is not under a disk-device mount, or the mount is a custom volume it can be written into. See "Files under a mount".

Files are placed in declaration order.

### What a failure to place means

**Placing is an ordering improvement, never a new way to fail.** If a push into the stopped instance fails (a directory that is not there, say), the instance is still started and the file resource fails in its own turn, with the message it has always had. The alternative, failing the creation, would leave a created-but-stopped instance and a new class of half-done state for no benefit.

### What is said

`instance/headscale: created; placed 2 file(s) before the first start (headscale-policy, headscale-config)`, and only for files that were placed. `plan` still says `would create` for each; the one thing it adds is a note for a file a mount keeps from the first start (see "Files under a mount").

## Alternatives considered

- **Split the instance into "create" and "started" steps in the graph**, with the start waiting for every file. The most general, and the biggest change: every resource that needs the instance running (execs) would have to be re-pointed at the second step, and the level order would change for all stacks. Placing at creation gets the same result for the case that matters, with no graph change.
- **Retry the transient errors** (`bad file descriptor`, `Failed to retrieve PID`). Treats the symptom: the app still crashes and restarts, and `restart: true` files would restart it again. It also depends on guessing which errors are transient.
- **Make the application wait** (an entrypoint that polls for the file). Not tink's call, and the image has no shell.
- **Do nothing; apply twice.** What the lab did. Every step is idempotent, so it converges, but "apply it again" is a poor first impression for a first install.

## Tests

- The selection (`firstStartFiles`): a container's files are chosen; a VM's are not; a file that waits for an exec is not; the fixed point (a file that waits for an ineligible file is not); a file for an instance not in the stack is not; two instances with the same name in different projects do not mix.
- The order, with fakes: create, config, each file in declaration order, start. A failed push does not stop the start. A failed create stops everything and places nothing.
- The reporting: the instance says which files were placed; a file that failed to place is not named.
- The existing file planning already covers "content equal means `no changes`"; one test pins that a file placed at creation then plans as `no changes`.
- Live: rebuild the lab Headscale from nothing. The first apply must converge with no failures, the log must not contain `error loading config file`, and the process must have started once.

## Measured, 2026-10-13 **[lab]**

The lab Headscale was rebuilt from nothing (instance and volume deleted, one `plan apply`), counting the first applies that converge:

| Build | Converged on the first apply | Failed |
|---|---|---|
| without the fix | 8 of 12 | 4 (the same message every time: `file/headscale-policy: reading current content of /etc/headscale-policy.hujson on headscale: bad file descriptor`) |
| with the fix | 11 of 11 | 0 |

With the fix each apply said `placed 2 file(s) before its first start`, the files then planned `no changes`, the exec set the first policy, and the plan afterwards was clean. If the true failure rate without the fix were a third, 11 successes in a row would happen about one time in 80. Caveats: one control batch passed 4 of 4 and the next 4 of 8, so a handful of runs misleads; one host and one service; and the check "no error in the application's log" was dropped because `incus console --show-log` keeps only recent lines.

## Files under a mount

**Added 2026-10-13.** Status: built, covered by tests with fakes, and run live the same day on the lab stack that found the gap (see "Run live"). **Not yet run on a real server:** a write into a volume that *fails* (the failure sentence), and a volume that has been attached before (the owner it gets from `volatile.idmap.last`). Everything from here to "Later applies" is about an instance's *first* start; a file's later updates are the next section.

### The gap **[lab]**

Found by accident in the end-to-end lab (the author's private lab notes). The instance `ha-e2e` had a disk device `config` (a custom volume `e2e-ha-config`, at `/config`) and a `kind: file` for `/config/configuration.yaml` that depended only on the instance. tink said `placed 1 file(s) before its first start: file/ha-yaml`. But the file was found in the container's own root filesystem (`<pool>/containers/<project>_<instance>/rootfs/config/configuration.yaml`, modified 17:16:22), underneath the mount, and the running container saw the volume. The file the application read was the file resource's own push after the start, at 17:16:31 (`file/ha-yaml: created`), and Home Assistant read its YAML about two seconds after that: a race it happened to lose to tink. A file on the instance's own filesystem (a Caddyfile at `/etc/caddy/Caddyfile`) behaved as designed and then planned `no changes`.

The cause is the order of events, not the push: a disk device is mounted when the instance starts, and whatever the root filesystem holds at that path is hidden from then on. Pushing into the stopped instance puts the file in the hidden place.

### The rule **[code]**

Each candidate file (chosen by the rules above) is checked against the disk devices the instance will have:

- the instance's own `devices:`, plus those of its profiles. A profile the stack declares is read from the stack, any other from the server. A profile that cannot be read adds nothing, so the file is placed exactly as before: this check never makes a placement fail. An instance that names no profile gets Incus's `default`. A device of the same name on the instance replaces a profile's whole device, as in Incus;
- only `type: disk` devices with a `path`. The root disk (`path: /`) is not a mount: it is the filesystem. Paths are cleaned and compared by whole components, so `/config` holds `/config` and `/config/x` but not `/configuration`; when mounts are nested the deepest decides.

Then, for a file at or below a mount path:

| the mount is | the file |
|---|---|
| not any (the file is not under a mount) | pushed into the stopped instance, as before |
| a custom storage volume (`pool` and a plain volume name as `source`) | **written into the volume** before the start (below) |
| a host directory (`source: /srv/...`), a disk that is not a plain volume, or the file *is* the mount path | **not placed** before the start; it is written by its own turn right after, as it always was |

A file that waits for a file that is not placed waits for the start too, the same as one that waits for an exec; nothing hides it, so it is not reported.

### Writing into the volume **[code]**, **[lab]**

The pinned Incus client (`github.com/lxc/incus/v7` v7.4.0) has a storage-volume file API: `GetStorageVolumeFile`, `CreateStorageVolumeFile` and `DeleteStorageVolumeFile` on `InstanceServer` (REST: `/1.0/storage-pools/{pool}/volumes/{type}/{volume}/files?path=`, path relative to the volume's root). Reading the server source: the call mounts the volume itself for as long as it runs (`Volume.FileSFTPConn` runs the work in `MountTask`, with a `forkfile` process rooted at the volume), so a volume nothing has mounted yet can be written.

The volume exists by then: a `storage-volume` resource the instance depends on is created before the instance, and a managed volume named by the instance's own devices is created by the instance's config step in any case.

What is done keeps the file resource's meaning, converge and not seed: read the file in the volume, **write only when it differs** (so a file already right is left untouched, and a different one is overwritten), with the mode `0644` and the owner defaults of the instance push, **no parent directories created** (a missing one fails, and the message says tink creates none). A failure is, as before, not a new way to fail: the instance still starts, the file is written by its own turn, and the apply now says why it was not placed.

### Run live **[lab]**

On the lab host (project `e2e-lab`), the instance `ha-e2e` (Home Assistant 2026.9.4, disk device `config` = the custom volume `e2e-ha-config` at `/config`) and its volume were deleted by hand, and the same stack was applied with the build of this change (`cef65d8`, darwin). What the fakes could not show, and what was seen:

1. **The file lands in the volume, and the second apply is quiet.** Apply printed `instance/ha-e2e: placed 1 file(s) before its first start: file/ha-yaml (in volume default/e2e-ha-config)` and then `file/ha-yaml: no changes`; the earlier build said `created` there, the second push. The second plan said `no changes` everywhere. The file is visible in the container at `/config/configuration.yaml`, and there is **no copy in the container's rootfs** under the mount.
2. **Owner and mode.** As the container sees it the file is mode `0644`, uid `0`, gid `0`, and Home Assistant read it.
3. **The start straight after the write is fine.** The instance was created at 23:55:59.27 and last used at 23:55:59.80, the file's mtime is 23:55:59, Home Assistant's stable `http` config appeared at 23:56:06.8 and the config migrated from the YAML (pending, with the `server_host` / `use_x_forwarded_for` / `trusted_proxies` stanza) at 23:56:12.9. It ran and listened on the bridge address, so the lingering `forkfile` and mount of the volume did not get in the way.
4. **Plan, a host directory (plan only, nothing applied).** For a stack with a host-directory disk device at `/media`, a file at `/media/x.conf` printed `note: file/probe-under-host-dir: not placed before the first start, because /media is a mounted host directory (device media); it is written right after the start, so the app may read its config first`, and a file at `/etc/caddy/Caddyfile` in the same stack printed no note.

**[verify]** Still only covered by fakes:

- A write into a volume that **fails** (a missing parent directory, say): the instance should still start, and the apply should print `... not placed before the first start, because writing /sub/x into volume default/vol failed: ...`.
- A volume that **has been attached before**: it has a `volatile.idmap.last`, which the volume's `forkfile` applies, so the owner the file gets there is the one thing not seen. The fresh volume above gave uid/gid 0 in the container.

### What is said

- Apply, after `instance/ha-e2e: created`: `instance/ha-e2e: placed 1 file(s) before its first start: file/ha-yaml (in volume default/e2e-ha-config)`, for a file that went into a volume; and for each file that was left for after the start:
  `file/ha-yaml: not placed before the first start, because /media is a mounted host directory (device media); it is written right after the start, so the app may read its config first`.
  The count is of files in place, and a skipped file is never in it. A write into a volume that failed is said the same way, with its cause: `... not placed before the first start, because writing /sub/x into volume default/vol failed: ...`.
- Plan, under the `would create` line of the instance: the same sentence for each such file, as a `note:`. Plan can know what a mount hides; whether a write into a volume works it finds out when it happens. A file that will be written into a volume gets no note.

### Later applies: the instance is not running

**Added 2026-10-13.** Status: **built and covered by tests with fakes only; not yet run against a real server.** What is read from the server's source is marked **[code]**; the build to verify it with is named in the pull request.

#### The gap **[lab]**

Found in the platform lab (the author's private lab notes). The sign-on helper's Authelia crash-looped on a bad config. Incus listed the container as `Running`, with a dead init (`Invalid PID -1`), and would not stop it. tink's corrected `configuration.yml` was pushed to the container and landed in `containers/tink_sso/rootfs/config/` (mtime 19:50:23), **under** the `/config` volume mount; the volume kept the old file (mtime 19:46:05). tink printed `file/sso-config: created`, the application kept reading the old file, and the way out was deleting the stateless instance by hand and letting tink recreate it with the file written into the volume first. It is the first-start hazard again: the instance's file API writes the root filesystem beneath the mount path whenever the mount is not in place, and it is not in place whenever the instance has no init, not only before the first start. The same applies to reading: `plan` compared the declared content with the copy beneath the mount, so it could say `no changes` about a file the application never sees.

#### What "not running" means **[code]**

Decided from the instance's *state* (`GET /1.0/instances/NAME/state`, `incusapi.InstanceRunning`), not the status string, which is what the lab showed to be wrong. The instance is **not running** when:

| the state says | counts as |
|---|---|
| a status other than `Running` (or `Ready`, which is `Running` plus the ready signal): `Stopped`, `Frozen`, `Error`, `Starting`, … | not running |
| `Running` with a pid of 0 or less | not running: no usable init |
| the call itself fails with a 500 whose message says `Invalid PID` | not running: no usable init |
| `Running` with a pid above 0 | **running** |
| any other failure (403, another 500, a dropped connection) | **an error**: the plan or the apply of that file fails with the cause, nothing is guessed |

The third row is the one the lab actually hit. The pinned server source (`incus` v7.4.0, `driver_lxc.go` `renderState`) asks for the init's start time of a `Running` container, and `driver_common.go` `processStartedAt` fails with `Invalid PID %d` for a pid below 1; the state handler turns that into a 500. So for a container whose init has died the state call *fails*, rather than returning a document with `pid: -1`; the second row is there for a server that does answer with one. **[verify]** that the real daemon answers that way (the lab log only has the message).

The state is asked **only for a file under a mount**. A file outside every mount is read and written through the instance's own file API without any extra call but one read of the instance, and a virtual machine keeps the instance's own file API in every case (it takes no push before its agent runs anyway, so this change adds nothing for it). The mounts are those of the **live** instance (its expanded devices, which include its profiles' disks), picked by the same code as the first start (`mountsFromDevices`, shared with `mountsOf`): `type: disk` with a `path`, the root disk excluded, the deepest mount deciding, a custom volume told from a host directory by its `source`. An instance that does not exist yet is not under any mount; the file plans as a create as before.

#### What is done

| the file is | the instance is running | the instance is **not** running |
|---|---|---|
| outside every mount | its own file API | its own file API (unchanged) |
| under a **custom volume** mount | its own file API (it sees the mount) | **read from, compared in, and written into the volume**: the same read-compare-write-if-different as the first start, mode `0644`, the owner the client defaults give, no parent directories created |
| under a **host directory** mount, a disk that is not a plain volume, or *is* the mount path | its own file API | **not pushed**: `DEFERRED`, with a warning; the next apply that finds the instance running writes it |

`plan` reads where `apply` will write, so it no longer says `no changes` or `would update` from a place the application never sees: for a volume file it reads the volume's copy, and for a deferred file it says it did not look. The file's `no changes` in an apply is therefore a statement about the volume when the instance is not running, and the line below it says so.

#### What is said

- Volume, plan (a `note:` under the file) and apply (a line after `created`, `updated (...)` or `no changes`):
  `instance sso is not running (its status is Stopped), so /config/configuration.yml is read from and written into volume default/sso-data, where the app finds it once the instance runs; pushed into the instance it would land beneath the /config mount (device config)`.
  For the dead init the parenthesis reads `Incus lists it as Running but cannot find its init process: Invalid PID -1` (or `... reports no init process (pid -1)` when the server answers with a document).
- Host directory, plan (`file/NAME: DEFERRED`, then a `warning:`) and apply (the same warning, then `file/NAME: deferred until the instance is running (see the warning above)`):
  `not written, because instance ha is not running (its status is Stopped) and /media/list.txt is at or under /media, a mounted host directory (device media): pushed now it would land beneath the mount, where the app never sees it, so it is left for the next apply that finds the instance running`.
- A deferred file **does not fail the apply** and is not `BLOCKED`: the exit status is unchanged and the summary line gets `, N deferred` at its end, only when there is one. This is deliberate, and a decision to revisit: the apply's promise that an exit status of 0 means converged does not hold for a deferred file. Making it count as blocked is one line (`outDeferred` to `outBlocked` in `applyOne`).
- If the instance's state changes between the plan and the push of one file, so that the push finds a mount it must not push beneath, it fails with that cause and the advice to run the apply again. It never pushes beneath the mount.

#### `restart: true` is unchanged **[code]**

`restart` fires after a write that was made, exactly as after a push: `run.EnsureRunning` asks for a *restart* when Incus lists the instance as `Running` and a *start* otherwise. So after a write into the volume of a **stopped** instance the instance is **started** (as today, after a push into a stopped instance), and for the **dead-init** instance a restart is asked for, which the lab says Incus may refuse ("would not stop it even with `boot.autorestart` off"); `EnsureRunning` then gives up after five attempts and the apply fails, with the file already in the volume. Nothing about when a restart happens was changed, and a file that was already right in the volume plans as `no changes` and restarts nothing.

#### Tests

Against fakes (`filemount_test.go`, `state_test.go` in `incusapi`): the state decision (stopped, frozen, `Running` with pid -1, the state call failing with `Invalid PID -1`, `Ready`, running, and every other failure as an error); the target of a file for each (a volume mount, a host directory, the mount path itself, nested mounts, a root-filesystem file, a name that only starts like the mount path, a virtual machine, an instance that does not exist) with how often the state is asked; `plan` reading the volume when not running (update, create and no changes, with a stale copy beneath the mount that would give the wrong answer) and the instance when running; the deferred warning with the file, the device, the mount and why; `pushFile` writing where the decision says, and its restart; the whole `applyOne` output; the summary. Each was checked to fail with its piece of the change taken out.

**[verify]** against a real server, none of it yet run:

- that Incus answers the state call of a `Running` container with a dead init with a 500 `Invalid PID -1` (read from the source, seen in the lab only as the message);
- that the volume file API writes into a volume that a `Running` container with a dead init still has attached, and that the application then reads the new file after a start;
- the owner a file overwritten that way gets (the first-start section's open case of a volume attached before, `volatile.idmap.last`, applies here too);
- what `restart: true` does on such an instance (above);
- the wording, in a real plan and a real apply.

### Tests

The selection against a table (a file under a volume mount, the mount path itself, `/etc/caddy/Caddyfile` next to a `/config` mount, the root disk, a device from a declared profile, a profile that cannot be read, `/configuration` against `/config`, nested mounts, a host directory, a virtual machine, a file that waits for an exec, a file that waits for a skipped one); the profile lookup (declared first, then the server, a failing server is "cannot be read"); `placeVolumeFile` against a fake server (absent, already right, different, a read that fails, a directory in the way, a missing parent); the sequence with the steps `createInstance` uses; the apply and plan sentences; the `note:` line of `plan`.

## Not in this slice

A virtual machine's files; files that wait for an exec; an option to leave an instance stopped; changing when an existing instance is started.

## Open questions

1. Whether `plan` should say that a file will be placed with its instance. It says `would create` today, which is true; the difference is only in the apply. It does now say it for a file a mount keeps from the first start, because there the plan and the old behaviour part ways.
2. ~~Whether the same placing should apply when an existing but *stopped* instance is started by an apply. Today that case already works, since a stopped instance takes the push.~~ It did not work for a file under a mount, and the platform lab showed it: see "Later applies: the instance is not running". A stopped instance still takes a push for a file outside every mount.
3. Whether a deferred file should count as not converged (exit status, `BLOCKED`) rather than as a warning. See "Later applies".
