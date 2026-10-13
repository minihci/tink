# `kind: network-acl`: the firewall rules of a bridge, in the stack

**Status: built and unit-tested against a fake Incus server; not yet applied by tink to a real server.** The recipe it reproduces was proven on 2026-10-13 with raw `incus` commands in a lab (the tron repo's `acl-lab/acl-lab.md`); this kind has not yet been run against that bridge. Claims are marked **[code]** (read in this repo, or in the Incus 7.4.0 source), **[docs]** (upstream Incus documentation), **[lab]** (seen live, with raw `incus`) or **[hypothesis]**.

## What it is for

A bridge can be isolated by one ACL that lets only the ingress helper reach a published port. Until now the stack could name that ACL (`security.acls` on a network or a NIC) but not say what it contains, and the one tool for a command tink has no kind for, `kind: incus`, is argv for the *local* CLI and is blocked under `--remote` **[docs]** [`remote.md`](remote.md). An ACL is an ordinary API object, so this kind works from a laptop with `--remote`, like `kind: network`.

An ACL does nothing until something names it: creating one changes no traffic. That is why it is safe to apply first, and why the order below matters.

## The lab recipe as a stack

The recipe, in raw Incus:

```
incus network acl create svclab0-in
incus network acl rule add svclab0-in ingress action=allow source=10.10.30.221/32 destination_port=8123 protocol=tcp state=enabled
incus network set svclab0 security.acls=svclab0-in
incus network set svclab0 security.acls.default.egress.action=allow
```

and the same as a stack file:

```yaml
kind: network-acl
name: svclab0-in
description: only the ingress helper may reach the apps on svclab0
ingress:
  - action: allow
    source: 10.10.30.221/32          # the ingress helper's address; a bridge ACL takes CIDRs, not names
    protocol: tcp
    destination_port: 8123
    state: enabled                   # the default; written here only to match the recipe
---
kind: network
name: svclab0
config:
  security.acls: svclab0-in
  security.acls.default.egress.action: allow   # traffic no rule matches is allowed OUT; unmatched ingress is rejected, which is Incus's default
```

The bridge `svclab0` existed already in the lab, and `kind: network` compares only the keys it is given, so naming it with just the two ACL keys adopts it and leaves its address, NAT and DNS alone ([`network.md`](network.md)). A bridge that does not exist yet would be created with only those keys, so give it an `ipv4.address` too. The ACL is created first and the network updated second, without being told: the network's `security.acls` names an ACL the stack declares, so that is an ordering edge.

The ACL is called `svclab0-in`, not `svclab0`, because resource names are unique across kinds in a stack (a profile and an instance cannot share one either).

## Fields

| Field | Meaning |
|---|---|
| `name` | The ACL's name: 1 to 63 letters, digits and `-`, not starting or ending with `-`, not all digits **[code]** (Incus's own rule; `@`, `%` and `#` are reserved for its selectors) |
| `description` | The ACL's description. Left out, the live one is left alone |
| `ingress`, `egress` | The rules for each direction, a list, in order |
| `config` | The ACL's own config, passed through (Incus accepts only `user.*` keys here **[code]**). Only the keys you list are compared and written |

A `project:`, `allow_disruption:`, or any field not listed is a load error.

**Direction is Incus's meaning.** `ingress` and `egress` here are traffic into and out of an instance's NIC, from the NIC's point of view **[docs]**. They have nothing to do with tink's *ingress* (the reverse proxy in front of apps); the [glossary](glossary.md#collisions) records the collision.

### A rule

Every field has Incus's name and meaning, so the output of `incus network acl show` pastes in unchanged.

| Field | Values |
|---|---|
| `action` | `allow`, `allow-stateless`, `drop` or `reject` (required) |
| `state` | `enabled`, `disabled` or `logged`. Left out it is `enabled`, which is what the `incus` CLI sends; the API itself refuses a rule with no state **[code]** |
| `source`, `destination` | Comma-separated CIDRs or ranges, or left out for any. On a bridge there are no ACL-name or `@internal` selectors; those are OVN-only **[docs]** |
| `protocol` | `icmp4`, `icmp6`, `tcp`, `udp`, or left out for any. Not `icmp` |
| `source_port`, `destination_port` | Ports or ranges (`53`, `"1000-2000"`, `"80,443"`), only with `tcp` or `udp`. Quote a list |
| `icmp_type`, `icmp_code` | Only with `icmp4` or `icmp6` |
| `description` | Free text |

## Attaching an ACL

No field of this kind attaches it; the other side names it, with `security.acls`, a comma list:

| Where | Key | Acts on |
|---|---|---|
| a `kind: network` bridge | `config: {security.acls: a,b}`, plus `security.acls.default.ingress.action`, `.egress.action` and `.logged` for traffic no rule matches | traffic crossing between the bridge and the host. It does **not** touch traffic between two instances on the same bridge **[docs]** **[lab]** |
| a NIC device of an instance or profile | `devices: {eth0: {type: nic, security.acls: a}}` | that NIC, including traffic to another instance on the same bridge **[docs]** |

### Order of creation

An ACL that a network, profile or instance names in `security.acls`, and that the stack declares, is created **before** it. Incus refuses to attach an ACL that does not exist **[code]**, so this is a requirement. The edge is matched by kind: a name that belongs to a profile is not read as an ACL.

A name the stack does **not** declare is allowed, since it may exist on the server already (made by hand). `plan` looks it up, and if the server does not have it either, says so on the resource that names it:

```
  network/svclab0: would update
      config.security.acls: "" -> "svclab0-in"
      warning: config security.acls names the network ACL "svclab0-in", which this stack does not declare and the server does not have: Incus will refuse to apply it until the ACL exists (declare a kind: network-acl for it)
```

`plan apply` prints the same warning as `network/svclab0: warning: ...`. For a NIC the place reads `device "eth0" security.acls`. If the server cannot be asked (a 403, a dropped connection) the warning says it could not be asked, never that the ACL is missing. The check is a warning, not a block: an ACL can be created between `plan` and `apply`.

### The disruption guard

Changing `security.acls` or `security.acls.default.*` on a bridge that instances use is **not counted as disruption**: it needs no `allow_disruption: true`, and does not make the plan BLOCKED. Every other key of a bridge is counted as before, and an edit that changes both is still blocked for the other key.

The reason: Incus applies these as a live change to its own firewall chains, not a reconfiguration of the bridge, so no address, NAT or DNS moves and nothing restarts. **[lab]** An ACL was created, given a rule, attached to a bridge that had running instances, and the default egress action was set, all with the instances running and none restarted; afterwards the allowed path was open, same-bridge and outbound traffic were open, and an instance added later was covered with no step taken for it. The same keys were unset at the end, also live. Two things the lab did not do: it probed before and after each change and not during it, so that no packet is dropped *while* the change is made is **[hypothesis]**; and it did not edit the rules of an ACL that was already attached (Incus re-applies the chains of every network that uses an ACL when the ACL is updated **[code]**, so this should be the same kind of change, untried). What an ACL *does* change is which traffic may cross the bridge, and that is its purpose: it is in the stack, and `plan` prints it as a `config.security.acls` line. A default of `reject` on a bridge whose callers were never listed will cut them off; that is the policy working, and `plan` is where to read it **[hypothesis: the lab set the default egress to allow, and rejecting by default was not tried]**.

## What `plan` and `plan apply` do

- **Missing:** `would create`; apply creates the ACL with the declared rules, description and config.
- **Present and equal:** `no changes`. Nothing Incus fills in is drift: a `state` you left out is `enabled`, and the spaces Incus trims from a list (`"10.0.0.1/32, 10.0.0.2/32"`) are trimmed before comparing.
- **Present and different:** `would update`, with a line for each difference, so a plan shows *which* rule:

  ```
  description: "old" -> "only the ingress helper may reach the apps on svclab0"
  config.user.owner: "" -> "smarthome"
  ingress rule 1: add (action=allow source=10.10.30.5/32 protocol=tcp destination_port=1883 state=enabled)
  ingress rule 2 (in the live list): remove (action=reject source=10.0.0.0/8 state=enabled)
  ingress rule 0: destination_port: "8123" -> "8080"
  ```

  A rule is *added* (its index in your list), *removed* (its index in the live list), or *changed* (the fields that differ, at its index in your list). Rules that match are found as a longest common subsequence, so inserting one rule in the middle is one `add`, and not a change to every rule after it. Indices count from 0, as Incus's own error messages do.
- **Never deletes an ACL.** An ACL the stack no longer declares stays, and the extras report ([`plan-extras.md`](plan-extras.md)) covers instances only, so it is not mentioned either.

### Rules are a list, in order

- **The order you write is the order sent and stored**, and it is compared as an ordered list. The same rules in another order are one `update`, reported as `ingress: rules reordered (the same 2; ...)`.
- **A reorder does not change filtering.** Incus applies `drop` rules, then `reject`, then `allow`, whatever the list order, and then the default for the direction **[docs]**; reject rules still came before an allow-everything rule when it was listed last **[verified]** ([`network-resources-design.md`](network-resources-design.md) appendix). The order is kept because the stored list is what `incus network acl show` prints, and a diff against the YAML should read the same; and because a rule in state `logged` takes its log name from its index **[code]**.
- **A direction you declare is the whole list for it.** A live rule the stack does not list is removed on apply: a stale `allow` left in a firewall is the drift this kind exists to catch. A direction you leave out (no `ingress:` at all) is **not managed** and its live rules are untouched; `ingress: []` declares "none". This is a different thing from deleting an ACL, which tink does not do.
- **A rule has no identity of its own** in Incus, so there is no "this rule changed" beyond the fields that differ at a position.

## Checked when the file is loaded

An invalid value fails before anything is read from the server, naming the resource and the rule: `resource "svclab0-in": ingress rule 1: action "permit" is not one of: allow, allow-stateless, drop, reject`.

| Checked | |
|---|---|
| `action`, `state`, `protocol` | one of Incus's values (`state` may be left out) |
| ports and ICMP fields | ports need `tcp` or `udp`; `icmp_type` and `icmp_code` need `icmp4` or `icmp6`, as Incus requires **[code]** |
| a rule repeated in one list | refused, as Incus refuses it **[code]** |
| the name | Incus's name rule |
| `project:` | refused (see below) |
| a `${secret:...}` anywhere in the document | refused, as for every kind |
| a field tink does not know, in the document or in a rule | refused |

Addresses and ports are **not** checked: a typo in a CIDR is reported by Incus when the ACL is created. A load-time check of them is an open question (design note, question 4).

## Which project it is in

An ACL is a **server-global** object here, exactly like `kind: network`. **[code]** Incus resolves the project an ACL lives in the way it does for a network: the default project, unless the project asked about has `features.networks`, which tink does not manage. So the ACL for a bridge is in the default project whatever project its instances are in, and an instance in any project can name it.

So `project:` on a `kind: network-acl` is a load error, not something silently ignored, and the stack's default project (`project:` on `kind: stack`) is never given to an ACL (nor to a network).

## Not in this kind

Address sets, OVN ACLs and their selectors, `tink export` of ACLs, rendering ACLs from other documents (a later feature), and deleting anything. Unchanged from the design note: whether a typo in a CIDR should be caught at load, and whether the removal of a rule or an ACL should one day be allowed once tink can say it owns what it created ([`authoritative-apply-design.md`](authoritative-apply-design.md)).
