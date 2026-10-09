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
| `-e K=V`, `--env-file F` | `-e K=V`, `--env-file F` | the file is read on the machine running tink; `-e` wins |
| `--user UID[:GID]` | `--user UID[:GID]` | numbers only. Volumes tink creates for this run are owned by that user |
| `-m 512m` | `-m 512m` | |
| `--cpus 1.5` | `--cpus 1.5` | CPU *time*, as in Docker (not how many CPUs the instance sees) |
| `--device /dev/ttyUSB0[:/dev/ttyACM0]` | `--device ...` | character devices; a GPU or a block device uses `--incus-device` |
| `--privileged` | `--privileged` | Incus's `security.privileged`: no user-namespace isolation. It does **not** also grant every host device |
| `--network NAME` | `--network NAME` | an Incus managed network. `--ip ADDR` for a fixed address |
| `--rm` | `--rm` | |
| `IMAGE CMD...` | `IMAGE CMD...` | replaces the image's entrypoint **and** command (Docker replaces only the command) |
| `--platform`, `--pull`, `-d`, `-it`, `--init` | - | not applicable: see [the `run` design](../internal/run/DESIGN.md) |

### Bind mounts and `:shift`

An Incus container is unprivileged by default: the ids inside are shifted (container root is host uid 1000000 and up). A host directory owned by your user therefore shows up as `nobody` inside, which
the app can read but not write. Verified on the lab host: with a plain bind mount `touch` is "Permission denied"; with `:shift` it works. `:shift` maps the host ids into the container
(so files keep their numbers, and the container's root writes as root on the host, as it would in Docker). It is opt-in because it needs idmapped-mount support from the filesystem, which a network
filesystem may not have; when a writable bind mount has neither `:ro` nor `:shift`, `tink run` says so. A managed volume needs none of this.

## When there is no flag: the escape hatch

`--incus-config KEY=VALUE` and `--incus-device 'NAME type=TYPE key=value ...'` take Incus's own vocabulary, for anything the flags above do not cover. They add to what the flags decide and refuse to
override them. A few that come up (all checked live unless marked):

| You want | Write |
|---|---|
| a tmpfs (`--tmpfs /cache:size=64m`) | `--incus-device 'cache type=disk source=tmpfs: path=/cache size=64MiB'` |
| a GPU (`--device /dev/dri`) | `--incus-device 'gpu type=gpu'` (docs) |
| a block device | `--incus-device 'disk1 type=unix-block source=/dev/sdb path=/dev/sdb'` (docs) |
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
  `nictype: bridged, parent: br0`). The app then has its own IP rather than the host's.
- **`--cap-add` / `--cap-drop`.** No instance option for capabilities. An unprivileged container has a fixed set; `--privileged` is the blunt instrument, and `raw.lxc` is a raw escape with no support
  promise. Pi-hole's `NET_ADMIN` is the case people hit.
- **Health checks.** An image's `HEALTHCHECK` is not run, and Compose's `depends_on: condition: service_healthy` has nothing to wait on. In a stack, `depends_on` is start order only; a `kind: exec`
  with a `check:` is the nearest to "wait until it answers".
- **A stop timeout (`--stop-timeout`, `stop_grace_period`).** Only `boot.host_shutdown_timeout`, for the host shutting down. For a slow database, stop it with `incus stop --timeout` yourself.
- **`--hostname`, `--add-host` / `extra_hosts`.** The hostname is the instance name (verified). Nothing sets extra hosts entries.
- **The Docker socket** (Portainer, Traefik's Docker provider, Watchtower). There is no Docker. Reverse-proxy routes are `user.tink.ingress.*` config here, not labels read from a socket; image updates are
  `on_image_change` in a stack.
- **Container-to-container by name** works, and without a suffix: a bare `kuma-play` resolved from another instance on the same network (verified). Compose service names carry over.

## Then keep it

`tink run` is the quick way to find a stack's shape. Once it works, `tink export --project apps navidrome > tink.yaml` writes down what is there (without the image's own defaults or Incus's bookkeeping;
the image pinned to its digest; secrets as `${secret:NAME}` placeholders), and `tink plan apply` rebuilds it from that file. Rebuilding Navidrome from its exported file after deleting the instance and both
volumes brought it back running as uid 1000 with its data directory owned by 1000:1000 ([export.md](export.md)).
