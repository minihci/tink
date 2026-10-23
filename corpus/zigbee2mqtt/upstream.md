# Zigbee2MQTT (Docker)

Upstream install instructions, verbatim. Retrieved 2026-10-23.

Source: https://www.zigbee2mqtt.io/guide/installation/02_docker.html (source: github.com/Koenkk/zigbee2mqtt.io, docs/guide/installation/02_docker.md, master at 2d9af8dbf2)

## docker run

The page says: Start by figuring out the location of your adapter as explained [here](../configuration/adapter-settings.md#basic-configuration).

```bash
$ docker run \
   --name zigbee2mqtt \
   --restart=unless-stopped \
   --device=/dev/serial/by-id/usb-Texas_Instruments_TI_CC2531_USB_CDC___0X00124B0018ED3DDF-if00:/dev/ttyACM0 \
   -p 8080:8080 \
   -v $(pwd)/data:/app/data \
   -v /run/udev:/run/udev:ro \
   -e TZ=Europe/Amsterdam \
   ghcr.io/koenkk/zigbee2mqtt
```

The page's parameter notes include: "`-v /run/udev:/run/udev:ro`: required for auto-detecting the adapter", and "You should always use the `/dev/serial/by-id/` path ...".

> [!TIP]
> If you run the MQTT-Server on the same host (localhost) you could use the IP
> of the `docker0` bridge to establish the connection: `server: mqtt://172.17.0.1`.

## docker compose

```yaml
services:
    zigbee2mqtt:
        container_name: zigbee2mqtt
        image: ghcr.io/koenkk/zigbee2mqtt
        restart: unless-stopped
        volumes:
            - ./data:/app/data
            - /run/udev:/run/udev:ro
        ports:
            # Frontend port
            - 8080:8080
        environment:
            - TZ=Europe/Berlin
        devices:
            # Make sure this matched your adapter location
            - /dev/serial/by-id/usb-Texas_Instruments_TI_CC2531_USB_CDC___0X00124B0018ED3DDF-if00:/dev/ttyACM0
```

Rootless variant, added to the service:

```yaml
group_add:
    - dialout
user: 1000:1000
```
