# Public exposure: how traffic reaches an ingress instance on a host with one public address

**Status: a design note, 2026-10-17. The two plan-time checks below are built; the rest is proposed.** Claims are marked **[lab]** (measured on a real host, Incus 7.5.1, 2026-10-17), **[code]** (read in this repository), **[docs]** or **[proposed]**.

## The question

The ingress helper is a container on a private bridge. On a LAN host (tron) it has its own LAN address on a bridged NIC (`user.tink.leg.<nic>: serves`), and clients reach it directly. On a VPS with **one public address and no routed block**, something has to carry ports 80 and 443 from that address into the container. The VPS this was measured on does it today with two firewall redirects for a rootless Caddy (`/etc/iptables/rules.v4`, `/etc/nftables.conf`), which is the right trade for rootless Podman, where nothing is privileged enough to do it any other way. Incus is the privileged agent, so the same job is a device on the instance.

## What Incus gives, as measured [lab]

An instance device `{type: proxy, nat: "true", listen: "tcp:<public-ip>:443", connect: "tcp:<instance-ip>:443"}` on a NIC with a static address:

| Behaviour | Seen |
|---|---|
| The rule Incus writes | `ip daddr <public-ip> tcp dport 443 dnat to <instance-ip>:443`, in `prerouting` and in `output`, one chain per device. Scoped by destination address, and the `output` chain also serves the host talking to its own address. The legacy redirect, `tcp dport 443` with no address, is what broke outbound HTTPS from every container on that host. |
| The client address the container sees | The real one (`73.52.160.19` in the test). The default userspace proxy (no `nat`) shows `127.0.0.1`. Caddy's `remote_ip` guards need the real address, so ingress needs `nat`. |
| A wildcard `listen` (`0.0.0.0`) with `nat` | Accepted; the rule matches `fib daddr type local`, so only traffic addressed to the host's own addresses, not forwarded traffic. Not greedy. |
| A port range in `listen`, one `connect` port | Accepted. |
| `connect` that is not an IP address (a name, with `nat`) | Refused by Incus when the device is added: `Not an IP address "localhost"`. |
| `nat` on an instance whose NIC has no static address | The instance is created, then **cannot start**: `Connect IP "…" must be one of the instance's static IPv4 addresses`. |
| Two instances with the same `listen` address and port | **Accepted without a word.** Both run, both rules exist, and the one that matches first answers; the other never sees traffic. |

## What tink does today [lab]

It needs no change to express this: a stack's devices are passed through, so `type: proxy` and `nat: "true"` are ordinary keys. A stack with an OCI container, a static address and two NAT proxies planned, applied and re-planned as `no changes`; changing a `listen` port on the running instance showed as `device.http.listen: "…" -> "…"` and applied live, with no restart.

The gaps are in what it does **not** say when the stack is wrong:

1. **A NAT proxy with no static address** planned as `would create` with no warning. The apply created the instance, retried the start five times, and left it stopped with Incus's message.
2. **A duplicate listener** planned and applied without comment.
3. **`tink join` cannot describe a public host.** A leg is `serves`, `mesh` or `discovery`, and the join **requires** exactly one dns helper, an ingress helper with one `serves` and one `mesh` leg (each with a static address), and at least one sign-on helper **[code, `internal/join/join.go`]**. A site that is reached from the internet through the host's address, with its names in public DNS and no mesh gateway, fits none of that.

## Changes

**Built here: two plan-time warnings** (`internal/resolve/proxydev.go`), in the manner of the OCI NIC warning ([oci-network.md](oci-network.md)). They are warnings, not errors, and they speak about the stack whether or not the instance exists yet.

- **A NAT proxy must connect to a static address of the instance.** For each proxy device with `nat` on and bound on the host, the `connect` host must be an IP address (Incus refuses anything else) and must be one of the `ipv4.address` / `ipv6.address` values (a CIDR counts, with its prefix ignored) of the NIC devices the instance gets from its own devices and from the profiles the stack declares. Profiles the stack does not declare cannot be seen; the warning says which were not consulted.
- **Two host-side listeners must not overlap, within a stack.** Compared across all instances of the stack, whatever their project, because a listener is host-global: same protocol, addresses equal or one a wildcard of the same family, and port sets (single ports, comma lists and ranges) that intersect. Each instance involved is told which other instance and device it clashes with. It cannot see other stacks, and says so. Devices bound inside the instance (`bind: container`) listen in the instance's own network and are ignored.

Run against a real server on 2026-10-17 **[lab]**, as a read-only `plan`: a correct stack (a static address, a NAT proxy that connects to it) was silent; the same stack with the NIC's address removed got the static-address warning; two instances listening on `tcp:170.205.38.254:443` and `tcp:0.0.0.0:443` each got a note naming the other. Nothing was applied.

**Proposed, in this order:**

3. **Generalize `join`'s site shape, then add a `public` leg.** Each rendering should need only what it renders: the dns records need a dns helper and a `serves` leg; the mesh records need a mesh helper and a `mesh` leg; the Caddy routes, sign-on rules and ACL rules need the ingress and sign-on helpers. A `public` leg says "reached from the internet through the host's address"; `join` would render nothing for it (the names live in public DNS, outside tink) and print a note. What the apps' ACLs admit as the source, today the `mesh` leg's address, has to be defined for a host with no mesh leg. This is the real design work, and it is a platform-recipe question as much as a `join` one.
4. **A `kind: network-forward`** (untested). Incus network forwards put the rule on the network, not on an instance device, so the helper that owns the traffic does not need a device-edit right. They also need a static target address, and tink would need the kind. Worth measuring against the proxy device before building.

## Cautions

- **A device-edit right is host reach.** A NAT proxy lets whoever can edit the instance expose host ports; this is the reason the daemon helper must not hold an instance-edit right ([helper.md](helper.md)). Only the identity that applies the stack should.
- **Rules outside Incus are invisible to tink.** On the measured VPS the legacy redirects sit in front of Incus's own rules and hijack container traffic; scoping them is a host change tink cannot see or make. A recipe for such a host should say so.
- **A restricted project** refuses proxy devices unless it allows them.
