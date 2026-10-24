# Shapes: what each app's upstream description needs

Every app in this directory, tagged from what its `upstream.md` states, and no more. A tag is a *fact the description
contains* (a UDP port, a bind mount, a sibling database); it says nothing yet about whether tink handles it. A tag
with `?` is shown only as an option or a variant. Restart policy is left out because nearly every page sets one.

## By shape, most common first

Counts are of 34 apps. "Optional" counts the `?` tags.

| Shape | Apps | Optional | Meaning | Apps with it |
|---|---|---|---|---|
| `web-ui` | 27 | 2 | an HTTP(S) interface a person browses to (what a proxy, a name and sign-on would front) | vaultwarden, uptime-kuma, audiobookshelf, actual-budget, navidrome, grafana, n8n, syncthing, home-assistant, jellyfin, pihole, adguard-home, sonarr, qbittorrent, portainer, forgejo, wordpress, paperless-ngx, nextcloud, immich, matterjs-server, esphome, zigbee2mqtt, frigate, node-red, music-assistant, zwave-js-ui, piper?, mariadb? |
| `tcp-port` | 27 | 2 | a published TCP port | vaultwarden, uptime-kuma, audiobookshelf, actual-budget, navidrome, grafana, n8n, mosquitto, jellyfin, pihole, adguard-home, sonarr, qbittorrent, portainer, forgejo, wordpress, paperless-ngx, nextcloud, immich, zigbee2mqtt, frigate, node-red, zwave-js-ui, piper, whisper, openwakeword, mariadb, esphome?, music-assistant? |
| `host-dir` | 26 | 2 | a bind mount of a host directory | vaultwarden, uptime-kuma, audiobookshelf, actual-budget, navidrome, mosquitto, syncthing, home-assistant, jellyfin, pihole, adguard-home, sonarr, qbittorrent, forgejo, paperless-ngx, immich, matterjs-server, python-matter-server, esphome, zigbee2mqtt, frigate, music-assistant, openthread-border-router, piper, whisper, mariadb, zwave-js-ui?, openwakeword? |
| `env-config` | 22 | 3 | settings in environment variables | vaultwarden, grafana, syncthing, home-assistant, jellyfin, pihole, sonarr, qbittorrent, forgejo, wordpress, paperless-ngx, nextcloud, matterjs-server, esphome, zigbee2mqtt, frigate, node-red, music-assistant, zwave-js-ui, openthread-border-router, whisper, mariadb, actual-budget?, navidrome?, piper? |
| `registry-named` | 15 |  | the image names a registry other than Docker Hub's default | audiobookshelf, actual-budget, n8n, home-assistant, sonarr, qbittorrent, forgejo, paperless-ngx, immich, matterjs-server, python-matter-server, esphome, zigbee2mqtt, frigate, music-assistant |
| `named-volume` | 10 | 1 | a named volume for state | uptime-kuma, grafana, n8n, portainer, wordpress, paperless-ngx, nextcloud, immich, node-red, zwave-js-ui, mariadb? |
| `secret` | 9 | 1 | a password or key in the environment | pihole, wordpress, paperless-ngx, nextcloud, immich, esphome, frigate, zwave-js-ui, mariadb, forgejo? |
| `ro-mount` | 8 |  | a read-only mount | navidrome, home-assistant, jellyfin, forgejo, esphome, zigbee2mqtt, frigate, music-assistant |
| `host-network` | 7 | 2 | host networking | syncthing, home-assistant, matterjs-server, python-matter-server, esphome, music-assistant, openthread-border-router, jellyfin?, adguard-home? |
| `uid-gid` | 7 | 1 | runs as, or maps files to, a specific user (user:, PUID, USER_UID) | navidrome, syncthing, jellyfin, sonarr, qbittorrent, forgejo, matterjs-server, zigbee2mqtt? |
| `ephemeral` | 7 | 1 | -it / --rm | n8n, mosquitto, esphome, zwave-js-ui, piper, whisper, openwakeword, mariadb? |
| `healthcheck` | 6 | 1 | a healthcheck | actual-budget, syncthing, immich, piper, whisper, mariadb, zwave-js-ui? |
| `media-lib` | 5 |  | a library of existing files (music, video, books) mounted in | audiobookshelf, navidrome, jellyfin, sonarr, music-assistant |
| `udp-port` | 5 |  | a published UDP port | jellyfin, pihole, adguard-home, qbittorrent, frigate |
| `sibling` | 4 | 5 | other containers of its own (database, cache, broker) | wordpress, paperless-ngx, nextcloud, immich, forgejo?, zigbee2mqtt?, frigate?, node-red?, mariadb? |
| `compose-vars` | 4 |  | ${VAR} substitution from a .env file | immich, matterjs-server, python-matter-server, music-assistant |
| `discover-mdns` | 4 |  | finds devices by multicast DNS on the LAN, so it needs the LAN's multicast (the pages say host networking) | matterjs-server, python-matter-server, esphome, music-assistant |
| `host-file` | 4 |  | a single host file mounted in (/etc/localtime) | forgejo, immich, esphome, frigate |
| `env-file` | 4 |  | an env file next to the compose file | paperless-ngx, nextcloud, immich, openthread-border-router |
| `device` | 3 | 6 | a hardware device (GPU, serial, a USB radio) | zigbee2mqtt, zwave-js-ui, openthread-border-router, home-assistant?, immich?, esphome?, frigate?, piper?, whisper? |
| `host-socket` | 3 | 2 | a host socket or runtime directory mounted in (docker.sock, D-Bus, udev) | home-assistant, portainer, zigbee2mqtt, matterjs-server?, python-matter-server? |
| `depends-on` | 3 | 1 | start ordering between them | paperless-ngx, nextcloud, immich, forgejo? |
| `public-url` | 3 |  | the app is told its own outside address or domain | vaultwarden, grafana, jellyfin |
| `shared-data` | 3 |  | the same data used by more than one container or app | sonarr, qbittorrent, nextcloud |
| `cap-add` | 2 | 3 | extra Linux capabilities | pihole, openthread-border-router, matterjs-server?, frigate?, music-assistant? |
| `privileged` | 2 | 1 | privileged | home-assistant, esphome, frigate? |
| `stop-timeout` | 2 | 1 | a longer time to stop cleanly | home-assistant, frigate, qbittorrent? |
| `loopback-port` | 2 |  | a port bound to 127.0.0.1 only | vaultwarden, nextcloud |
| `low-port` | 2 |  | a host port below 1024 (53, 80, 443) | pihole, adguard-home |
| `same-lan` | 2 |  | must be on the same layer 2 network (LAN or VLAN) as the devices it talks to; a bridge that translates addresses does not give that | matterjs-server, music-assistant |
| `shm-tmpfs` | 2 |  | shared memory size or tmpfs | immich, frigate |
| `security-opt` | 1 | 2 | a container security option (apparmor, no-new-privileges) | python-matter-server, frigate?, music-assistant? |
| `cmd-override` | 1 | 1 | a command or entrypoint override | nextcloud, mariadb? |
| `digest-pin` | 1 |  | an image pinned by digest | immich |
| `discover-ssdp` | 1 |  | finds devices by SSDP/uPnP multicast | music-assistant |
| `ipv6-multicast` | 1 |  | needs IPv6 link-local multicast, router advertisements and neighbour discovery on the LAN (Matter, Thread) | matterjs-server |
| `read-only-root` | 1 |  | a read-only root filesystem | matterjs-server |
| `host-setup` | 1 |  | a change to the host that the page asks for before the container runs (a sysctl, a script) | openthread-border-router |
| `bluetooth` | 0 | 3 | the host's Bluetooth stack (reached through D-Bus) | home-assistant?, matterjs-server?, python-matter-server? |
| `file-before-start` | 0 | 1 | a config file expected to exist before the first start | mosquitto? |
| `discover-dhcp` | 0 |  | finds devices by watching DHCP requests on the LAN (defined from the integrations' manifests; no page states it yet) |  |

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
| matterjs-server | `web-ui` `host-network` `host-dir` `env-config` `registry-named` `uid-gid` `read-only-root` `compose-vars` `discover-mdns` `ipv6-multicast` `same-lan` `host-socket?` `bluetooth?` `cap-add?` |
| python-matter-server | `host-network` `host-dir` `registry-named` `security-opt` `compose-vars` `discover-mdns` `host-socket?` `bluetooth?` |
| esphome | `web-ui` `tcp-port?` `host-network` `host-dir` `host-file` `ro-mount` `privileged` `ephemeral` `env-config` `secret` `registry-named` `discover-mdns` `device?` |
| zigbee2mqtt | `web-ui` `tcp-port` `host-dir` `ro-mount` `host-socket` `device` `env-config` `registry-named` `sibling?` `uid-gid?` |
| frigate | `web-ui` `tcp-port` `udp-port` `host-dir` `host-file` `ro-mount` `shm-tmpfs` `stop-timeout` `env-config` `secret` `registry-named` `sibling?` `device?` `privileged?` `security-opt?` `cap-add?` |
| node-red | `web-ui` `tcp-port` `named-volume` `env-config` `sibling?` |
| music-assistant | `web-ui` `tcp-port?` `host-network` `host-dir` `ro-mount` `media-lib` `env-config` `registry-named` `compose-vars` `discover-mdns` `discover-ssdp` `same-lan` `cap-add?` `security-opt?` |
| zwave-js-ui | `web-ui` `tcp-port` `named-volume` `host-dir?` `device` `env-config` `secret` `ephemeral` `healthcheck?` |
| openthread-border-router | `host-network` `cap-add` `device` `host-dir` `env-file` `env-config` `host-setup` |
| piper | `tcp-port` `host-dir` `ephemeral` `healthcheck` `env-config?` `web-ui?` `device?` |
| whisper | `tcp-port` `host-dir` `ephemeral` `healthcheck` `env-config` `device?` |
| openwakeword | `tcp-port` `host-dir?` `ephemeral` |
| mariadb | `tcp-port` `env-config` `secret` `host-dir` `healthcheck` `named-volume?` `sibling?` `web-ui?` `ephemeral?` `cmd-override?` |

## Shapes the integrations ask for

The first 21 pages state almost nothing about how an app finds its devices: no page says it needs multicast, and the closest are Jellyfin's
"autodiscovery" address and Pi-hole's optional DHCP-server role, which are settings, not a need. Home Assistant's own page gives the container
the host's network and never says why. The reason is on the integrations' side, and Home Assistant publishes it: each integration's manifest says
how it finds its devices, and its usage statistics say how many installations run it.

The companions added since (#204, #212) do state it, and their tags are counted above: ESPHome ("`--net=host` lets the dashboard use mDNS, which is
how it discovers devices"), the Matter servers (mDNS, and "Matter is based on IPv6 link-local multicast protocols"), and Music Assistant ("discovers and
streams to players using multicast (mDNS and uPnP), so it must be on the same layer 2 network as your players"). So `discover-mdns`, `discover-ssdp`,
`ipv6-multicast` and `same-lan` are tags with pages behind them. `discover-dhcp` is not: no page states it yet, and it stays defined from the manifests alone.

For scale, the table below is the integrations' side, from the manifests in the survey of 2026-10-23 on #201 (method and script:
[home-assistant/integrations.md](home-assistant/integrations.md); the top 200 integrations by installs, less the `default_config` bundle, which depends on all
of them: 199). It counts integrations, not apps in this corpus, and it will change.

| Shape | Meaning | Integrations in the top 199 |
|---|---|---|
| `discover-mdns` | finds devices by multicast DNS on the LAN (zeroconf, HomeKit), so it must hear the LAN's multicast | 39 |
| `discover-ssdp` | finds devices by SSDP/UPnP multicast | 18 |
| `discover-dhcp` | finds new devices by watching DHCP requests on the LAN, so it must see the LAN's broadcasts | 25 |
| `bluetooth` | needs the host's Bluetooth adapter | 11 |
| `device` | a USB radio or serial device (Zigbee, Z-Wave) | 6 find devices by USB |

What these have in common is a presence on the LAN, not a port: a bridge that NATs gives none of the discoveries, and Music Assistant and the Matter server
say so in their own words (their pages call host networking, or a network interface directly on the LAN, a requirement). `docs/docker-to-tink.md` says
what the Incus answer is (a NIC of its own on the LAN bridge) and #206 is the proposal to declare it.
