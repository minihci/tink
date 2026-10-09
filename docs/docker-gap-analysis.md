# Gap analysis: `docker run` -> `tink run` -> `tink.yaml` -> Incus

2026-10-09. Not a plan to replace `docker run` ([DESIGN.md](../internal/run/DESIGN.md) says why not). The question is narrower: **when someone
pastes the install instructions of a popular self-hosted project, what do they hit?** So the gaps are ranked by how often the real instructions use a feature, and by what your
own stacks needed, not by what Docker can do.

How each claim is known, marked on every row:

- **live** - tried on the lab host (Incus 7.5.1), 2026-10-09, in a throwaway project, with a probe container.
- **docs** - from Incus's reference documentation; not tried.
- **code** - from reading tink's code; not tried.
- **untested** - a guess worth one test.

## 1. Evidence: what real install instructions use

Twelve projects' own install pages or compose files (read through a summarising fetch, so counts are approximate and quotes are not verbatim):
Pi-hole, Jellyfin, Frigate, Zigbee2MQTT, Vaultwarden, Nginx Proxy Manager, Gitea, Paperless-ngx, Navidrome, Home Assistant, plus Uptime Kuma and
Audiobookshelf from the earlier rounds. It is a homelab-skewed sample of 12, which is small; a count from Docker Hub's most-pulled images would be better evidence. Your own Immich,
Mosquitto and Matter stacks are a second source.

| Feature | Projects (of 12) | Notes |
|---|---|---|
| a restart policy | **12** | 11 `unless-stopped`, 1 `always` |
| published ports | **11** | Home Assistant uses host networking instead. 3 publish **UDP** (Pi-hole, Jellyfin, Frigate); 1 binds to **loopback** (Vaultwarden `127.0.0.1:8000:80`) |
| volumes | **12** | **6 read-only** (`:ro`/`read_only`: media, `/etc/localtime`, udev, dbus). 3 mount a single host **file** (`/etc/localtime`, `/etc/timezone`) |
| environment | 8 (+2 optional) | almost always `TZ` and passwords |
| a database sibling + `depends_on` | 3 | Nginx Proxy Manager, Gitea, Paperless (their DB variants) |
| `user:` / `--user` | 4 | all optional, all about file ownership of mounted data |
| devices (`--device`) | 3 | Frigate (USB Coral, GPU), Zigbee2MQTT (serial), Jellyfin (GPU) |
| `privileged` | 2 | Frigate, Home Assistant |
| host networking | 2 | Home Assistant; Jellyfin for DLNA (optional) |
| `cap_add` | 1 | Pi-hole (NET_ADMIN, SYS_TIME, SYS_NICE) |
| tmpfs / `shm_size` | 1 / 1 | Frigate |
| a stop timeout | 2 | Frigate (30s), Home Assistant (60s) |
| memory / cpu limits | 1 | but **all 6** instances in your Immich/Mosquitto/Matter stacks set `limits.memory` |
| `env_file` | 1 | Paperless |
| healthcheck, command override | **0** | (images ship `HEALTHCHECK`; no install page configures one) |

Registries: Docker Hub **6**, ghcr.io **5**, an own host **1** (`docker.gitea.com`). **None of the six Docker Hub projects writes the registry** (`pihole/pihole`, `jc21/nginx-proxy-manager`,
`louislam/uptime-kuma`...); every ghcr.io and own-host project does. So "no registry means Docker Hub" is the case for about half of all installs.

## 2. How a flag becomes an API call

```
docker run flag --> tink run (Spec: Config + Devices + Profiles) --> tink.yaml (Resource) --> Incus API
```

- **`tink run`** is a *narrow* sugar layer: it knows about `-e -p -v --network --ip --restart --rm --vm --profile` and a command. Its output is two maps, instance
  `config` and `devices`, plus a profile list.
- **`tink.yaml`** is a near-**superset**: an instance's `config:` and `devices:` are passed through as they are, so anything Incus can express is expressible there, just without
  sugar. Anything `run` cannot say, the YAML can. (The exceptions are in section 5.)
- **Incus API**: create from an image (`POST /1.0/instances`, source = the OCI registry via the remote); create managed volumes
  (`POST /1.0/storage-pools/POOL/volumes/custom`); set config and devices (`PUT /1.0/instances/NAME`); start (`PUT .../state`). Applied atomically: one invalid device drops
  every change in the same update (found while building `run`).

So most gaps below are **"`run` has no way to say it"**, not "tink cannot do it". That shapes the recommendations: a few flags plus one escape hatch recover most of it.

## 3. The gap table

Verdicts: ✅ covered, ◐ partial, ❌ gap (Incus can; tink's `run` cannot say it), ⛔ no Incus equivalent (a different model, say so in the docs).
"Freq" is from the table above.

### Common (most installs hit these)

| Docker | Freq | `tink run` | `tink.yaml` | Incus primitive | Verdict |
|---|---|---|---|---|---|
| image from a registry | 12 | needs `docker-oci:`/`ghcr:`, or (new) a bare name with a warning | same (new) | OCI remote; Incus pulls it with skopeo | ◐ see section 6 |
| `--restart unless-stopped` | 12 | sets `boot.autorestart` only | `config:` | `boot.autorestart` (restart on unexpected exit, up to 10/min) **live**; `boot.autostart` (start with the daemon) docs | ◐ `run` does not set `boot.autostart`; your stacks set both by hand |
| `-p H:C` (tcp) | 11 | ✅ | ✅ | `proxy` device | ✅ **live** |
| `-p H:C/udp` | 3 | ✅ **done** | ✅ | `proxy` `listen=udp:...` | ✅ **live** |
| `-p 127.0.0.1:H:C` | 1 | ✅ **done** | ✅ | `proxy` `listen=tcp:127.0.0.1:H` | ✅ **live** |
| `-v name:/path` | 12 | ✅ (creates the volume) | ✅ | custom storage volume + `disk` device | ✅ **live** |
| `-v /host:/path` | 12 | ✅ with `:ro` or `:shift` (a note says so otherwise) | ✅ | `disk` bind. Unprivileged id mapping shows files as `nobody` | ✅ **live** (with `:shift`) |
| `-v ...:shift` (Docker has no such flag) | - | ✅ **done**, opt-in | ✅ | `disk` `shift=true` makes it writable | ✅ **live** |
| `-v /host:/path:ro` | 6 | ✅ **done** (was silently wrong: the container path became `/path:ro`) | ✅ | `disk` `readonly=true` | ✅ **live** |
| `-v /etc/localtime:/etc/localtime:ro` (a host file) | 3 | ◐ same `:ro` problem | ✅ | `disk` with a file source (containers) | docs |
| `-e KEY=VAL` | 8 | ✅ | ✅ and `${secret:NAME}` | `environment.KEY` | ✅ **live** |
| `--env-file` | 1 | ✅ **done** | ◐ one `${secret:}` per value | - | ✅ |
| `--user UID:GID` | 4 | ✅ **done** | ✅ | `oci.uid` / `oci.gid` **live** (process runs as 1234) | ✅ |
| volume owned by that user | 4 | ✅ **done** (new volumes) | ✅ (`config: initial.uid`) | volume config `initial.uid/gid/mode` at creation **live** (mounted as `1000:1000`, mode 750) | ✅ this is what your `kind: exec` + `chown` steps in the Matter and Mosquitto stacks work around |
| `--name` | 12 | ✅ required | `name:` | instance name; also the hostname **live** | ✅ |
| `container_name` / service names as hostnames | 3 | - | - | a bare name resolves between instances on the same network (`kuma-play`, no suffix) **live**, via the `incus` search domain | ✅ matches Compose; worth saying in the docs |

### Less common, but each blocks someone completely

| Docker | Freq | `tink run` | `tink.yaml` | Incus primitive | Verdict |
|---|---|---|---|---|---|
| `--device /dev/x:/dev/x` | 3 | ✅ **done** (character devices) | ✅ | `unix-char` **live** (`/dev/null` appeared in the container); `required=false` hotplugs docs; `usb`, `gpu` device types docs | ✅ |
| `--privileged` | 2 | ✅ **done** | ✅ | `security.privileged` **live** (identity uid map). Not Docker's: no extra devices or capabilities | ✅ **live** |
| `--network host` | 2 | ⛔ | ⛔ | none. The nearest is a LAN address: `nic` `nictype=bridged parent=br0`. **Your `lan` profile on tron already is this**, and `tink run --profile lan` works | ◐ docs only: say so |
| `--cap-add` | 1 | ⛔ | ⛔ | none in instance options docs. Unprivileged containers have a fixed capability set; `raw.lxc` is a raw escape docs | ⛔ |
| `-m` / `--cpus` | 1 (6 of 6 of yours) | ✅ **done** | ✅ | `limits.memory` **live** (cgroup `memory.max` = 128MiB); `limits.cpu` (a count) and `limits.cpu.allowance` (a share) docs | ✅ |
| `--tmpfs` / `type: tmpfs` | 1 | ◐ `--incus-device 'cache type=disk source=tmpfs: ...'` **live** | ✅ | `disk` `source=tmpfs:` with `size` **live** (64MiB tmpfs, size enforced). Only added while the instance is stopped; refused when running | ◐ |
| `--shm-size` | 1 | ◐ `--incus-device` | ✅ | a tmpfs disk at `/dev/shm` | untested |
| `--stop-timeout` | 2 | ⛔ | ⛔ | none per instance. `boot.host_shutdown_timeout` is for host shutdown only docs | ⛔ |
| `--dns` | 0-1 | ◐ `--incus-config oci.dns.nameservers=...` | ✅ | `oci.dns.nameservers` / `.search` / `.domain` **live** | ◐ |
| `--hostname` | 0 | ⛔ | ⛔ | the instance name, not settable **live** | ⛔ |
| `--add-host` / `extra_hosts` | 1 | ⛔ | ⛔ | none found | ⛔ |
| `--ulimit`, `--pids-limit`, `--sysctl`, `--security-opt` | 1 each | ◐ `--incus-config` | ✅ | `limits.kernel.*`, `limits.processes`, `linux.sysctl.*`, `security.syscalls.*` docs | ◐ |
| `--label` | 1 | ◐ `--incus-config user.KEY=VALUE` | ✅ | `user.*`. Reverse-proxy labels (Traefik/Caddy) are `user.tink.ingress.*` here | ◐ |
| `healthcheck` / `depends_on: condition: service_healthy` | 0 | ⛔ | ⛔ | Incus has no health check for application containers docs. `depends_on` is start order only; `kind: exec` with a `check` is the closest | ⛔ |
| `depends_on` | 3 | n/a | ✅ | tink's graph | ✅ |
| `IMAGE CMD...` | 0 | ◐ **replaces** entrypoint and command | ✅ | `oci.entrypoint`. Docker keeps the ENTRYPOINT and replaces only CMD | ◐ code: a semantic difference |
| `--rm` | 0 | ✅ | ✅ **done**: `ephemeral:`, and `export` keeps it | `ephemeral` | ◐ |
| `-d`, `-it`, `--init`, `--pull` | many | n/a | n/a | always detached; `incus exec`; image freshness is `on_image_change` | n/a |
| `/var/run/docker.sock` mounts (Portainer, Traefik, Watchtower) | - | n/a | n/a | no Docker here | n/a |

## 4. Recommendations, in order

**Status (2026-10-09, later the same day):** done: item 4 (`--user`, with volume ownership), item 6 (the escape hatch, as `--incus-config` and `--incus-device`: the names avoid Docker's `-c` and `--device`),
and, because ownership needs a home in a stack, the "stack-level" half of item 8 (a volume's `config:`, `initial.*` creation-only). **Later the same day, the small items:** 1 (`:ro`), 2 (`:shift`, opt-in, with a note when a writable bind mount has neither), 3 (`-p` UDP, bind address, ranges), 5 (`-m`, `--cpus`), 7 (`--device`), 10 (`--restart` also
sets `boot.autostart`), and `ephemeral:` in the YAML (and `export`), plus relative host paths refused up front. Then `--env-file` and `--privileged`, and 9 (the docs: [docker-to-tink.md](docker-to-tink.md)). Still open: what is under "Not done" in section 6. Everything the
escape hatch reaches is in the table above (a memory limit is `--incus-config limits.memory=512MiB`, a serial adapter is `--incus-device 'zigbee type=unix-char source=/dev/ttyUSB0 path=/dev/ttyUSB0 required=false'`).

By frequency in the evidence, then by whether it is a **trap** (silently wrong or confusing) rather than a plain gap. Sizes are rough.

1. **`-v SRC:DST[:ro|:rw]`** (small, and it is a bug today). Understand `ro`, accept `rw`, reject anything else. 6 of 12 installs.
2. **Bind-mount writability** (small code, a real decision). Verified: `shift=true` works. Either a `-v ...:shift`-style option or a documented default for host paths; the hesitation was
   that `shift` needs kernel support, so a default can be wrong on some hosts. The cheapest honest step is a paragraph in the docs and a clear error when it fails.
3. **`-p` with a protocol and a bind address** (small): `3001:3001/udp`, `127.0.0.1:8000:80`. 4 of 12.
4. **`--user`** (small) -> `oci.uid/oci.gid`, **plus ownership of the volumes `run` creates**: pass `initial.uid/gid` when it creates a managed volume for a `--user` container. That
   removes the `chown` step. 4 of 12 and two of your own stacks.
5. **`-m/--memory` and `--cpus`** (small): all your own instances set a memory limit.
6. **One escape hatch**: `-c KEY=VALUE` (instance config) and `--device NAME,type=...,k=v` (as `incus launch -c` / `incus config device add` do). This alone recovers `privileged`,
   `sysctl`, `ulimit`, `dns`, `devices`, `tmpfs` and the whole long tail without a flag each, and keeps `run` honest about being a translator rather than a clone. **I would do this before
   adding more sugar.**
7. **`--device HOST[:CONTAINER]`** as sugar over `unix-char` with `required=false`. 3 of 12, all hardware, all hard to get right by hand.
8. **Stack-level**: `storage-volume` must be able to carry `config:` (`initial.uid`, `size`) so the YAML can say what `run` could (section 5), and an `ephemeral:` field.
9. **Docs, no code**: host networking -> the LAN profile; `cap_add`, health checks, stop timeouts, `extra_hosts` -> "Incus has no equivalent, here is what to do instead"; bare service names resolve.
10. `--restart always|unless-stopped` also sets `boot.autostart`. Small, and makes `run` agree with what your stacks do by hand.

Not worth doing: a Docker CLI clone, `-it`, build, the Docker socket.

## 5. Gaps between `run`, the YAML and `export` (found while writing this)

- **(Fixed the same day.)** **A stack's `storage-volume` rejected `config:`** ("does not use field Config", tested live: the stack refused to load). So the YAML **could not** say `initial.uid: 1000`, `size`, or any
  other volume setting, even though Incus takes them on creation (**live**) and even though that is exactly what the `kind: exec` + `chown` steps are standing in for. The refusal was
  the right behavior (a silently ignored field is worse), but it was a gap in the format. It is allowed now, with `initial.*` creation-only ([resolver-architecture.md](resolver-architecture.md#update-2026-10-09-a-volumes-own-settings)).
- **`tink export` once wrote such a `config:` block anyway** for a volume with settings, producing a file tink would then refuse to load; its own check missed it because it plans in-memory
  resources. Fixed in this branch: the settings now appear as `# not exported:` comments with a note, and a test loads the exported file.
- `run --rm` sets `ephemeral`; the YAML had no field for it and `export` did not carry it. **Fixed:** `ephemeral: true`, additive like every other field.
- Device names `run` makes (`proxy0`, `volume0`) say nothing; a name derived from the port or path would read better in an exported file.
- The dry run listed devices in random order and did not say that a host path is on the **server** when `--remote` is used. **Fixed** (sorted; one note).

## 6. The registry story

What the evidence says: half of all installs leave the registry off, because on Docker's side it is implied; the other half always write it. Incus's own convention is a **remote name**
(`docker-oci:`, `ghcr:`), not a host. The goal is that someone can paste an install line unchanged, and that tink is clear about what it assumed.

**One rule, used by `tink run`, `plan` and `plan apply` (done in this branch):**

| You write | tink uses | tink says |
|---|---|---|
| `docker-oci:louislam/uptime-kuma:2` (a configured remote) | as written | nothing |
| `ghcr.io/advplyr/audiobookshelf:latest` (a registry host) | `ghcr:advplyr/audiobookshelf:latest` | nothing in `plan` (you were explicit); a note in `run` |
| `docker.io/louislam/uptime-kuma:2` | `docker-oci:louislam/uptime-kuma:2` | nothing |
| `louislam/uptime-kuma:2` (**no registry**) | `docker-oci:louislam/uptime-kuma:2` | **warning**: `names no registry: assuming Docker Hub. Say so with docker.io/louislam/uptime-kuma:2 (or docker-oci:louislam/uptime-kuma:2)` |
| `redis:7` (an official image) | `docker-oci:redis:7` | the same warning, suggesting `docker.io/library/redis:7` |
| `registry.example.com/team/app:1` (a host no remote is configured for) | refused | an error with the `incus remote add NAME https://registry.example.com --protocol=oci` command and the name to write afterwards |
| an alias that exists on the server, or that the stack's own `kind: image` declares | as written | nothing |

Things this fixed: the earlier fallback sent `ghcr.io/...` to Docker Hub, which failed with an error about `docker.io/ghcr.io/...`; and a stack's bare `image:` used to be read as a local
alias and fail at apply, after `plan` had said "would create". Also added two built-in remotes that homelab projects use, **`quay`** and **`lscr`** (the sample has none, but both are common
for linuxserver.io and Prometheus images). `tink export` always writes the explicit, pinned form, so the warning does not follow an exported file around.

**Decided, and why:**

- *Warn, do not refuse, for a missing registry.* The assumption is almost always right, and refusing would make the most common paste fail. The warning is on every `plan` until it is
  fixed, which is the nudge you asked for.
- *Refuse an unknown host.* Guessing Docker Hub for it is never right.
- *The stack file is the durable artifact*, so `plan` warns there too; the fix goes into the file.

**Not done, and worth deciding:**

- **Any registry without configuring a remote.** `quay.io/x/y` works now only because it is built in. A way to let *any* host work (synthesising an OCI remote from the host) would match
  Docker most closely, but remote *names* appear in `image.id`, in `plan`'s drift check and in the exported file, so it touches several places. Costs more than the rest of this section.
- **`:latest` and floating tags.** Most of the sample projects' examples use `latest`, `stable` or a moving major tag (two pin a version). `plan`'s drift check handles it; a warning that a stack is not pinned is a different
  question (`export` already pins).
- **Private registries** work through the remote's credentials (docs/remote.md); nothing here changes that.
- **`docker.io` aliases** such as `index.docker.io` and `registry-1.docker.io` are not mapped.

## 7. What I would try next

Fix the two bugs (`:ro`, relative host paths), then re-run Audiobookshelf as a newcomer with the escape hatch and `--user` in place; then Frigate or Zigbee2MQTT as the hardware case, since
nothing in the earlier rounds touched `--device`.
