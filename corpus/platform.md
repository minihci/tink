# What the apps ask of the platform

`shapes.md` says what an app needs to *run*. This page says what it asks of the services a site shares: the reverse
proxy, names, sign-on and backup. These are the facts that take an internet example from one container to part of a
managed site, and they are what the derive stage (Phase 3) and the volume backup policy have to carry.

**Read it with care.** Unlike `shapes.md`, most of this is not stated on the install pages. It is general knowledge of
the apps, written down so it can be checked. A tag with `?` is the least sure; none was tested against a proxy. Which
version of each app matters (OIDC support in particular has changed). Treat the backup classes as a proposal to correct.

The one fact here that was run: Incus refuses a second instance with the same DNS name on a network, across projects (see
[status.md](status.md), finding 5).

## Proxy, sign-on and backup tags, most common first

Counts are of 21 apps; the third column counts the `?` tags.

| Tag | Apps | Unsure or optional | Meaning | Apps with it |
|---|---|---|---|---|
| `embedded-db` | 16 |  | state is a file database (sqlite, leveldb) inside a volume | vaultwarden, uptime-kuma, audiobookshelf, actual-budget, navidrome, grafana, n8n, mosquitto, syncthing, home-assistant, jellyfin, pihole, adguard-home, sonarr, portainer, forgejo |
| `app-clients` | 12 |  | non-browser clients (mobile apps, sync, git, API) that cannot follow a sign-on redirect | vaultwarden, audiobookshelf, actual-budget, navidrome, home-assistant, jellyfin, sonarr, qbittorrent, forgejo, paperless-ngx, nextcloud, immich |
| `admin-ui` | 9 |  | an administration interface that should sit behind sign-on and not be public by default | uptime-kuma, grafana, n8n, syncthing, pihole, adguard-home, sonarr, qbittorrent, portainer |
| `stream-port` | 7 | 1 | listens on ports that are not HTTP (ssh, mqtt, dns, sync), so no proxy route carries them | mosquitto, syncthing, jellyfin, pihole, adguard-home, qbittorrent, forgejo, home-assistant? |
| `oidc` | 4 | 6 | can sign users in through OIDC itself | grafana, forgejo, paperless-ngx, immich, vaultwarden?, audiobookshelf?, actual-budget?, jellyfin?, portainer?, nextcloud? |
| `db-server` | 4 | 1 | a database server in its own container | wordpress, paperless-ngx, nextcloud, immich, forgejo? |
| `big-upload` | 3 | 2 | takes large uploads (body size and timeouts) | paperless-ngx, nextcloud, immich, forgejo?, wordpress? |
| `public-paths` | 2 | 2 | part of the app is meant to be reached without signing on (a public site, webhooks, a status page) | n8n, wordpress, uptime-kuma?, forgejo? |
| `is-dns` | 2 |  | is itself a DNS server, which overlaps with the site's DNS | pihole, adguard-home |
| `trust-proxy` | 1 | 4 | must be told the proxy's address, or it refuses or misreports the client and the scheme | nextcloud, home-assistant?, forgejo?, wordpress?, paperless-ngx? |
| `https-upstream` | 1 | 1 | its web port speaks TLS (the proxy must connect over TLS) | portainer, pihole? |
| `websocket` | 0 | 12 | needs WebSocket upgrade through the proxy | vaultwarden?, uptime-kuma?, audiobookshelf?, grafana?, n8n?, home-assistant?, jellyfin?, sonarr?, portainer?, paperless-ngx?, nextcloud?, immich? |

## By app

| App | Tags | Told its public address by |
|---|---|---|
| vaultwarden | `websocket?` `app-clients` `oidc?` `embedded-db` | DOMAIN (page) |
| uptime-kuma | `websocket?` `admin-ui` `public-paths?` `embedded-db` | none; a trust-proxy setting in the UI (?) |
| audiobookshelf | `websocket?` `app-clients` `oidc?` `embedded-db` | none |
| actual-budget | `app-clients` `oidc?` `embedded-db` | none |
| navidrome | `app-clients` `embedded-db` | ND_BASEURL for a subpath only (?) |
| grafana | `websocket?` `admin-ui` `oidc` `embedded-db` | GF_SERVER_ROOT_URL (page) |
| n8n | `websocket?` `admin-ui` `public-paths` `embedded-db` | WEBHOOK_URL, N8N_HOST, N8N_PROTOCOL (?) |
| mosquitto | `stream-port` `embedded-db` | n/a (not HTTP) |
| syncthing | `stream-port` `admin-ui` `embedded-db` | none; the GUI rejects an unexpected Host header (?) |
| home-assistant | `websocket?` `trust-proxy?` `stream-port?` `app-clients` `embedded-db` | http: trusted_proxies in a config file (?) |
| jellyfin | `websocket?` `stream-port` `app-clients` `oidc?` `embedded-db` | JELLYFIN_PublishedServerUrl (page) |
| pihole | `https-upstream?` `stream-port` `is-dns` `admin-ui` `embedded-db` | none |
| adguard-home | `stream-port` `is-dns` `admin-ui` `embedded-db` | none |
| sonarr | `websocket?` `admin-ui` `app-clients` `embedded-db` | none; URL base for a subpath (?) |
| qbittorrent | `stream-port` `admin-ui` `app-clients` | none |
| portainer | `websocket?` `https-upstream` `admin-ui` `oidc?` `embedded-db` | none |
| forgejo | `big-upload?` `trust-proxy?` `stream-port` `app-clients` `public-paths?` `oidc` `embedded-db` `db-server?` | FORGEJO__server__ROOT_URL, DOMAIN, SSH_DOMAIN (?) |
| wordpress | `big-upload?` `trust-proxy?` `public-paths` `db-server` | WP_HOME, WP_SITEURL, via WORDPRESS_CONFIG_EXTRA (?) |
| paperless-ngx | `websocket?` `big-upload` `trust-proxy?` `app-clients` `oidc` `db-server` | PAPERLESS_URL (?) |
| nextcloud | `websocket?` `big-upload` `trust-proxy` `app-clients` `oidc?` `db-server` | OVERWRITEHOST, OVERWRITEPROTOCOL, TRUSTED_PROXIES, NEXTCLOUD_TRUSTED_DOMAINS (?) |
| immich | `websocket?` `big-upload` `app-clients` `oidc` `db-server` | none required (?) |

## Backup: what each volume is

`keep`: losing it loses something for good. `rebuild`: the app can make it again. `external`: files that something else
owns and backs up. `scratch`: transient. A **consistent copy** matters where a database server writes (see `db-server`);
whether a crash-consistent snapshot is enough for those is an open question, not decided here.

| App | Volume | Class | Why |
|---|---|---|---|
| vaultwarden | `/data` | keep | the whole vault |
| uptime-kuma | `/app/data` | keep | monitors and history; small |
| audiobookshelf | `/config` | keep | users, progress; small |
| audiobookshelf | `/metadata` | rebuild | covers, cache; also holds the app's own backups |
| audiobookshelf | `/audiobooks` | external | the library |
| audiobookshelf | `/podcasts` | rebuild | re-downloadable |
| actual-budget | `/data` | keep | the budget files |
| navidrome | `/data` | keep | database and playlists; the cache inside is rebuildable |
| navidrome | `/music` | external | the library |
| grafana | `/var/lib/grafana` | keep | dashboards and users, unless dashboards are provisioned from files |
| n8n | `/home/node/.n8n` | keep | workflows and the encryption key; losing the key loses every stored credential |
| mosquitto | `/mosquitto/config` | rebuild | written by the stack |
| mosquitto | `/mosquitto/data` | rebuild | retained messages |
| mosquitto | `/mosquitto/log` | scratch |  |
| syncthing | `/var/syncthing` | keep | the device identity key and config; the synced folders are copies of other devices' data |
| home-assistant | `/config` | keep | configuration, automations and the history database (the history is large and rebuildable) |
| jellyfin | `/config` | keep | users, libraries, watch state; small |
| jellyfin | `/cache` | rebuild | transcodes, images |
| jellyfin | `/media` | external | the library |
| pihole | `/etc/pihole` | keep | settings and local DNS records; the block lists are rebuildable |
| adguard-home | `/opt/adguardhome/conf` | keep | AdGuardHome.yaml |
| adguard-home | `/opt/adguardhome/work` | rebuild | statistics and query log |
| sonarr | `/config` | keep | database and settings; small |
| sonarr | `/tv` | external | the library |
| sonarr | `/downloads` | scratch | shared with the download client |
| qbittorrent | `/config` | keep | settings and torrent state; small |
| qbittorrent | `/downloads` | scratch | shared with Sonarr |
| portainer | `/data` | keep | settings; the app cannot run here anyway |
| forgejo | `/data` | keep | repositories, database (sqlite by default), attachments |
| wordpress | `html` | keep | uploads, themes, plugins |
| wordpress | `db` | keep | the database; a consistent copy matters |
| paperless-ngx | `media` | keep | the documents |
| paperless-ngx | `pgdata` | keep | the database; a consistent copy matters |
| paperless-ngx | `data` | rebuild | search index and classifier, rebuilt by reindexing |
| paperless-ngx | `redisdata` | scratch | queue |
| paperless-ngx | `consume` | scratch | drop folder |
| paperless-ngx | `export` | external | an export for backup, by design |
| nextcloud | `nextcloud` | keep | config, apps and, by default, every user's files; large |
| nextcloud | `db` | keep | the database; a consistent copy matters |
| immich | `UPLOAD_LOCATION` | keep | the photo library; large |
| immich | `DB_DATA_LOCATION` | keep | the database; a consistent copy matters |
| immich | `model-cache` | rebuild | downloaded models |

42 volumes in all: keep 24, rebuild 8, external 5, scratch 5.
Every app but one has at least one `keep` volume (20 of 21; Mosquitto has none, because its config comes from the stack).
