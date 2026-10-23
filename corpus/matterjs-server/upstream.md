# Matter.js Server (the Matter controller server)

Upstream install instructions, verbatim. Retrieved 2026-10-23.

Source: https://github.com/matter-js/matterjs-server (docs/docker.md, docs/os_requirements.md and docker/matterjs-server/docker-compose.yml, main at d2365a52a0)

The successor to the Python Matter server (`python-matter-server/`, archived): "This Matter server is designed as a drop-in replacement for the Python Matter Server, and supports the same WebSocket API." Home Assistant's own add-on is the recommended way to run it; this page is the standalone container.

## docker run

```bash
mkdir data
docker run -d \
  --name matterjs-server \
  --restart=unless-stopped \
  -v $(pwd)/data:/data \
  --network=host \
  ghcr.io/matter-js/matterjs-server:stable
```

With command-line arguments (these override the environment variables):

```bash
docker run -d \
  --name matterjs-server \
  --restart=unless-stopped \
  -v $(pwd)/data:/data \
  --network=host \
  ghcr.io/matter-js/matterjs-server:stable \
  --storage-path /data \
  --primary-interface eth0
```

## docker compose

The page says: "A Docker Compose file is provided in the repository at docker/matterjs-server/docker-compose.yml". It reads:

```yaml
services:
  matterjs-server:
    container_name: matterjs-server
    image: ghcr.io/matter-js/matterjs-server:latest
    read_only: true
    network_mode: host
    restart: unless-stopped
    # Optional Node.js heap hint. No default — size depends on the number of
    # commissioned nodes. See docs/docker.md "Node.js Memory Limit".
    # environment:
    #   NODE_OPTIONS: "--max-old-space-size=512"
    volumes:
      - "${HOME}/.matterjs-server:/data"
```

## What the page says about the host (quoted)

> [!NOTE]
> **Attention Home Assistant users:** The Docker image is provided as-is and without official support (due to all the complex requirements to the underlying host/OS). Use it at your own risk if you know what you're doing.

> [!NOTE]
> **Attention Python server docker users:** This Matter server is designed as a drop-in replacement for the Python Matter Server, and supports the same WebSocket API. The only difference is that the new container runs unprivileged by default and so needs a [permission-change of the data dir](#permission-issues)! Content-wise just use the same data directory and command-line arguments. The first start will include a data migration, see the [migration guide](migration.md).

> If you still prefer a self-managed container installation, you might experience communication issues with Matter devices, especially Thread-based devices. This is mostly because the container installation uses host networking, and relies on the networking managed by your operating system.

> [!WARNING]
> **Security:** The server binds to all network interfaces by default. You are responsible for
> securing access according to your requirements and use-case — for example by setting
> `LISTEN_ADDRESS=127.0.0.1` to restrict the WebSocket port to localhost, setting
> `PRIMARY_INTERFACE=eth0` to control which interface Matter uses, or placing a firewall or
> authenticating reverse proxy in front of the server.

> For security reasons, by default the matter.js server is run as an unprivileged user in Docker. The Bluetooth backend ([noble](https://github.com/matter-js/matter.js/tree/main/packages/nodejs-ble)) talks to the host adapter in one of two ways, selectable via the `NOBLE_BINDINGS` environment variable:

> Set `NOBLE_BINDINGS=dbus` (in addition to selecting the adapter) to make noble talk to the host's BlueZ daemon over D-Bus instead of opening a raw HCI socket. This keeps the container unprivileged: the only host resource it needs is the system D-Bus socket, mounted read-only.

Bluetooth commissioning through the host's BlueZ over D-Bus (the page's example):

```bash
docker run -d \
  --name matterjs-server \
  --restart=unless-stopped \
  -v $(pwd)/data:/data \
  -v /run/dbus:/run/dbus:ro \
  --network=host \
  -e NOBLE_BINDINGS=dbus \
  -e BLUETOOTH_ADAPTER=0 \
  ghcr.io/matter-js/matterjs-server:stable
```

And, for the container's ping: "To allow this, modern Linux distributions use a sysctl `net.ipv4.ping_group_range` with a value of `0 2147483647` ... update the value to include group id 1000, or to grant the kernel capability `CAP_NET_RAW` instead: `docker run --cap-add NET_RAW` (for compose: `cap_add: NET_RAW`)".

## What its OS requirements page says (docs/os_requirements.md, quoted)

> Matter is based on IPv6 link-local multicast protocols and thus running the Matter Server (or developing it) is not as straightforward as any other application, mostly due to the bad shape of IPv6 support in various Linux distributions, let alone the IPv6 Neighbor
> Discovery Protocol, which is required for Thread.

> Matter uses link-local multicast protocols which do not work across different LANs or VLANs so best to use either a complete flat network or ensure that the machine running Matter Server is on the same (v)LAN as the devices, any border routers and the phone/device used for commissioning.

> The host network interface needs IPv6 support enabled.

> **DO** give the Matter Server host (or VM) a network interface directly on the network where the Thread Border Router lives. Bridged/NAT networking combined with Matter UDP unicast traffic is unreliable.

> **DO** set the IPv6 sysctls (`accept_ra`, `accept_ra_rt_info_max_plen`, `forwarding=0`) **inside** the VM guest as well, not just on the hypervisor.

> **DO** run the Docker image with `--network=host`. Bridge networking will not work. Note that `--network=host` bypasses Docker's own iptables rules but **not** firewalld or ufw on the host kernel — see [Stateful firewalls](#stateful-firewalls-host-vm-hypervisor) above.

> **DO NOT** enable mDNS reflectors or Avahi forwarders between network segments.
