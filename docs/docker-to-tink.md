# From a `docker run` line to tink

For someone with an install line from a project's README. It says what to change, what tink does for you, and what Docker has that Incus does not.
([docker-gap-analysis.md](docker-gap-analysis.md) is the evidence behind it: which flags the real install pages use, and how each was checked.)

## Paste it, then fix the four things that always differ

```
docker run -d --name navidrome --restart unless-stopped --user 1000:1000 -p 4533:4533 \
  -v /path/to/data:/data -v /path/to/music:/music:ro -e ND_LOGLEVEL=info deluan/navidrome:latest
```
becomes
```
tink run --name navidrome --project apps --network incusbr0 --restart unless-stopped --user 1000:1000 -p 4533:4533 \
  -v navidrome-data:/data -v /srv/music:/music:ro -e ND_LOGLEVEL=info deluan/navidrome:latest
```

1. **`-d` goes.** An Incus instance is always created in the background. (`-it` is `incus exec`, after the instance exists.)
2. **Name a network.** Incus's default profile may have no network device at all (the lab host's has none): the instance would run with no address and a published port forwarding to
   nothing. `tink run` refuses that when you publish a port. `--network incusbr0` (or your bridge) is the fix.
3. **The project must exist.** `tink run` does not create projects. `incus project create apps -c features.profiles=false` makes one that shares the default profile.
4. **Paths are on the server.** With `--remote`, `-v /srv/music:/music` is a path on the Incus server, not on your laptop, and it must be absolute. A named volume (`-v navidrome-data:/data`) lives in a
   storage pool and is created on first use, as in Docker.

Nothing else needs changing for the common cases. In particular, **leave the registry off if you like**: `deluan/navidrome:latest` is taken as Docker Hub, with a warning that tells you
how to say so (`docker.io/deluan/navidrome:latest`). `ghcr.io/org/app` and `quay.io/org/app` just work. A registry tink has no remote for is an error that tells you how to add one.

## The flags

| Docker | `tink run` | Notes |
|---|---|---|
| `--name` | `--name` | required: tink does not invent names |
| `--restart always\|unless-stopped\|on-failure\|no` | `--restart ...` | sets both restart-on-crash and start-at-boot. No retry counts (`on-failure:5`) |
| `-p [IP:]H:C[/udp]`, `-p 8000-8010:8000-8010` | `-p ...` | the same forms. A container port alone is refused |
| `-v name:/path` | `-v name:/path` | a managed volume, created if absent (in `--pool`, default `default`) |
| `-v /host:/path[:ro]` | `-v /host:/path[:ro]` | absolute, and on the server. **Add `:shift` if the app must write to it** (below); options combine with a comma, `:ro,shift` |
| `-e K=V`, `--env-file F` | `-e K=V`, `--env-file F` | the file is read on the machine running tink; `-e` wins. **An empty value (`-e K=`) is an unset variable:** Incus never stores an empty config value, so the process does not see `K` at all, where Docker sets it to the empty string. A program that tells the two apart behaves differently; most read the environment in a way that cannot |
| `--user UID[:GID]` | `--user UID[:GID]` | numbers only. Volumes tink creates for this run are owned by that user. Without `--user`, they are owned by the user the image runs as when that is not root (Docker copies the image's ownership into a new named volume; Incus does not, so tink reads `oci.uid` and `oci.gid` from the new instance and says so in the "creating volume" line). A volume that already exists keeps its owner, and `--dry-run` does not read the image, so it cannot say |
| `-m 512m` | `-m 512m` | |
| `--cpus 1.5` | `--cpus 1.5` | CPU *time*, as in Docker (not how many CPUs the instance sees) |
| `--device /dev/ttyUSB0[:/dev/ttyACM0]` | `--device ...` | character devices; a GPU, the whole USB bus (`/dev/bus/usb`) or a block device uses `--incus-device` |
| `--privileged` | `--privileged` | Incus's `security.privileged`: no user-namespace isolation. It does **not** also grant every host device |
| `--network NAME` | `--network NAME` | an Incus managed network. `--ip ADDR` for a fixed address. `--network host` is refused, with the reason (below) |
| `--rm` | `--rm` | |
| `IMAGE CMD...` | `IMAGE CMD...` | goes after the image's own entrypoint, as in Docker (CMD is replaced). `tink run` reads the entrypoint from the image's registry, anonymously, and prints it; a registry it cannot read (private, offline) gives a warning, and then the command **replaces** the entrypoint, which for an image that has one starts nothing: write the entrypoint out before the command (#230) |
| `--platform`, `--pull`, `-d`, `-it`, `--init` | - | not applicable: see [the `run` design](../internal/dockerrun/DESIGN.md) |

### Bind mounts and `:shift`

An Incus container is unprivileged by default: the ids inside are shifted (container root is host uid 1000000 and up). A host directory owned by your user therefore shows up as `nobody` inside, which
the app can read but not write. Verified on the lab host: with a plain bind mount `touch` is "Permission denied"; with `:shift` it works. `:shift` maps the host ids into the container
(so files keep their numbers, and the container's root writes as root on the host, as it would in Docker). It is opt-in because it needs idmapped-mount support from the filesystem, which a network
filesystem may not have; when a writable bind mount has neither `:ro` nor `:shift`, `tink run` says so. A managed volume needs none of this.

## A command after the image, and the image's entrypoint

An image carries two things: `ENTRYPOINT` (the program) and `CMD` (its default arguments). Docker always runs the entrypoint followed by arguments, and what you write only chooses which:

| You write | The process that runs (Docker, and Compose) |
|---|---|
| nothing | `ENTRYPOINT` + `CMD` |
| arguments after the image, or Compose `command:` | `ENTRYPOINT` + your arguments (CMD is replaced) |
| Compose `entrypoint:` alone | your entrypoint (CMD is cleared) |
| `entrypoint:` and `command:` | your entrypoint + your command |

Incus keeps one merged string for an OCI container's process, the entrypoint and the command joined, and records no boundary between them. The one setting, `oci.entrypoint`, is a single string that replaces all of it. So the second row is the one place the two disagree: Docker means "append to an entrypoint you cannot see", and a value set as `oci.entrypoint` would have replaced it. `tink run` used to do that, and an image with an entrypoint started nothing: Piper's page writes `rhasspy/wyoming-piper --voice en_US-lessac-medium`, its entrypoint is `bash docker_run.sh`, and the container tried to run a program called `--voice` (#230). The other rows already agree. An image with only a `CMD` is fine, because replacing the CMD and replacing everything are the same.

What tink does, by door **[code]**, and **[lab]** for Piper and MariaDB (Incus 7.5.1):

- **`tink run IMAGE ARGS`** reads the image's own `ENTRYPOINT` from its registry (the one place it is still apart from the `CMD`; `internal/imageconfig`), prints it, and sets `oci.entrypoint` to the entrypoint followed by your arguments, which is Docker's result. It reads anonymously. A registry it cannot read (a private one, no network) gives a warning, and then the command replaces the entrypoint as before; for an image that has one, write the entrypoint out before the command. An image with no entrypoint, and one that is not an OCI image, are left as they were. `--dry-run` does not read the registry and says so.
- **`tink import`** joins `entrypoint:` and `command:` when a service has both. A `command:` alone is refused, because import connects to nothing and cannot see the entrypoint it would follow; the message says how to find it (`docker inspect IMAGE`, or `skopeo inspect --config docker://IMAGE`, or the line `tink run IMAGE ARGS` prints).
- **A stack written by hand** says the whole `oci.entrypoint` line, so there is nothing to resolve.

One limit that is not solved: `oci.entrypoint` is a single string, and tink joins the arguments with spaces and no quoting, so an argument that contains a space is split.

## When the process exits at once

Docker would show the exit in `docker ps -a` and the reason in `docker logs`. `tink run` checks for it: after a start, it waits two seconds and asks whether the instance is still running. If it is not, it prints the last 20 lines of the instance's console log and exits non-zero:

```
started grafana
grafana stopped straight after it started (status Stopped); the last lines of its console log:
  GF_PATHS_DATA='/var/lib/grafana' is not writable.
Error: grafana stopped straight after it started: the process exited
```

When the start itself fails (an instance with a published port reports Incus's `Error occurred when starting proxy device`, because the proxy is started after the process and the process is already gone), the same lines are printed before the error. Two limits: the check is one look, so an instance with a restart policy that crashes in a loop may be running when it is asked, and an ephemeral instance (`--rm`) is deleted when it stops, with its console log. The values of `-e` variables whose names look like secrets (`PASSWORD`, `TOKEN`, ...) are redacted from the lines. This is not `docker logs`: tink prints the tail once, on a failure, and does not follow the log (`internal/dockerrun/DESIGN.md`). Checked live on a local Incus 7.5.1 server (2026-10-24) with Grafana on a root-owned existing volume: with a published port the cause was printed before Incus's error, without one it was printed after `started` and the exit status was 1, and a healthy instance printed nothing extra and exited 0.

## When there is no flag: the escape hatch

`--incus-config KEY=VALUE` and `--incus-device 'NAME type=TYPE key=value ...'` take Incus's own vocabulary, for anything the flags above do not cover. They add to what the flags decide and refuse to
override them. A few that come up (all checked live unless marked):

| You want | Write |
|---|---|
| a tmpfs (`--tmpfs /cache:size=64m`) | `--incus-device 'cache type=disk source=tmpfs: path=/cache size=64MiB'` |
| shared memory (`--shm-size 512m`) | `--incus-device 'shm type=disk source=tmpfs: path=/dev/shm size=512MiB'` (an instance already has a 64 MiB `/dev/shm`, as Docker does; the device replaces it) |
| a GPU (`--device /dev/dri`) | `--incus-device 'gpu type=gpu'` (docs) |
| a block device | `--incus-device 'disk1 type=unix-block source=/dev/sdb path=/dev/sdb'` (docs) |
| one USB device (`--device /dev/bus/usb/001/004`) | `--incus-device 'coral type=usb vendorid=1a6e productid=089a'` (docs) |
| a serial adapter that may be unplugged | `--incus-device 'zigbee type=unix-char source=/dev/ttyUSB0 path=/dev/ttyUSB0 required=false'` |
| DNS servers (`--dns`) | `--incus-config oci.dns.nameservers=1.1.1.1` |
| a sysctl (`--sysctl`) | `--incus-config linux.sysctl.net.ipv4.ip_forward=1` (docs) |
| open-file limit (`--ulimit nofile=`) | `--incus-config limits.kernel.nofile=65536` (docs) |
| process limit (`--pids-limit`) | `--incus-config limits.processes=200` (docs) |
| a label (`--label`) | `--incus-config user.KEY=VALUE` |

## What Docker has that Incus does not

Say these out loud rather than hide them. Each is from Incus's reference for instance options and devices (docs), except where marked.

- **Host networking (`--network host`).** There is no shared network namespace. What the install page wants it for is usually discovery on the LAN (Home Assistant, DLNA), and the Incus answer is to give the
  instance **its own address on the LAN**: a `bridged` (or `macvlan`) network device on your LAN bridge. If you have a profile for that, layer it: `--profile lan` (the lab host's `lan` profile is exactly
  `nictype: bridged, parent: br0`). The app then has its own IP rather than the host's. `tink run --network host` and `tink import` on `network_mode: host` both refuse and say this, instead of planning a NIC on a network called `host`.
- **`--cap-add` / `--cap-drop`.** No instance option for capabilities. An unprivileged container has a fixed set; `--privileged` is the blunt instrument, and `raw.lxc` is a raw escape with no support
  promise. Pi-hole's `NET_ADMIN` is the case people hit.
- **A read-only root (`--read-only`, compose `read_only`).** A root device of the instance's own with `readonly=true` makes `/` read-only, and an Alpine container started that way (checked live, Incus 7.5.1). The Matter.js server's image did not: it
  stopped at start with "Failed to setup mount entries", and a mount point that is not already in the image cannot be created on a read-only root. So it is by hand, with `--incus-device 'root type=disk path=/ pool=POOL readonly=true'`,
  and may not start. `tink import` refuses `read_only` for that reason.
- **`--security-opt` / `security_opt` (`apparmor=unconfined`, `no-new-privileges`).** No instance key chooses an AppArmor profile or sets no-new-privileges. `raw.apparmor` and `raw.lxc` can carry one, with no support promise (docs; not tried).
  `tink import` refuses the key and says so.
- **Health checks.** An image's `HEALTHCHECK` is not run, and Compose's `depends_on: condition: service_healthy` has nothing to wait on. In a stack, `depends_on` is start order only; a `kind: exec`
  with a `check:` is the nearest to "wait until it answers".
- **A stop timeout (`--stop-timeout`, `stop_grace_period`).** Only `boot.host_shutdown_timeout`, for the host shutting down. For a slow database, stop it with `incus stop --timeout` yourself.
- **`--hostname`, `--add-host` / `extra_hosts`.** The hostname is the instance name (verified). Nothing sets extra hosts entries.
- **The Docker socket** (Portainer, Traefik's Docker provider, Watchtower). There is no Docker. Reverse-proxy routes are a hand-written route file here, not labels read from a socket; image updates are
  `on_image_change` in a stack.
- **Container-to-container by name** works, and without a suffix: a bare `kuma-play` resolved from another instance on the same network (verified). Compose service names carry over.

## Then keep it

`tink run` is the quick way to find a stack's shape. Once it works, `tink export --project apps navidrome > tink.yaml` writes down what is there (without the image's own defaults or Incus's bookkeeping;
the image pinned to its digest; secrets as `${secret:NAME}` placeholders), and `tink plan apply` rebuilds it from that file. Rebuilding Navidrome from its exported file after deleting the instance and both
volumes brought it back running as uid 1000 with its data directory owned by 1000:1000 ([export.md](export.md)).
