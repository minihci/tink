# ESPHome Device Builder (Docker)

Upstream install instructions, verbatim. Retrieved 2026-10-23.

Source: https://esphome.io/install/docker/ (source: github.com/esphome/esphome-docs, src/content/docs/install/docker.mdx, current at 1ae3fdd7eb)

## docker run (the dashboard)

Started with no subcommand, the image runs the ESPHome Device Builder dashboard:

```bash
docker run --rm --net=host -v "${PWD}":/config -it ghcr.io/esphome/esphome
```

The page says of it:

> `--net=host` lets the dashboard use mDNS, which is how it discovers devices, resolves their `.local` names, and tracks
> whether they are online. The [host networking driver](https://docs.docker.com/engine/network/drivers/host/) only works
> on Linux hosts. On Docker Desktop version 4.34 and later it is available, but must first be enabled under
> **Settings** → **Resources** → **Network**.

Without host networking, publish the port instead:

```bash
docker run --rm -p 6052:6052 -v "${PWD}":/config -it ghcr.io/esphome/esphome
```

> The Device Builder still runs this way, and still checks devices with a periodic ICMP ping sweep. Without multicast,
> though, `.local` names cannot be resolved, so the sweep only has an address to ping for devices that have a fixed one -
> set with `use_address` or [`manual_ip`](/components/wifi/#wifi-manual_ip) in the device's own configuration. Other
> devices will show no status.

## Flashing over USB

```bash
docker run --rm --privileged -v "${PWD}":/config --device=/dev/ttyUSB0 -it ghcr.io/esphome/esphome run livingroom.yaml
```

## docker compose

```yaml
services:
  esphome:
    container_name: esphome
    image: ghcr.io/esphome/esphome
    volumes:
      - /path/to/esphome/config:/config
      - /etc/localtime:/etc/localtime:ro
    restart: always
    privileged: true
    network_mode: host
    environment:
      - ESPHOME_USERNAME=admin
      - ESPHOME_PASSWORD=ChangeMe
```

> [!NOTE]
> The credential variables are `ESPHOME_USERNAME` and `ESPHOME_PASSWORD`, not the legacy dashboard's bare `USERNAME`
> and `PASSWORD` - those collided with the `$USERNAME` most login shells already set. Prefer an environment file with
> restricted read permissions over a password on the command line, since command-line arguments are visible to other
> users in process listings.
