# OCI containers: a NIC needs a `name:` for its static settings to apply

**Status: the cause was found in the Incus v7.5.1 source and confirmed on a live host; tink's warning is unit-tested and its output was checked against that host (see the end).** Claims are marked **[code]** (read in the Incus source), **[verified]** (seen on the host) or **[docs]**.

## What goes wrong

An Incus OCI (app) container has no init system in the image to configure its network, so Incus does it: `incusd forknet dhcp` runs a DHCP client on each interface, unless a record says the interface is statically configured. That record, `network/interfaces.json` in the instance's directory on the host, is built from the container's NIC devices, **but only from the ones that have a `name:` key** (the interface's name inside the container) **[code]** (`driver_lxc.go` skips a NIC device whose `name` is empty). An interface that is not in the record is treated as fully dynamic: DHCP runs, and the lease's default route and nameservers are applied **[code]** (`main_forknet.go`).

So a NIC device **without `name:`** ignores these settings, which Incus documents for OCI containers **[docs]**:

| Setting | What it would have done on a named interface |
|---|---|
| `ipv4.address: <CIDR>` | configure that address statically and not run DHCPv4 |
| `ipv4.address: none` | not run DHCPv4 and restrict IPv4 traffic |
| `ipv4.gateway: none` (or an address) | not take the lease's default route (or use that gateway) |
| `ipv6.address`, `ipv6.gateway` | the same for IPv6 |

Nothing says so. The instance starts, the CIDR is even configured (by a different code path), and `tink plan` says `no changes` because it compares Incus's configuration, not what the guest does with it. What you see is a second address from the router's DHCP next to your static one, the router's default route, and the router's nameservers in `resolv.conf` **[verified]**: on the lab host `interfaces.json` was `{}`; adding `name: eth0` and `name: eth1` made it `{"eth0": {dhcp4 true, ...}, "eth1": {"dhcp4": false, "route4": false, ...}}`, and the second address, the router's default route and its nameservers all went away.

## What tink says

`tink plan` and `tink plan apply` warn, once per device, when an **OCI** instance has a NIC device with **no `name:`** that sets one of the four settings above with a value that matters (a CIDR or `none` for an address, any gateway):

```
instance/ha-lab: would update
    warning: NIC device "lan" sets ipv4.address, ipv4.gateway but has no name:. Incus's OCI network setup records only named NICs, so this interface is treated as fully dynamic: ...
```

It looks at the instance's own devices and at the devices of the profiles the stack declares (an instance's device of the same name replaces the profile's whole device). The fix is to add `name:` with the interface's name inside the container:

```yaml
devices:
  lan:
    name: eth1          # without this, ipv4.address and ipv4.gateway below are ignored
    type: nic
    nictype: bridged
    parent: br0
    ipv4.address: 10.0.142.246/24
    ipv4.gateway: none
```

It is a **warning**, not an error: a stack that works today must keep applying. It stays quiet where the name would change nothing: an unnamed NIC with none of those settings, or with a bare address on a managed network (a DHCP reservation, dynamic either way).

## What it does not see

- A profile that is not in the stack (the server's `default` profile, say). Its devices are not read.
- An image that is not recognisably OCI: an instance is OCI when its image is `remote:ref` and the remote's protocol is `oci` (`docker-oci:`, `ghcr:`, a registry host mapped to a remote). A local alias or an image the stack builds is not.
- Whether the name matches the interface Incus actually creates. It only checks that a name is present.
- Instances that already exist: the warning is about the stack, so it appears on the next `plan` whether the instance is new or not.

## Where this came from

The Home Assistant migration lab (`smarthome/EVAL.md`, gap G19): a dual-homed Home Assistant container kept a router lease next to its static LAN address, took the router's default route, and picked up the router's nameservers, and two documented settings seemed to be ignored. Reading the Incus source showed one cause. The Incus NIC reference does not mention that `name:` is required; that is worth raising upstream.

## Status

Unit tests cover the decision table, profile devices, the plan options, and a plan of an instance that does not exist yet. The output was checked against the lab host with `tink plan --remote`: the stack with `name:` on its NICs is silent, and the same stack without it warns.
