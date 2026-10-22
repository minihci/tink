# Corpus: how popular self-hosted projects describe their install

Twenty-one projects' own install instructions, copied as published, one directory each (`NAME/upstream.md`). This is the
input to tink, before any tink stack exists for it: the docker run line or compose file a person finds in the project's
README or docs. Retrieved 2026-10-21 from the URL on each page.

**Why it exists.** tink's job is to take that kind of description and turn it into a running, wired-up service on an Incus
host. Progress is measured here: how many of these apps tink can bring up from a stack, and what each one that fails needs
that tink does not yet have. See [the roadmap](../docs/roadmap.md) for the "demand gate" this implements.

**What it is not.** These are not tink stacks, and nothing here is tested. The snippets stay under their projects'
licences and are quoted as test inputs. Where a page contained a template placeholder it is resolved and noted.

| App | Shape of the upstream description | Why it is in the set |
|---|---|---|
| [Vaultwarden](vaultwarden/upstream.md) | one container, run + compose | the simplest case; loopback-only port |
| [Uptime Kuma](uptime-kuma/upstream.md) | one container, run + compose | already explored (`docs/explore-uptime-kuma.md`) |
| [Audiobookshelf](audiobookshelf/upstream.md) | one container, compose | already explored |
| [Actual Budget](actual-budget/upstream.md) | one container, compose with healthcheck | small app, bind-mounted data |
| [Navidrome](navidrome/upstream.md) | one container, run + compose | `user:`, read-only media mount |
| [Grafana](grafana/upstream.md) | one container, run + compose | named volume, runs as a fixed uid |
| [n8n](n8n/upstream.md) | one container, run only | `--rm`, named volume |
| [Mosquitto](mosquitto/upstream.md) | one container, run only | config file must exist before first start |
| [Syncthing](syncthing/upstream.md) | one container, host network | host networking, healthcheck |
| [Home Assistant](home-assistant/upstream.md) | one container, run only | `--privileged`, host network, D-Bus |
| [Jellyfin](jellyfin/upstream.md) | one container, run + compose | UDP port, media mounts, optional GPU |
| [Pi-hole](pihole/upstream.md) | one container, compose | port 53, capabilities |
| [AdGuard Home](adguard-home/upstream.md) | one container, run only | many ports incl. UDP and DHCP |
| [Sonarr](sonarr/upstream.md) | one container, run + compose | `PUID`/`PGID`, shared download path |
| [qBittorrent](qbittorrent/upstream.md) | one container, run + compose | shares a path with Sonarr; UDP; stop timeout |
| [Portainer](portainer/upstream.md) | one container, run + compose | mounts the Docker socket, which Incus does not have |
| [Forgejo](forgejo/upstream.md) | one container, compose (database variants are diffs) | SSH and HTTP ports, optional database |
| [WordPress](wordpress/upstream.md) | two containers, compose | app + MySQL, named volumes |
| [Paperless-ngx](paperless-ngx/upstream.md) | three containers, compose + env file | app + database + broker, `env_file`, `depends_on` |
| [Nextcloud](nextcloud/upstream.md) | four containers, compose + env file | app + cron + database + cache sharing one volume |
| [Immich](immich/upstream.md) | multi-container, compose + env file | machine-learning, database, cache; GPU optional |

Not chosen on purpose: anything that only ships as a VM or an appliance image, and anything needing a Kubernetes chart.

## Shapes

[shapes.md](shapes.md) tags each app with what its description states (a UDP port, a bind mount, a sibling database, ...) and
counts the tags. The count ranks what tink needs; it does not yet say what tink already does.

## What the apps ask of the shared services

[platform.md](platform.md) tags each app with what it asks of the proxy, names, sign-on and backup (WebSockets, trusting the
proxy, apps that cannot follow a sign-on redirect, the database kind) and gives every volume a backup class. It is mostly
general knowledge of the apps and is marked as such: check it before relying on it.

## The herd test

[herd.md](herd.md) defines the measuring stick: the corpus applied to a shared site (proxy, names, sign-on, backup), one project and
one bridge per stack first, and what it takes for a stack to pass.

## Status

[status.md](status.md) runs each app's own line on a local Incus server and says, shape by shape, what tink does. Today 14
of 21 answer on their published port; none is wired (name, proxy route, sign-on, backup) yet.
