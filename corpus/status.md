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
