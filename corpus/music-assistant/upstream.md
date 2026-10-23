# Music Assistant server (Docker)

Upstream install instructions, verbatim. Retrieved 2026-10-23.

Source: https://music-assistant.io/installation/ (source: github.com/music-assistant/music-assistant.io, src/content/docs/installation.md, main at a97a3afd39)

## docker run

The page says: "An alternative way to run the Music Assistant server is by running the docker image:"

```
docker run -v <dir>:/data --network host ghcr.io/music-assistant/server
```

## docker compose ("recommended")

```yaml
services:
  music-assistant-server:
    image: ghcr.io/music-assistant/server:latest # <<< Desired release version here (or use beta to get the latest beta version)
    container_name: music-assistant-server
    restart: unless-stopped
    # Network mode must be set to host for MA to discover and stream to players (see networking note below)
    network_mode: host
    volumes:
      - ${USERDIR:-$HOME}/docker/music-assistant-server/data:/data/
      # Optional: expose local music to MA by bind-mounting it read-only
      - /path/to/your/music:/media:ro
    environment:
      # Provide logging level as environment variable.
      # default=info, possible=(critical, error, warning, info, debug)
      - LOG_LEVEL=info

```

## What the page says about the host (quoted)

> **Your network.** The server discovers and streams to players using multicast (mDNS and uPnP), so it **must be on the same layer 2 network as your players**, with no VLANs between them. See [Networking Basics](/faq/networking/) for what these terms mean. This is the single most common cause of support requests, and no installation method can work around it.

> You must run the docker container with **host network mode** (see the note on networking below). The data volume is `/data` - replace `<dir>` with a writable directory to ensure the data volume persists between updates. If you want access to your local music files from within MA, make sure to also mount that local directory, e.g. `/media` (mount it read-only where possible).

> `network_mode: host` gives the container direct (layer 2) access to your network. Music Assistant relies on this for local player discovery (mDNS/uPnP, [explained in Networking Basics](/faq/networking/)) and for streaming to and interacting with networked audio devices (AirPlay, Chromecast, DLNA, Sonos), which open random TCP/UDP ports. This is why host networking (or macvlan) is a supported requirement - see the support notes below.

Without host networking, the page's own alternative:

```
    ports:
      - "8095:8095"
      - "8097:8097"
```

> Be aware of the trade-off: on a bridge network, player discovery and any players that need direct network access (AirPlay, Chromecast, DLNA, Sonos, and similar) will not work, and this configuration is not supported by the MA team.

The extra privileges the page shows for letting the server mount a share itself (it recommends a host mount instead):

```
    # WARNING: only needed to mount SMB/NFS shares from inside the container.
    # These reduce container isolation - prefer host-mounting the share instead.
    cap_add:
      - SYS_ADMIN
      - DAC_READ_SEARCH
    security_opt:
      - apparmor:unconfined
```

From "Supported installations":

- **Home Assistant App on HAOS.** Fully supported, on dedicated hardware or in a VM.
- **A simple standalone docker container**, installed as described above. Not Kubernetes, not a stack of orchestration.

In either case:

- MA, HA and all players must be on the same flat network (or VLAN)
- Music Assistant needs direct (layer 2) access to the network to properly discover and stream to players, so either host networking or macvlan networking is a mandatory requirement for the docker container
- Any restriction of the available ports, such as running MA behind a [firewall](/faq/networking/#the-jargon-translated), is not supported, because protocols like AirPlay open random TCP and UDP ports

Everything else is unsupported. If you run into a problem on a docker install, try running Home Assistant OS in a VM or on a spare RPi and see whether it happens there too.
