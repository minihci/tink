# Home Assistant (Container)

Upstream install instructions, verbatim. Retrieved 2026-10-21; the compose form and the notes below it added 2026-10-23.

Source: https://www.home-assistant.io/installation/linux/ (source: github.com/home-assistant/home-assistant.io, source/_includes/installation/container/cli.md, compose.md and container.md, current; the 2026-10-23 retrieval is of commit 3e2946107a)

## docker run

```sh
docker run -d \
  --name homeassistant \
  --privileged \
  --restart=unless-stopped \
  -e TZ=MY_TIME_ZONE \
  -v /PATH_TO_YOUR_CONFIG:/config \
  -v /run/dbus:/run/dbus:ro \
  --network=host \
  ghcr.io/home-assistant/home-assistant:stable
```

(The page also says: "To give the shutdown more time, increase the timeout" with `docker run ... --stop-timeout 60 ...`.)

The docs source writes the image as a template, `{{ site.installation.container }}:{{ include.tag | default: 'stable' }}`; it is filled in above from the site config (`ghcr.io/home-assistant/home-assistant`, tag `stable`).

## docker compose

The page says: "As the Docker command becomes more complex, switching to `docker compose` can be preferable and support automatically restarting on failure or system restart. Create a `compose.yaml` file:"

```yaml
  services:
    homeassistant:
      container_name: homeassistant
      image: "ghcr.io/home-assistant/home-assistant:stable"
      volumes:
        - /PATH_TO_YOUR_CONFIG:/config
        - /etc/localtime:/etc/localtime:ro
        - /run/dbus:/run/dbus:ro
      restart: unless-stopped
      stop_grace_period: 60s
      privileged: true
      network_mode: host
      environment:
        TZ: Europe/Amsterdam
```

(The include is indented two spaces in the docs source and is kept so; it is valid YAML as it is. Its image is the same template as above, `"{{ include.image | default: site.installation.container }}:{{ include.tag | default: 'stable' }}"`, filled in the same way.)

## What else the page says

On the prerequisites and the D-Bus mount: "D-Bus is optional but required if you plan to use the [Bluetooth integration](/integrations/bluetooth)." And, of the Docker version: "If you are using Docker, you need Docker Engine 23.0.0 or later. Docker _Desktop_ will not work; you must use Docker _Engine_."

On the shutdown timeout ("Allow time for a clean shutdown"): "When the container is stopped or restarted, Docker sends `SIGTERM` and then forcibly stops the container after a timeout, which is 10 seconds by default. Home Assistant can need longer than that to close its database." It gives `docker run ... --stop-timeout 60 ...` and, for compose, `stop_grace_period: 60s`.

On devices ("Exposing devices"): "In order to use Zigbee or other integrations that require access to devices, you need to map the appropriate device into the container. Ensure the user that is running the container has the correct privileges to access the `/dev/tty*` file, then add the device mapping to your container instructions:"

```bash
docker run ... --device /dev/ttyUSB0:/dev/ttyUSB0 ...
```

```yaml
services:
  homeassistant:
    ...
    devices:
      - /dev/ttyUSB0:/dev/ttyUSB0
```

The page's other install variants (a NAS package manager's container UI) set the network to `host` and, for a Z-Wave USB stick, pass `--device=/PATH_TO_YOUR_USB_STICK`.

Nothing on this page says why the container uses the host's network. The reason (finding devices on the LAN) is on the pages of the integrations; see the integration survey on #201.
