# Networks as resources: bridges, ACLs and forwards

**Status: design, except the first slice.** `kind: network` for managed bridges (create and update, never delete, a guard on changes to a bridge in use) is built and documented in [`network.md`](network.md); network ACLs, forwards and everything else below are not. Written 2026-10-12, from a Home Assistant migration whose network decisions (a dedicated bridge, an isolating
ACL, published ports) had to be made by hand because tink cannot declare them. It is the reasoning to be argued with, not a decision record.

Claims are marked **[verified]** (tried live; see the [appendix](#appendix-how-the-claims-were-verified)), **[code]** (read in this repo), **[docs]**
(read in upstream documentation) or **[hypothesis]** (believed, to be checked).

## The problem

A stack can declare an instance's NIC, `network: incusbr0`, but not `incusbr0`, the ACL the NIC is held to, or the forward that publishes its port.
Everything around the instances is hand-made host state:

| Hand-made today | Where it lives | What depends on it |
|---|---|---|
| A bridge's `raw.dnsmasq`, pushing classless routes for the tailnet and an overlay to instances named `helper` | `incus network edit incusbr0` | the helper's reach to the other side of an overlay **[verified]** |
| Two network ACLs that isolate a restored copy of production from the LAN and, in the second, allow only the internet | `incus network acl create ...` over ssh | the safety of the lab; without them the copy would reconnect to the real MQTT broker and cloud accounts **[verified]** |
| (Planned) a dedicated bridge, and ACLs and forwards for the new project | the same | the design choice between a shared and a dedicated bridge |

The one tool tink has for this, `kind: incus`, is argv for the **local** `incus` CLI and is **BLOCKED under `--remote`** **[docs]** `remote.md`, so a stack
applied from a laptop cannot carry it. The consequences:

- The stack is not reproducible: rebuilding the host from the YAML and the volume backups loses the network, silently.
- The decisions the stack is *about* (a bridge per project, an isolating ACL, what is published) are the ones it cannot state.
- Any check about networking has nothing to read. `opinion-as-code.md` (on the `platform-design` branch) has no networking opinion, and "a LAN leg must carry a reason", "names collide across
  projects on one bridge" and "an IP literal in a config equals a bridge address" all need to know the networks and ACLs in the stack.

## What is true today

| Fact | Where |
|---|---|
| The Incus client has create, update and delete calls for networks, ACLs, forwards, load balancers, peers and zones (and `GetNetworks`, `GetNetworkACLs`, ...), all over the API, so all work under `--remote` | **[code]** `client/interfaces.go`, Incus v7.4.0 |
| The dependency graph infers edges from a resource's project, profiles, `disk` device sources, instance, image, backup targets and `depends_on`. It does **not** infer an edge from a NIC's `network:` or `security.acls:` | **[code]** `Resource.dependencies` in `internal/resolve/resource.go` |
| Networks and ACLs are **server-global** unless a project has `features.networks`, which requires OVN | **[docs]** Incus project reference, ACL how-to |
| An ACL on a bridge acts at the bridge-to-host boundary; filtering between instances on one bridge needs the ACL on the NIC. Once an ACL applies, unmatched traffic is rejected. DHCP and DNS baseline rules are evaluated before ACL rules. ACL groups and `@internal` selectors are not supported on bridges | **[docs]** Incus network ACL how-to |
| Incus applies an ACL's `reject` rules **before** an `allow`-everything rule, whatever order they are listed in | **[verified]** |
| A bridge's live config holds what Incus generated (a concrete address where `auto` may have been asked for) | **[verified]** `incusbr0` shows `ipv4.address: 10.10.10.1/24`, `ipv6.address: none` |
| `tink export` can turn live instances into YAML that plans as "no changes"; it has nothing for networks | **[code]** |
| A bridge's `used_by` lists the instances on it | **[verified]** |

## Proposal

### Three kinds, flat, global

| Kind | Fields | Notes |
|---|---|---|
| `kind: network` | `config`, `description` (bridge networks only at first) | Name is the network name. Not project-scoped: a `project:` on it is a load error |
| `kind: network-acl` | `ingress`, `egress` (lists of rules: `action`, `source`, `destination`, `protocol`, `source_port`, `destination_port`, `icmp_type`, `icmp_code`, `description`), `description` | Rules compared as a **set**, since Incus orders them by action |
| `kind: network-forward` | `network`, `listen_address`, `target_address`, `ports` (protocol, listen/target port, target address), `snat` | Depends on its network |

Why three flat kinds and not one with nested ACLs and forwards: it matches how every other kind is declared, dependencies infer by name, an ACL is shared by
many networks and NICs, and a change to one should not look like a change to the others.

### Dependencies

Add edges for a NIC device's `network:` and each name in its `security.acls:` (comma list), a network's `security.acls`, and a forward's `network`, **when the
named resource is in the stack**. Naming one that is not stays lenient, like profiles and volume sources: it may exist on the server already.
The stack-level default project (`stackproject.go`) must not give these kinds a project; they are global.

### Semantics

- **Create and update, never delete**, as for every other kind. A forward or ACL rule removed from the YAML stays until removed by hand; see
  `authoritative-apply-design.md` for what remembering ownership would change, and its Layer 1 would report them as undeclared.
- **Adoption first.** `tink export` learns networks, ACLs and forwards and writes YAML that `plan` reads as "no changes", before any apply. This is the
  stage-1 move in `opinion-as-code.md` ("adopt, then prove it read-only"), and the only safe way to bring `incusbr0` under management.
- **Generated values.** A declared `ipv4.address: auto` (and `ipv6.address: auto`) matches any live address. **[hypothesis]** that this covers it; check
  what Incus stores for each of `auto`, `none` and an explicit CIDR.
- **Disruption is shown, and the dangerous part needs consent.** A network's `used_by` is the count of instances `plan` can name. Changing the address or
  NAT of a bridge, or `dns.mode`/`dns.domain`/`raw.dnsmasq`, can interrupt traffic for every instance on it. **[hypothesis]** which keys; a lab test should
  settle it. Suggested: `plan` prints "affects N instances: ..." for any change to a network in use; address and NAT changes are BLOCKED unless the
  resource says `allow_disruption: true`. This is a network version of `on_image_change: report`: the default is to say, not to do.
- **Everything is an API call**, so it works from a laptop under `--remote`, unlike `kind: incus`.
- **Load-time validation:** a network or ACL name Incus accepts, an `action` in `allow|reject|drop`, parseable CIDRs, a forward `listen_address` that parses.

### What it unlocks

With the networks, ACLs and NICs in one stack, `plan` can check things it cannot today. These are opinions of their own and need their own design, but they
depend on this:

1. Two instances in different projects with the same name on one bridge (Incus's bridge DNS names are flat across projects **[verified]**).
2. A literal IP in `environment.*` or `file` content that equals an address of a managed bridge or a host.
3. A NIC bridged onto an unmanaged parent (a LAN leg) with no stated reason.
4. A plain `proxy` device where `nat: true` would keep the client's address (the instance sees every client as `127.0.0.1` otherwise **[verified]**).
5. An ACL attached to a NIC with no ingress allow for the host, which would make the instance unmanageable.

## Out of scope for a first version

OVN networks and everything that needs OVN (project-scoped networks, uplinks); macvlan, ipvlan and physical NICs; the host's own unmanaged bridges (`br0`);
peers, load balancers, zones, address sets and integrations. Deleting anything.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Lift the `kind: incus` restriction under `--remote` | It runs the *local* CLI with local paths, so "remote" would be a lie; it stays host-dependent shell |
| Keep a written runbook of `incus network ...` commands | What exists today; it is the problem |
| One `kind: network` with nested ACLs and forwards | An ACL is shared; ownership and diffs get confusing; one huge document |
| Start with `kind: network` | The biggest blast radius first. A new ACL changes nothing until it is attached |

## Smallest first step

1. **`kind: network-acl` alone.** Create and update, rules as a set, dependency edges from NIC `security.acls`, export. It is safe (an unattached ACL does
   nothing), pure API, and it is exactly what the migration lab needed and hand-made twice.
2. **`kind: network` read-only:** export and plan ("no changes" on `incusbr0`), no apply. Prove adoption on the real bridge before it can change anything.
3. Apply for non-disruptive network keys, then the disruption guard.
4. `kind: network-forward`.

## Questions for the author

1. Is a first release that can only create and update (and report) an acceptable shape for networks, given that "never deletes" is already how tink behaves?
2. `allow_disruption`: the right word and the right default? Or should any change to a bridge in use be a block until consented?
3. Does `export` learn networks by default, or only with `--network NAME`? A host has bridges tink did not create and should not touch.
4. Should an ACL's rule list be validated against Incus's own rules at load (a typo in a CIDR) or left to the server?

## Appendix: how the claims were verified

On the evaluation host, 2026-10-12, Incus 7.5.1, `tink v0.1.4`.

- **Reject before allow:** an ACL with five `reject` rules for private ranges and a final `allow` for everything was attached to a container's NIC. From inside
  it, ten private targets were blocked and the public endpoints needed to install Python packages were open, with the allow listed last.
- **Hand-made `raw.dnsmasq`:** `incus network show incusbr0` holds `dhcp-name-match=set:ts,helper` and a `dhcp-option=tag:ts,121,...` line pushing routes for
  `100.64.0.0/10` and an overlay prefix via a gateway container, and no stack declares it.
- **Flat bridge DNS names:** `mosquitto.incus` resolves from the host against the bridge's dnsmasq; `x10-mqtt.x10-lab.incus` and `ha.ha-eval.incus` do not.
- **Plain proxy devices hide the client:** the broker behind a plain `proxy` device logged `127.0.0.1` for clients that came through the published port, and
  the real address (`10.10.10.44`) for one on the same bridge.
