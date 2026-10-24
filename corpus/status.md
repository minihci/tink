# Status: what tink does with each shape

Run on 2026-10-21 (L-2026-10-21b) against a local Incus 7.5.1 server, in a disposable project (since deleted), with tink built from
`main` (2f9d5c7). Each app's own line was pasted into `tink run` with only the changes `docs/docker-to-tink.md` says always differ (a
project, a network, no `-d`, a named volume or a server path for a host path) and host ports moved to loopback. "Answered"
means the published port returned an HTTP response or a DNS answer. It does not mean the app is set up or usable.

Evidence is marked **live** (run today), **docs** (the repository says it was run on the lab host, or only says it) or **none**.

## The first result: input format

17 of 21 pages give a compose file; 4 give only `docker run` (AdGuard Home, Home Assistant, Mosquitto, n8n). tink reads
neither. `tink run` takes docker-run *flags*, and a stack is written in Incus's own vocabulary (devices, `config:`). So each
compose-only app run here was translated by hand: into `tink run` flags when it had one container, and into a stack for
WordPress (live). The multi-container apps (Paperless-ngx, Nextcloud, Immich, and Forgejo with a database) need a stack
written by hand. This is a gap in the milestone, not an edge case: no shape below matters until a person has done it.

**Added 2026-10-23.** Home Assistant's page also gives a compose form (the corpus had only its `docker run` line), so 18 of 21 pages give a compose file and 3
give only `docker run` (AdGuard Home, Mosquitto, n8n). `tink import` on that compose example, with tink built from `main` (d911aff), is **refused**:
`privileged` is not in the supported subset, and `network_mode: host` has no equivalent; `stop_grace_period` is dropped with a warning ("Incus has no
per-instance stop timeout"); nothing is written. `tink run` takes the same page's flags (`--privileged`, `--device`, a LAN profile), so the two commands
disagree about the same page (#205). The 18 compose files `tink import` was surveyed on (`internal/compose/testdata`) did not include this one.

## Does each app run?

| Result | Apps |
|---|---|
| Answered, live | Vaultwarden, Uptime Kuma, Navidrome, Audiobookshelf, Actual Budget, Sonarr, Jellyfin, Home Assistant, Syncthing, Mosquitto, Pi-hole (web and DNS), WordPress + MySQL (as a stack; reaches the installer; planned again as "no changes") |
| Answered after a fix | Grafana (see finding 1), AdGuard Home (without its DHCP ports, finding 4) |
| Not run, same shapes as one that did | qBittorrent (as Sonarr), Paperless-ngx (as Immich and Nextcloud) |
| Ran on the lab host earlier (docs) | Nextcloud, Immich |
| Stopped by a registry with no remote | n8n (`docker.n8n.io`), Forgejo (`codeberg.org`): the error says how to add one |
| Cannot work | Portainer: needs the Docker socket, and Incus has no Docker |

That is 14 of 21 answering today. **None is wired**: no name, proxy route, sign-on or backup was checked. That is the next
measuring stick, and it is Phase 3's work.

## Shapes

| Shape | Apps | tink today | Evidence |
|---|---|---|---|
| `web-ui` | 20 | port reachable: yes. A name, proxy route and sign-on: `tink join` prints them, only DNS is applied | live (port); docs (join) |
| `tcp-port` | 19 | handled | live, 12 apps |
| `host-dir` | 16 | handled; a directory the app must write to needs `:shift` | live (read-only use); docs (`:shift`) |
| `env-config` | 12 | handled | live |
| `registry-named` | 9 | partly: `docker.io`, `ghcr.io` and `lscr.io` work as typed; any other registry needs a one-time `incus remote add` | live |
| `named-volume` | 8 | handled | live |
| `uid-gid` | 6 | partly: `--user` and `PUID` work; the image's own non-root user does not own a fresh volume (finding 1) | live |
| `secret` | 5 | handled by the age store for `environment.*`; not exercised today | docs |
| `sibling` | 4 | handled in a stack: a sibling resolves by bare name | live (`db`) |
| `media-lib` | 4 | partly: mounting an existing host directory works; filling a managed volume with existing files does not (#144) | live; issue |
| `ro-mount` | 4 | handled | live |
| `udp-port` | 4 | handled | live (Pi-hole answered a DNS query over UDP) |
| `depends-on` | 3 | partly: start order yes, "wait until ready" no (#141) | live; issue |
| `env-file` | 3 | handled for `tink run --env-file`; a stack has no `env_file` | live |
| `healthcheck` | 3 | not: an image's healthcheck is not run | docs |
| `public-url` | 3 | handled (it is an environment variable) | live (Vaultwarden) |
| `shared-data` | 3 | handled: two instances mounted one volume and saw each other's files | live |
| `host-network` | 2 (+2 optional) | not as such; the app gets its own address instead (Syncthing's GUI answered on it) | live; docs |
| `ephemeral` | 2 | `--rm` supported; not run | docs |
| `host-file` | 2 | handled (`/etc/localtime` read-only) | live |
| `host-socket` | 2 | a plain path such as `/run/dbus` mounts (Home Assistant ran with it); the Docker socket cannot (Portainer: clear error) | live |
| `loopback-port` | 2 | handled | live |
| `low-port` | 2 | handled (Pi-hole on port 53 and 80) | live |
| `stop-timeout` | 1 (+1) | not: only a host-shutdown timeout exists | docs |
| `cap-add` | 1 | not: no flag, no Incus option. Pi-hole ran without it; it is only needed for its DHCP and NTP roles | live; docs |
| `cmd-override` | 1 | handled, but it replaces the entrypoint too, unlike Docker | live; docs |
| `compose-vars` | 1 | not: there is no compose input | none |
| `digest-pin` | 1 | accepted; a pinned image is run in Immich's lab stack | docs |
| `privileged` | 1 | handled (Home Assistant) | live |
| `shm-tmpfs` | 1 | tmpfs handled (64 MiB mounted); `shm_size` not checked | live |
| `device` | 0 (+1) | handled for character devices and by `--incus-device`; no hardware to try | docs |
| `file-before-start` | 0 (+1) | handled for a bind-mounted directory (Mosquitto read its config) | live |

## What turned up

1. **An image that runs as its own non-root user cannot write to a fresh volume.** Grafana runs as uid 472; tink creates
   the volume owned by root unless `--user` is given. The container exits at once, and tink reports "Error occurred when
   starting proxy device ... pidfd", which names the wrong thing. The real cause is in the console log ("`/var/lib/grafana`
   is not writable"). `--user 472:0` fixed it. tink already knows the image's uid (`oci.uid`), so it could own the volume by
   it, or say so. This probably affects every image that is not root.
2. **A service and a volume cannot share a name.** WordPress's own compose names both `wordpress`; tink stops with
   "duplicate resource name" (the flat namespace noted in the roadmap).
3. **A registry other than Docker Hub, GitHub and LinuxServer needs a one-time `incus remote add`.** The message is exact.
4. **Port 67/68 clashes with the host's bridge.** The local bridge's DHCP server holds UDP 67, so AdGuard Home's DHCP
   ports cannot be published on the same host; Incus says "address already in use". Environmental, and only for the DHCP
   role.
5. **A name like `db` can be used once per network, across every project.** Two projects each wanting an instance called `db`
   (WordPress's and Nextcloud's databases are both called that) cannot share a bridge: Incus refuses the second with
   "Instance DNS name "db" already used on network". Run on 2026-10-21 (L-2026-10-21b) with two projects on one bridge. A third project could also
   resolve the first one's `db`, so the project boundary is not a name boundary. `tink plan` said "would create" for the second
   and only `plan apply` failed. #143 already lists "flat-namespace name collisions across projects"; this is a reproduction.
6. **Nothing reads compose.** See the first result.

## Reading it

Of 32 shapes, most are handled or partly handled, and the ones that are not are mostly small (`cap-add`, `healthcheck`,
`stop-timeout`). Getting an
app to *run* is mostly there. What the corpus shows is missing is at the two ends: getting a compose file in, and
everything after the app is running (name, proxy route, sign-on, backup).

## Added 2026-10-23: the Home Assistant companions

Seven pages added by #204: the Matter.js server, the Python Matter server it replaces, ESPHome, Zigbee2MQTT, Frigate, Node-RED and Music Assistant (the
page for each is `NAME/upstream.md`). Run on a local Incus 7.5.1 server in a disposable project (since deleted), tink built from `main` (d911aff), the same
way as above: each page's own `docker run` line through `tink run` with only the always-different changes (a project, a named volume for a host path, host
ports on loopback), and each page's own compose example through `tink import`. "Answered" means an HTTP response from the published port. Evidence is **live**.

| App | Its own line through `tink run` | Its compose example through `tink import` |
|---|---|---|
| Node-RED | **stopped at once**: `EACCES` copying `settings.js` into `/data` (the image runs as uid 1000, the fresh volume is root's: finding 1 above, #176). With `--user 1000:1000`: answered, 200, in about 10 s | imports (3 documents); the top-level `networks` is a note |
| ESPHome | host networking has no translation; the page's own bridge variant (`-p 6052:6052`) answered, 200, in about 5 s. The page says this variant loses `.local` name resolution; not tested | refused: `privileged`, `network_mode: host`. A warning that `ESPHOME_PASSWORD` looks like a secret |
| Music Assistant | host networking has no translation; the page's bridge variant (ports 8095 and 8097) answered, 302, in about 5 s. The page says that variant is unsupported and loses player discovery; not tested | refused: `network_mode: host`; and `${USERDIR:-$HOME}/docker/...` is read as a named volume that is not declared (finding 3) |
| Matter.js server | host networking has no translation; on a bridge it **stopped**: the data directory could not be locked (uid 1000 against a root-owned fresh volume; its page warns that "the new container runs unprivileged by default and so needs a permission-change of the data dir"). With `--user 1000:1000`: answered, 200, in about 10 s. Matter's own function (mDNS, IPv6 multicast, Thread) not tested, and its page says a bridge will not work | refused: `read_only`, `network_mode: host`, and `${HOME}` ("not set": only a `.env` beside the file is read) |
| Python Matter server | on a bridge, answered, 200, in about 10 s (it runs as root). `--security-opt apparmor=unconfined` has no `tink run` flag (`unknown flag`) and was left out | refused: `security_opt`, `network_mode: host`, and `${USERDIR:-$HOME}/...` as for Music Assistant |
| Frigate | answered in about 5 s, with the `/dev/shm` size and the `/tmp/cache` tmpfs given as `--incus-device` disks and no GPU or Coral. **Over TLS**: plain HTTP gets nginx's "400 The plain HTTP request was sent to HTTPS port"; `https://127.0.0.1:8971/` (self-signed) returns the UI and `/api/version` asks for a login (finding 4) | refused: `devices`, a `tmpfs` volume |
| Zigbee2MQTT | not run: it needs a Zigbee adapter. `tink run --dry-run` translates the page's line completely (the serial device, the port, `/app/data`, and `/run/udev` read-only) | refused: `devices` |

**One of seven imports; six are refused.** Counting the shapes refused across the seven (a service can have several): `network_mode: host` 4 (five with Home
Assistant's page), `devices` 2, `privileged` 1, a `tmpfs` volume 1, `read_only` 1, `security_opt` 1, and a `${VAR}` that compose reads from
the shell or defaults 3. `tink run` takes the same pages' flags except `--network=host` and `--security-opt`.

**Findings**

1. **Host networking is the dominant refusal, and these pages say it is a requirement.** ESPHome ("`--net=host` lets the dashboard use mDNS, which is how
   it discovers devices"), the Matter servers ("Bridge networking will not work"), and Music Assistant ("either host networking or macvlan networking is a
   mandatory requirement"). Three of them name a second thing that would do: Music Assistant says macvlan; the Matter server asks for "a network interface
   directly on the network where the Thread Border Router lives". That is the Incus answer (a NIC on the LAN bridge) and it is #206.
2. **`tink run --network=host` and `tink import` disagree about the same input.** Import refuses `network_mode: host` and says what to do. `tink run`
   takes `host` as the name of an Incus network, plans without a word (`would add device eth0: map[network:host type:nic]`), and only the real run fails, in Incus's words:
   "Failed to load network "host" for project "default": Network not found" (the instance is removed again). #205.
3. **A compose `${VAR:-$OTHER}` is not expanded.** Music Assistant's and the Python server's examples write `${USERDIR:-$HOME}`; the importer takes the default
   `$HOME` literally, so the bind mount becomes a named volume that "is not declared". The importer reads variables only from a `.env` beside the file, by
   design, so `${HOME}` is refused as unset. Three of the seven examples hit one or the other.
4. **Frigate's page contradicts its own port.** The ports table calls 8971 "Authenticated UI and API access without TLS"; the port speaks TLS with a
   self-signed certificate. A proxy in front needs an HTTPS upstream (`https-upstream` in `platform.md`).
5. **#176 is not an edge case here.** Two of seven (Node-RED and the Matter.js server) fail on a fresh volume for the same reason Grafana did, and a third
   page (Frigate) says its services run as an unprivileged user. Both start once `--user` is given; neither tells the person.
6. **`read_only` and `security_opt` are in no subset.** Neither is a `tink run` flag nor an importer key. Whether Incus has an equivalent for either was not checked.


**After #205 (2026-10-23, tink built from the branch that closes it, on the same local Incus 7.5.1).** `tink import` over the same seven examples and Home Assistant's own:
**two of seven import** (Node-RED, and Zigbee2MQTT, which `tink run` already translated), six do not. `privileged`, `devices`, a `tmpfs` volume and `shm_size` now
translate, as `tink run` does them; Frigate is then refused only for listing `/dev/bus/usb` (the whole USB bus, a directory), with the `--incus-device` for one USB
device. Host networking remains (ESPHome, Music Assistant, the two Matter servers, Home Assistant), and so does `read_only` and `security_opt`. The detail is in
[`docs/import.md`](../docs/import.md). Findings 2, 3 and 6 above, as they stand now:

- Finding 2 (**live**): `tink run --network host` is refused with the same explanation `tink import` gives, and not planned as a NIC on a network called `host`.
- Finding 3 (**code**): `${USERDIR:-$HOME}` expands its default; `$HOME` is read from a `.env` beside the file like any variable, so the variable resolves once that file says what
  `HOME` is. The shell's environment is still never read.
- Finding 6 (**live**): a root device of the instance's own with `readonly=true` makes `/` read-only (an Alpine container started and `touch /x` failed with "Read-only file
  system"), and a mount point that is not in the image then cannot be created. The Matter.js server's image **did not start** with a read-only root and a volume at
  `/data` ("Failed to setup mount entries"), nor without the volume, so `read_only` stays refused, with that reason. `security_opt` has no instance key (`no-new-privileges`,
  an AppArmor profile choice); `raw.apparmor` and `raw.lxc` can carry one, **not tried**.
- A tmpfs device honours its size at any path, including `/dev/shm` (**live**: 64 MiB by default, replaced by 512 MiB), and takes a size in bytes or with a suffix; a stack that
  `tink import` wrote from a compose file with `privileged`, a device, a tmpfs and `shm_size` applied, the mounts and the device were there, and a second plan said no changes.

## Added 2026-10-23, batch 2: Z-Wave, OpenThread, the voice trio and a database

Six pages added by #212: Z-Wave JS UI, the OpenThread border router, Piper, Whisper, openWakeWord and MariaDB (the page for each is `NAME/upstream.md`). Run on a local Incus
7.5.1 server in a disposable project (since deleted), tink built from `main` (9aaba7f), the same way as the batch above: each page's own `docker run` line through `tink run`
with only the always-different changes (a project, a managed volume for a host path, host ports on loopback, no `-it`), and each page's compose example through `tink import`.
"Answered" is a reply to the protocol the app speaks: a Wyoming `describe` request for the voice trio, the MySQL greeting for MariaDB. Evidence is **live** unless it says **dry run**.

| App | Its own line through `tink run` | Its compose example through `tink import` |
|---|---|---|
| openWakeWord | answered (`info`) at once; first run in about 18 s with the image pull | none on the page |
| Piper | **did not start** as the page writes it (finding 1). With `bash docker_run.sh` before the page's arguments: started in about 4 s and answered (`info`) 2 s later (the image had been pulled by the failed attempt) | none on the page |
| Whisper | **did not start** the same way. With `bash docker_run.sh` before the page's arguments (`--model tiny-int8 --language en`): started in about 4 s, answered (`info`) 4 s later | none on the page |
| MariaDB | answered with the server greeting (13.0.2-MariaDB) 4 s after start; about 23 s with the pull. The page's minimal line has no volume, so the data is on the instance's root disk (the page's "Where to store data" shows `-v /my/own/datadir:/var/lib/mysql`) | imports (a stack and two instances: `db` and Adminer on 8080); a warning that `MARIADB_ROOT_PASSWORD` looks like a secret |
| Z-Wave JS UI | **not run**: it needs a Z-Wave stick. **Dry run** of the page's line, with `-it` and `--rm` dropped and `--mount source=...,target=...` written as `-v`: the serial device, two ports and the store volume all translate (finding 3) | refused: `stop_signal` and `tty` are not in the supported subset; `container_name` and `networks` are notes, `SESSION_SECRET` a secret warning |
| OpenThread border router | **not run**: it needs a radio, and the page's line cannot be given to `tink run` (findings 2 and 4). **Dry run** of the rest (two devices, `/data`, the env file, a restart policy) translates | none on the page |

**One of two compose examples imports; the other is refused for two keys.** Nothing in this batch is refused for host networking at import, because the only compose example that
exists is Z-Wave JS UI's and it publishes ports. The OpenThread page's line is the one that asks for it.

**Findings**

1. **A command after the image replaces the image's entrypoint, and the failure does not say so.** Piper's and Whisper's pages put their arguments after the image
   name (`rhasspy/wyoming-piper --voice en_US-lessac-medium`), which Docker appends to the image's entrypoint. The images' entrypoint is `bash docker_run.sh` (read from the
   registry on 2026-10-23). `tink run` set `oci.entrypoint` to the arguments, as `docs/docker-to-tink.md` says it does, and the instance never started: `starting/restarting piper to
   apply its config: giving up after 5 attempts: bad file descriptor`, and the container log says only `Failed to receive file descriptors for command "get_init_pid"`. Two of the
   four pages that can run here hit it. The way out is to write the entrypoint out. Whether `tink run` could say so when the image has an entrypoint was not looked at.
2. **The OpenThread page's line has two parts that `tink run` does not take.** `--network=host` is refused with the host-networking explanation, and `--cap-add=NET_ADMIN` is
   `unknown flag` (the importer drops `cap_add` with a warning). The page says host networking "is often necessary for OTBR, as it needs to have direct access to network
   interfaces" and gives `OT_INFRA_IF` as "the network interface used for the adjacent infrastructure network". It does not say layer 2 or multicast, so the corpus tags it
   `host-network` and not `same-lan`; whether it is a case for #206 is for the page to decide, and this one does not.
3. **Z-Wave JS UI is the one page whose line translates completely.** The device is a `unix-char` device with the path inside the container. Its compose example is refused only for
   two keys that say how the process is run (`stop_signal: SIGINT`, `tty: true`).
4. **The OpenThread page asks for a change to the host first** (step 2: enable IP forwarding with a `setup-host` script), tagged `host-setup`. Whether an Incus instance key can carry it
   (a sysctl) was not checked.
5. **`-it` as one flag is `unknown shorthand flag: 'i' in -it`.** `docs/docker-to-tink.md` says it does not apply; the four pages that use it (the voice trio and Z-Wave JS UI) were run without it.

**What was left out.** Z-Wave JS: the issue's "server and its UI" is one page, because the UI image contains the Z-Wave JS server and the page says so (it names the web socket
server on port 3000). The server alone (`zwave-js-server`) has its own repository and was not fetched. A recorder database: MariaDB only, as the issue said. PostgreSQL and InfluxDB were
not fetched; add one if a workload asks for it.

