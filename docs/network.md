# `kind: network`: a managed bridge in the stack

**Status: built and unit-tested against a fake Incus server; not yet applied to a real one.** The first live run is the migration lab that asked for it, and this
note is edited when that has happened. Claims are marked **[code]**, **[docs]** (upstream Incus documentation) or **[hypothesis]**.

## What it is for

An instance's NIC can say `network: svclab0`, but until now a stack could not say what `svclab0` *is*. The bridge a project's services talk over was hand-made host
state, so rebuilding a host from the YAML and the volume backups lost it, silently, and `plan` could not tell that a NIC named a bridge that was not there.
`kind: network` declares the bridge in the same file as the instances that use it:

```yaml
kind: network
name: svclab0
description: the smarthome services talk to each other on this bridge
config:
  ipv4.address: 10.10.20.1/24
  ipv4.nat: "true"
  ipv6.address: none
---
kind: instance
name: mosquitto
image: docker-oci:eclipse-mosquitto:2@sha256:...
devices:
  eth0: {type: nic, network: svclab0}   # waits for the network above; no address needed, DHCP and the bridge's own DNS give `mosquitto.incus`
```

Incus runs a DNS server per managed bridge and records an instance's name only on the bridge its NIC is on **[docs]**, so a bridge of its own gives the project its
own `<name>.incus` namespace.

## Fields

| Field | Meaning |
|---|---|
| `name` | The network's name, which is also its bridge interface name: at most 15 characters, letters, digits, `_`, `.`, `-` |
| `config` | The Incus network config (`ipv4.address`, `ipv4.nat`, `ipv6.address`, `dns.domain`, `raw.dnsmasq`, ...). Only the keys you list are compared and written |
| `description` | The network's description |
| `allow_disruption` | `true` lets apply change the config of a bridge that instances are using (see below). Default `false` |

A `project:` is a load error: networks are server-global unless a project has `features.networks`, which needs OVN, and tink does not manage OVN **[docs]**. There is no `type:`; this kind manages bridges.

## What `plan` and `plan apply` do

- **Missing:** `would create`; apply creates a managed bridge with the declared config. If you declare `ipv4.address: auto` (or `ipv6.address: auto`) Incus picks the subnet.
- **Present and equal:** `no changes`. Keys Incus added itself (`volatile.*`, the concrete subnet it picked) are never drift; the comparison is one-directional, as for profiles.
- **`auto` addresses:** Incus stores the address it picked, not the word `auto`. So a declared `auto` is satisfied by any real address and is never written over one, because writing it would ask Incus to pick a new subnet and move every instance on the bridge. If the network has no address (or `none`) a declared `auto` is a change.
- **Present and different, nothing on it:** `would update`; apply merges the declared keys into the live config.
- **Present and different, instances on it:** `BLOCKED`, naming the instances, and nothing is written. Changing a bridge's address, NAT or DNS can interrupt every instance on it **[hypothesis: which keys; the guard treats every config change alike to stay safe]**. `allow_disruption: true` on the resource turns the block into an update with an `affects N instance(s): ...` warning. A description-only change is harmless and is never blocked. This is the network version of `on_image_change: report`: the default is to say, not to do.
- **Never deletes**, like every other kind. Removing a key from the YAML leaves it on the bridge.

## What it refuses to touch

| Found | Result |
|---|---|
| a network Incus does not manage (the host's own LAN bridge, a physical NIC) | `BLOCKED`. tink never creates over or changes it. Name a different network |
| a managed network that is not a bridge (OVN, macvlan, ...) | `BLOCKED` |
| a network whose status is not `Created` | `BLOCKED` |
| the server answering 403, a 5xx or dropping the connection | an error, never read as "the network is absent" |

## Ordering

A NIC's `network:` (on an instance or on a profile) is an ordering edge **when the network is in the stack**: the bridge is created before the profile or instance that names it. A NIC naming a network the stack does not declare (`incusbr0`) stays lenient, because it may exist on the server already, the same as a profile or a volume source.

## Adopting a bridge you already have

Declare it with the keys you care about and run `plan`: it should say `no changes`, and if it does not it shows exactly which declared key differs, before anything is written. Declare few keys at first, and leave `raw.dnsmasq` and the like out until you want tink to own them. A bridge with instances on it is protected by the disruption guard even if a later edit changes a key.

## Not in this kind

ACLs, forwards, zones and peers (the design note `docs/network-resources-design.md`, on branch `eval/design-notes`, covers how they would follow), `tink export` of networks, OVN, and deleting anything. Two things the design note raises are left open on purpose: whether `export` should learn networks by default or only by name, and whether a typo in an `ipv4.address` should be caught at load. Today a malformed value is rejected by Incus at apply.
