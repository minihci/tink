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
6. **`read_only` and `security_opt` are in no subset.** Neither is a `tink run` flag nor an importer key; Incus has no per-instance read-only root.

