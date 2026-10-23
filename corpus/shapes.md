# Shapes: what each app's upstream description needs

Every app in this directory, tagged from what its `upstream.md` states, and no more. A tag is a *fact the description
contains* (a UDP port, a bind mount, a sibling database); it says nothing yet about whether tink handles it. A tag
with `?` is shown only as an option or a variant. Restart policy is left out because nearly every page sets one.

## By shape, most common first

Counts are of 21 apps. "Optional" counts the `?` tags.

| Shape | Apps | Optional | Meaning | Apps with it |
|---|---|---|---|---|
| `web-ui` | 20 |  | an HTTP(S) interface a person browses to (what a proxy, a name and sign-on would front) | vaultwarden, uptime-kuma, audiobookshelf, actual-budget, navidrome, grafana, n8n, syncthing, home-assistant, jellyfin, pihole, adguard-home, sonarr, qbittorrent, portainer, forgejo, wordpress, paperless-ngx, nextcloud, immich |
| `tcp-port` | 19 |  | a published TCP port | vaultwarden, uptime-kuma, audiobookshelf, actual-budget, navidrome, grafana, n8n, mosquitto, jellyfin, pihole, adguard-home, sonarr, qbittorrent, portainer, forgejo, wordpress, paperless-ngx, nextcloud, immich |
| `host-dir` | 16 |  | a bind mount of a host directory | vaultwarden, uptime-kuma, audiobookshelf, actual-budget, navidrome, mosquitto, syncthing, home-assistant, jellyfin, pihole, adguard-home, sonarr, qbittorrent, forgejo, paperless-ngx, immich |
| `env-config` | 12 | 2 | settings in environment variables | vaultwarden, grafana, syncthing, home-assistant, jellyfin, pihole, sonarr, qbittorrent, forgejo, wordpress, paperless-ngx, nextcloud, actual-budget?, navidrome? |
| `registry-named` | 9 |  | the image names a registry other than Docker Hub's default | audiobookshelf, actual-budget, n8n, home-assistant, sonarr, qbittorrent, forgejo, paperless-ngx, immich |
| `named-volume` | 8 |  | a named volume for state | uptime-kuma, grafana, n8n, portainer, wordpress, paperless-ngx, nextcloud, immich |
| `uid-gid` | 6 |  | runs as, or maps files to, a specific user (user:, PUID, USER_UID) | navidrome, syncthing, jellyfin, sonarr, qbittorrent, forgejo |
| `secret` | 5 | 1 | a password or key in the environment | pihole, wordpress, paperless-ngx, nextcloud, immich, forgejo? |
| `sibling` | 4 | 1 | other containers of its own (database, cache, broker) | wordpress, paperless-ngx, nextcloud, immich, forgejo? |
| `media-lib` | 4 |  | a library of existing files (music, video, books) mounted in | audiobookshelf, navidrome, jellyfin, sonarr |
| `ro-mount` | 4 |  | a read-only mount | navidrome, home-assistant, jellyfin, forgejo |
| `udp-port` | 4 |  | a published UDP port | jellyfin, pihole, adguard-home, qbittorrent |
| `depends-on` | 3 | 1 | start ordering between them | paperless-ngx, nextcloud, immich, forgejo? |
| `env-file` | 3 |  | an env file next to the compose file | paperless-ngx, nextcloud, immich |
| `healthcheck` | 3 |  | a healthcheck | actual-budget, syncthing, immich |
| `public-url` | 3 |  | the app is told its own outside address or domain | vaultwarden, grafana, jellyfin |
| `shared-data` | 3 |  | the same data used by more than one container or app | sonarr, qbittorrent, nextcloud |
| `host-network` | 2 | 2 | host networking | syncthing, home-assistant, jellyfin?, adguard-home? |
| `ephemeral` | 2 |  | -it / --rm | n8n, mosquitto |
| `host-file` | 2 |  | a single host file mounted in (/etc/localtime) | forgejo, immich |
| `host-socket` | 2 |  | a host socket mounted in (docker.sock, D-Bus) | home-assistant, portainer |
| `loopback-port` | 2 |  | a port bound to 127.0.0.1 only | vaultwarden, nextcloud |
| `low-port` | 2 |  | a host port below 1024 (53, 80, 443) | pihole, adguard-home |
| `stop-timeout` | 1 | 1 | a longer time to stop cleanly | home-assistant, qbittorrent? |
| `cap-add` | 1 |  | extra Linux capabilities | pihole |
| `cmd-override` | 1 |  | a command or entrypoint override | nextcloud |
| `compose-vars` | 1 |  | ${VAR} substitution from a .env file | immich |
| `digest-pin` | 1 |  | an image pinned by digest | immich |
| `privileged` | 1 |  | privileged | home-assistant |
| `shm-tmpfs` | 1 |  | shared memory size or tmpfs | immich |
| `device` | 0 | 2 | a hardware device (GPU, serial, a USB radio) | immich?, home-assistant? |
| `bluetooth` | 0 | 1 | the host's Bluetooth stack (reached through D-Bus) | home-assistant? |
| `file-before-start` | 0 | 1 | a config file expected to exist before the first start | mosquitto? |

## By app

| App | Tags |
|---|---|
| vaultwarden | `web-ui` `tcp-port` `loopback-port` `public-url` `host-dir` `env-config` |
| uptime-kuma | `web-ui` `tcp-port` `named-volume` `host-dir` |
| audiobookshelf | `web-ui` `tcp-port` `host-dir` `media-lib` `registry-named` |
| actual-budget | `web-ui` `tcp-port` `host-dir` `healthcheck` `env-config?` `registry-named` |
| navidrome | `web-ui` `tcp-port` `host-dir` `ro-mount` `media-lib` `uid-gid` `env-config?` |
| grafana | `web-ui` `tcp-port` `public-url` `named-volume` `env-config` |
| n8n | `web-ui` `tcp-port` `named-volume` `ephemeral` `registry-named` |
| mosquitto | `tcp-port` `host-dir` `file-before-start?` `ephemeral` |
| syncthing | `web-ui` `host-network` `host-dir` `uid-gid` `healthcheck` `env-config` |
| home-assistant | `web-ui` `host-network` `host-dir` `ro-mount` `host-socket` `privileged` `stop-timeout` `device?` `bluetooth?` `env-config` `registry-named` |
| jellyfin | `web-ui` `tcp-port` `udp-port` `host-network?` `public-url` `host-dir` `ro-mount` `media-lib` `uid-gid` `env-config` |
| pihole | `web-ui` `tcp-port` `udp-port` `low-port` `host-dir` `cap-add` `env-config` `secret` |
| adguard-home | `web-ui` `tcp-port` `udp-port` `low-port` `host-network?` `host-dir` |
| sonarr | `web-ui` `tcp-port` `host-dir` `media-lib` `shared-data` `uid-gid` `env-config` `registry-named` |
| qbittorrent | `web-ui` `tcp-port` `udp-port` `host-dir` `shared-data` `uid-gid` `stop-timeout?` `env-config` `registry-named` |
| portainer | `web-ui` `tcp-port` `named-volume` `host-socket` |
| forgejo | `web-ui` `tcp-port` `host-dir` `ro-mount` `host-file` `uid-gid` `env-config` `secret?` `sibling?` `depends-on?` `registry-named` |
| wordpress | `web-ui` `tcp-port` `named-volume` `env-config` `secret` `sibling` |
| paperless-ngx | `web-ui` `tcp-port` `named-volume` `host-dir` `env-config` `env-file` `secret` `sibling` `depends-on` `registry-named` |
| nextcloud | `web-ui` `tcp-port` `loopback-port` `named-volume` `shared-data` `cmd-override` `env-config` `env-file` `secret` `sibling` `depends-on` |
| immich | `web-ui` `tcp-port` `named-volume` `host-dir` `host-file` `device?` `healthcheck` `shm-tmpfs` `env-file` `secret` `sibling` `depends-on` `compose-vars` `registry-named` `digest-pin` |

## Shapes the integrations ask for (defined here, not yet counted)

The 21 pages above state almost nothing about how an app finds its devices: no page says it needs multicast, and the closest are Jellyfin's
"autodiscovery" address and Pi-hole's optional DHCP-server role, which are settings, not a need. Home Assistant's own page gives the container
the host's network and never says why. The reason is on the integrations' side, and Home Assistant publishes it: each integration's manifest says
how it finds its devices, and its usage statistics say how many installations run it.

These shapes are named so the pages added next (the companions, #204) can be tagged with them when their text states the need. They are **not** in the
counts above, because a tag here is a fact a page contains and these come from manifests. The counts below are from the survey of 2026-10-23 on
#201 (method and script: [home-assistant/integrations.md](home-assistant/integrations.md)) (the top 200 integrations by installs, less the `default_config` bundle, which depends on all of them: 199), and will change.

| Shape | Meaning | Integrations in the top 199 |
|---|---|---|
| `discover-mdns` | finds devices by multicast DNS on the LAN (zeroconf, HomeKit), so it must hear the LAN's multicast | 39 |
| `discover-ssdp` | finds devices by SSDP/UPnP multicast | 18 |
| `discover-dhcp` | finds new devices by watching DHCP requests on the LAN, so it must see the LAN's broadcasts | 25 |
| `bluetooth` | needs the host's Bluetooth adapter (already a tag above, from the one page that says so) | 11 |
| `device` | a USB radio or serial device (Zigbee, Z-Wave); already a tag above | 6 find devices by USB |

What these have in common is a presence on the LAN, not a port: a bridge that NATs gives none of the three discoveries. `docs/docker-to-tink.md` says
what the Incus answer is (a NIC of its own on the LAN bridge) and #206 is the proposal to declare it. Names are provisional until a page uses them.

