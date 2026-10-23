# Open Home Foundation Matter Server (python-matter-server) -- superseded

Upstream install instructions, verbatim. Retrieved 2026-10-23.

Source: https://github.com/home-assistant-libs/python-matter-server (README.md and docs/docker.md, main at 4c820ed12b; the repository is archived)

**Superseded.** The repository is archived, and its README says: "The Matter Server was rewritten and moved to matterjs-server! The version 8.1.2 is the final version of the python-matter-server, libraries, and docker-container. Please migrate to the new server as soon as possible, as the python-matter-server will no longer receive updates or support." It is kept here because it is what Home Assistant's add-on (and so most of the usage statistics) ran until the rewrite; the current server is `matterjs-server/`.

## docker run

```
mkdir data
docker run -d \
  --name matter-server \
  --restart=unless-stopped \
  --security-opt apparmor=unconfined \
  -v $(pwd)/data:/data \
  --network=host \
  ghcr.io/matter-js/python-matter-server:stable
```

To use local commissioning with Bluetooth, make sure to pass the D-Bus socket as well:

```sh
docker run -d \
  --name matter-server \
  --restart=unless-stopped \
  --security-opt apparmor=unconfined \
  -v $(pwd)/data:/data \
  -v /run/dbus:/run/dbus:ro \
  --network=host \
  ghcr.io/matter-js/python-matter-server:stable --storage-path /data --paa-root-cert-dir /data/credentials --bluetooth-adapter 0
```

## docker compose

```yaml
services:
  # python-matter-server
  matter-server:
    image: ghcr.io/matter-js/python-matter-server:stable
    container_name: matter-server
    restart: unless-stopped
    # Required for mDNS to work correctly
    network_mode: host
    security_opt:
      # Needed for Bluetooth via dbus
      - apparmor:unconfined
    volumes:
      # Create an .env file that sets the USERDIR environment variable.
      - ${USERDIR:-$HOME}/docker/matter-server/data:/data/
      # Required for Bluetooth via D-Bus
      - /run/dbus:/run/dbus:ro
    # If you adjust command line, make sure to pass the default CMD arguments too:
    #command: --storage-path /data --paa-root-cert-dir /data/credentials --bluetooth-adapter 0
```

## What the page says about the host (quoted)

> [!NOTE] **Attention Home Assistant users:** The docker image is provided as-is and without official support (due to all the complex requirements to the underlying host/OS). Use it at your own risk if you know what you're doing.

> If you still prefer a self-managed container installation, you might experience
> communication issues with Matter devices, especially Thread based devices.
> This is mostly because the container installation uses host networking, and
> relies on the networking managed by your operating system.
