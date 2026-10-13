# Design: put an instance's files in place before its first start

**Status: a design note, 2026-10-13. Built** (#78) and measured in the lab (see "Measured"). It fixes a race found in the Headscale lab ([what-tink-is.md](what-tink-is.md), "Where it is strong, and where it is weak"). A gap found in the next lab, files under a disk-device mount, is closed by "Files under a mount" below: **built, and run against a real server on 2026-10-13** (the Home Assistant stack that found it); two cases are still only covered by fakes, listed there. Claims are marked **[lab]**, **[code]**, **[proposed]** or **[verify]**.

## The problem

When tink creates an instance, it creates it, applies its config, and **starts it**; only afterwards do the `kind: file` resources that target it push their files **[code: `createInstance`, then the file level]**. A service that needs one of those files at start has a window in which the file is not there yet.

Seen on a throwaway Headscale 0.29.4 (it needs `/etc/headscale.yaml`) **[lab]**:

- The app exits within about a second (`FTL … error loading config file /etc/headscale.yaml`), the container is `RUNNING` for roughly eleven seconds and then `STOPPED`. In that window, 3 of 15 file reads failed.
- On the first rebuild from nothing, tink's own calls landed in it: `file/headscale-policy: reading current content … bad file descriptor`, then `Failed to retrieve PID of executing child process`. The apply failed. A second apply converged, because every step is idempotent.
- It is a race, so it is not reliable in either direction: the first creation of the same Headscale a few hours earlier, and of Authelia, went through.

The same first-start sequence is how any OCI service with a config file is brought up with tink: Authelia, Mosquitto, Headscale, the helper's own configuration.

## What Incus allows **[lab]**

An instance that was created and never started accepts file pushes (the call tink already uses) and gives them back, and the first start then finds them: a throwaway Headscale, created with `init`, given its config while `STOPPED`, started once, came up cleanly. So the ordering "create, place the files, start" is available; it is only not what tink does.

## The shape **[proposed]**

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

**Added 2026-10-13.** Status: built, covered by tests with fakes, and run live the same day on the lab stack that found the gap (see "Run live"). **Not yet run on a real server:** a write into a volume that *fails* (the failure sentence), and a volume that has been attached before (the owner it gets from `volatile.idmap.last`).

### The gap **[lab]**

Found by accident in the end-to-end lab (tron repo, `e2e-lab/e2e-lab.md`, scorecard E1b and finding 2). The instance `ha-e2e` had a disk device `config` (a custom volume `e2e-ha-config`, at `/config`) and a `kind: file` for `/config/configuration.yaml` that depended only on the instance. tink said `placed 1 file(s) before its first start: file/ha-yaml`. But the file was found in the container's own root filesystem (`<pool>/containers/<project>_<instance>/rootfs/config/configuration.yaml`, modified 17:16:22), underneath the mount, and the running container saw the volume. The file the application read was the file resource's own push after the start, at 17:16:31 (`file/ha-yaml: created`), and Home Assistant read its YAML about two seconds after that: a race it happened to lose to tink. A file on the instance's own filesystem (a Caddyfile at `/etc/caddy/Caddyfile`) behaved as designed and then planned `no changes`.

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

### Tests

The selection against a table (a file under a volume mount, the mount path itself, `/etc/caddy/Caddyfile` next to a `/config` mount, the root disk, a device from a declared profile, a profile that cannot be read, `/configuration` against `/config`, nested mounts, a host directory, a virtual machine, a file that waits for an exec, a file that waits for a skipped one); the profile lookup (declared first, then the server, a failing server is "cannot be read"); `placeVolumeFile` against a fake server (absent, already right, different, a read that fails, a directory in the way, a missing parent); the sequence with the steps `createInstance` uses; the apply and plan sentences; the `note:` line of `plan`.

## Not in this slice

A virtual machine's files; files that wait for an exec; an option to leave an instance stopped; changing when an existing instance is started.

## Open questions

1. Whether `plan` should say that a file will be placed with its instance. It says `would create` today, which is true; the difference is only in the apply. It does now say it for a file a mount keeps from the first start, because there the plan and the old behaviour part ways.
2. Whether the same placing should apply when an existing but *stopped* instance is started by an apply. Today that case already works, since a stopped instance takes the push.
