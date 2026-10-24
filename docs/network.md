# `kind: network`: a managed bridge in the stack

**Status: built, unit-tested against a fake Incus server, and run live once.** On the lab host (Incus 7.5.1, 2026-10-12) it created a bridge together with the three
instances on it, the next `plan` said `no changes` on every resource, the disruption guard blocked a change to that bridge and named its three instances, and
`plan` read the host's own `incusbr0` (21 instances) as `no changes` with `auto` addresses and its LAN bridge `br0` as BLOCKED. Apply of an *update* to a bridge,
and `allow_disruption: true`, have only the fake-server tests. Claims are marked **[code]**, **[docs]** (upstream Incus documentation), **[verified]** or **[hypothesis]**.

## What it is for

An instance's NIC can say `network: svclab0`, but until now a stack could not say what `svclab0` *is*. The bridge a project's services talk over was hand-made host
state, so rebuilding a host from the YAML and the volume backups lost it, silently, and `plan` could not tell that a NIC named a bridge that was not there.
`kind: network` declares the bridge in the same file as the instances that use it:

```yaml
kind: network
name: svclab0
description: the smarthome services talk to each other on this bridge
config:
  ipv4.address: 203.0.113.1/24
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
- **Present and different, instances on it:** `BLOCKED`, naming the instances, and nothing is written. Changing a bridge's address, NAT or DNS can interrupt every instance on it **[hypothesis: which keys; the guard treats every config change alike to stay safe]**. `allow_disruption: true` on the resource turns the block into an update with an `affects N instance(s): ...` warning. A description-only change is harmless and is never blocked. The ACL keys (`security.acls` and `security.acls.default.*`) are the exception: attaching or changing an ACL is a live firewall change and is not counted, see [`network-acl.md`](network-acl.md). This is the network version of `on_image_change: report`: the default is to say, not to do.
- **Never deletes**, like every other kind. Removing a key from the YAML leaves it on the bridge.

## What it refuses to touch

| Found | Result |
|---|---|
| a network Incus does not manage (the host's own LAN bridge, a physical NIC) | `BLOCKED`. tink never creates over or changes it. Name a different network |
| a managed network that is not a bridge (OVN, macvlan, ...) | `BLOCKED` |
| a network whose status is not `Created` | `BLOCKED` |
| the server answering 403, a 5xx or dropping the connection | an error, never read as "the network is absent" |

## Ordering

A NIC's `network:` (on an instance or on a profile) is an ordering edge **when the network is in the stack**: the bridge is created before the profile or instance that names it. A NIC naming a network the stack does not declare (`incusbr0`) stays lenient, because it may exist on the server already, the same as a profile or a volume source. The same holds for an ACL named in a bridge's `security.acls`: the ACL is created first ([`network-acl.md`](network-acl.md)).

## A bridge of the stack's own

**Status: built (the second piece of the derive stage, #177); unit-tested, and run on a local Incus 7.5.1 server on 2026-10-22 (L-2026-10-21b).** Six stacks, each with `network: own` and no network, ACL, NIC or address written, were brought up from nothing; the 32 checks of the herd test passed, a second plan of every stack said no changes, and the stacks' names, routes and sign-on rules were rendered by `tink join --from-server` from what the stacks had made.

```yaml
kind: stack
name: blog
network: own          # optional: network_pool: 203.0.113.0/24, network_prefix: 27
egress: open          # optional: none (the default) or open
```

For a stack that says `network: own`, `tink plan` (the fifth pass of `prepare`, `internal/derive`) adds, before anything is planned:

- **A `kind: network`** named `tink-` and up to ten characters of the stack's name (a longer name is cut to five characters and a short hash, so the whole fits an interface name), with a subnet, `ipv4.nat: "true"`, `ipv6.address: none`, a DHCP range, the default egress action (below), and the ACL below attached.
- **A `kind: network-acl`** named after the bridge with `-in`, that derives its ingress rules from the publishes ([network-acl.md](network-acl.md#rules-derived-from-the-publishes)).
- **A NIC on the bridge** for each instance that names no network. An instance with no NIC gets `eth0`; a NIC device with no network and no parent (what `defaults:` leaves) gets the bridge. A NIC that names a network, or has an address, is left as written.
- **A static address** on that NIC for each instance that has an offer (`user.tink.offer.*`), because the proxy and the ACL name the app by address.

Whatever the stack writes itself wins: a `kind: network` or `kind: network-acl` of those names is used as it is.

**Egress: what the stack's apps may send out.** Outbound traffic is denied unless the stack grants it, as inbound traffic is denied unless a publish admits the ingress helper. `egress: none` (the default) sets the bridge's `security.acls.default.egress.action` to `reject`, and `egress: open` sets it to `allow`. The default is written out, not left unset, so that a hand-set `allow` on the bridge shows in `plan` as a change.

`open` is Incus's own meaning of `allow`, not a promise of internet access: traffic goes where the host routes it and the destinations accept it. It also depends on NAT on the bridge (`ipv4.nat` is on), the host's forwarding and firewall, and the upstream network, none of which tink guarantees. It is wider than the internet, too: it covers the host, the LAN and the platform bridge, minus what the destination's own ACL rejects (another stack's bridge admits only its ingress helper). A narrower grant is not available: Incus applies reject rules before allow rules whatever their order, so "everything but the private ranges" would also block the bridge's own DNS. The bridge is the unit, so `egress` covers all of a stack's apps or none. With the default, a name still resolves (the bridge answers DNS) and the connection is then refused, which looks like a broken app: Whisper, which downloads its model on first start, is the case seen **[lab]**.

**`plan` asks once.** A stack with `network: own` that writes no `egress` gets a note at the end of `plan` and `plan apply`: its apps cannot open outbound connections, and the field answers it either way. The note is about the stack's stance, not about any app's need, so it needs no knowledge of the app; writing `egress: none` (meant) or `egress: open` silences it.

`egress` needs `network: own`. A stack without it has no tink ACL, so tink restricts nothing and the field is refused. A stack that declares the bridge itself (`kind: network` named as above) is refused too, because what it writes wins and `egress` could not change it: set `security.acls.default.egress.action` in that network's config. On a `kind: network-acl`, the same key `egress:` is the list of egress rules.

**Where the numbers come from.** The subnet is chosen by tink, not by Incus's `auto`, because an address has to be in the plan before the bridge exists. It is a `network_prefix` (default /24) out of `network_pool` (default 10.0.0.0/8), starting at the place a hash of the stack's name points at and moving on past every subnet any network on the server already holds. The subnet is divided as the bridge is made: the gateway, then a run of static addresses (a quarter of the hosts, at least four), then the DHCP range for the rest, so a lease handed out later never lands on an address tink allocated. An app's address is the lowest free one in that run, below every static address any NIC on the server holds (every project is read, because Incus refuses a duplicate across projects).

**Nothing is remembered but on the objects.** Once the bridge exists its address is read back from the server and never chosen again, and an instance that already has an address on the server keeps it, so a second plan says no changes. Several stacks get different subnets.

**Refusals.** A network of that name that belongs to another stack (its `user.tink.stack` stamp), that Incus does not manage, or that has no IPv4 address; a pool with no free subnet; a subnet with no free static address. Each says what to change.

**Limits.**
- tink cannot see the host's own routes or the LAN's range. A pool that overlaps them is the person's to move (`network_pool`).
- `tink join FILE...` reads the files, which do not hold what was derived; use `tink join --from-server` after `plan apply`.
- Not here: several bridges in one stack (write them with `kind: network`), a bridge shared between stacks, OVN, and deleting a bridge.
- **A host firewall that does not trust unknown interfaces** needs a rule once for the `tink-*` prefix, or the bridge's instances get no DHCP or DNS (#177). tink never changes the host's firewall. On firewalld, one zone entry for `tink-+` was enough for six bridges that did not exist when it was added.

## Adopting a bridge you already have

Declare it with the keys you care about and run `plan`: it should say `no changes`, and if it does not it shows exactly which declared key differs, before anything is written. Declare few keys at first, and leave `raw.dnsmasq` and the like out until you want tink to own them. A bridge with instances on it is protected by the disruption guard even if a later edit changes a key.

## Not in this kind

Forwards, zones and peers (proposed in #139), `tink export` of networks, OVN, and deleting anything. ACLs are their own kind: [`kind: network-acl`](network-acl.md) declares the rules, and a bridge attaches them with `security.acls` in the `config:` above. Two things the design note raises are left open on purpose: whether `export` should learn networks by default or only by name, and whether a typo in an `ipv4.address` should be caught at load. Today a malformed value is rejected by Incus at apply.
