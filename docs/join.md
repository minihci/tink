# `tink join`: what a published name needs from the site's helpers

**Status: built, print-only.** `tink join` and `kind: publish` are unit-tested, and have been run on the author's own platform and canary stacks, where all five renderings below matched the pieces that were written by hand, including the control plane's live file **[live]**. Nothing is applied from them yet: `join` reads the files you name and prints. The design is in [offer-grant-join-design.md](offer-grant-join-design.md).

## The idea

Publishing an app behind sign-on touches five places that have to agree: the dns records, the mesh control plane's records, the proxy's routes, the sign-on helper's access rules, and the ACL on the app's bridge. Written by hand, one name is five edits in five files, and nothing notices when one is forgotten. With `join` the app says what it **provides**, the site says who may **use** it under what **name**, and the join derives the five.

```
an app states an offer ──┐
                         ├── join ──> dns records, mesh records, proxy routes, sign-on rules, ACL rules
the site publishes it ───┘                     (printed; compared with the hand-written ones)
```

## The three pieces

**An offer** is a fact the app states about itself, as keys on its instance. It says nothing about who may use it.

```yaml
kind: instance
name: whoami
config:
  user.tink.offer.web.port: "80"
  user.tink.offer.web.proto: http
  # user.tink.offer.web.listen: eth0     # only needed when the instance has more than one NIC
devices:
  eth0: {name: eth0, type: nic, network: canary0, ipv4.address: 10.10.60.10}   # the address must be static (see below)
```

**A publish** gives one offer a name in the site's zone and says which groups may use it. The same offer can be published twice, to different groups.

```yaml
kind: publish
name: whoami            # the host label: whoami.home.xlii.co is this plus the dns helper's zone
instance: whoami        # the instance that makes the offer
offer: web              # which of its offers
allow: [family]         # groups, never people; a typed subject such as group:family is refused
```

A publish is a **declaration**. `tink plan` leaves it out of the dependency graph exactly as it leaves out `kind: stack`: it has nothing to create, nothing waits for it, and it may share its name with the instance it publishes (the host label and the instance are very often the same word, and the resolver's names are one flat namespace per stack). Two publishes with one name are an error, because a published name is unique in the zone.

**The helpers' own facts**, which the join needs to fill in the rest. Helpers are found by role, never by name:

| Key | On | Says |
|---|---|---|
| `user.tink.role: dns` / `ingress` / `sso` / `mesh` | each helper | which job it does. `dns` and `ingress` must be exactly one; `sso` at least one; `mesh` is optional (without it there is no mesh view) |
| `user.tink.dns.zone: home.example.org` | the dns helper | the zone every published name ends with |
| `user.tink.leg.<nic>: serves` | the ingress helper | LAN clients reach it on this NIC: the address the dns records answer with |
| `user.tink.leg.<nic>: mesh` | the ingress helper | mesh clients reach it on this NIC: the address the mesh view answers with, and the source address the apps' ACLs admit (these coincide by design: the gateway advertises exactly that address **[to confirm]** if a site ever needs them apart) |
| `user.tink.leg.<nic>: discovery` | any | why a NIC exists when it is not a way in for users (not read by the join) |

## Running it

```
tink join platform/platform.yaml canary/canary.yaml
tink join --only dns platform/platform.yaml canary/canary.yaml      # one section, no heading: easy to diff
```

Each argument is one stack (that is what gives its resources the stack's default project); a stack split across files is given as `a.yaml,b.yaml`. Output, for two publishes of one app:

```
== dns: records for the dns helper's hosts file (zone home.xlii.co)
10.0.142.231 whoami.home.xlii.co
10.0.142.231 whoami-admin.home.xlii.co

== mesh: ...                                  a JSON array of A records, the format of Headscale's extra_records file

== caddy: routes for the proxy, to sit inside the wildcard site block ...
@whoami host whoami.home.xlii.co
handle @whoami {
	import sso
	reverse_proxy 10.10.60.10:80 {
		import no_portal_cookie
	}
}
...
== sso: rules for the sign-on helper, to sit under access_control.rules
- domain: 'whoami.home.xlii.co'
  policy: one_factor
  subject: 'group:family'
...
== acl: rules for the ACLs on the apps' bridges, under ingress:
# network-acl canary0-in (network canary0)
ingress:
  - {action: allow, source: 10.10.50.3/32, protocol: tcp, destination_port: "80", state: enabled, description: "ingress helper publishes whoami, whoami-admin"}
```

What each section is shaped for, and what it assumes:

- **dns**: a hosts-file line per name. Fits CoreDNS's `hosts` plugin; every name answers with the ingress helper's `serves` address.
- **mesh**: Headscale's `extra_records` entries, answering with the ingress helper's `mesh` address. Left out, with a warning, when no helper has role `mesh`.
- **caddy**: `handle` fragments for the **wildcard** site, not a site block per name: a site block of its own would ask for its own certificate instead of using the wildcard one **[docs, from memory]**. The upstream is the app's **address**, because names do not resolve across bridges. They import two snippets the platform's Caddyfile defines by hand: `sso` (the forward-auth call to the sign-on helper) and `no_portal_cookie` (which strips the portal's zone-wide session cookie before a request reaches the app, so an app cannot replay a user's portal session against another host).
- **sso**: one Authelia rule per name, `one_factor` (a publish does not choose; a field for it arrives when something needs two factors). Several groups render as an OR of single-group lists, `[['group:a'], ['group:b']]`, which Authelia's own policy checker confirmed means "either" **[live]**.
- **acl**: one rule per port per ACL: the ingress helper (on its `mesh` leg) may reach it. Several publishes of one port collapse into one rule whose description names them. The ACL is found from the network the app's NIC is on (`security.acls` on the `kind: network`).

## Findings

Findings go to standard error. **Any error means nothing is printed and the exit status is 1**: a partial rendering (a mesh view missing one name) looks exactly like a complete one. Warnings are printed beside a complete rendering.

| Finding | Severity |
|---|---|
| a publish names an instance the files do not declare, or an offer the instance does not make (the message lists the ones it does) | error |
| the offer is not `http` (a route and a sign-on rule are for web traffic) | error |
| an offer key tink does not know (`user.tink.offer.web.prot`), a missing `port` or `proto`, a port out of range | error |
| the offer's NIC takes DHCP: the proxy and the ACL name an address, so it must be fixed | error |
| the offer's NIC is on an unmanaged bridge: no ACL can restrict who reaches it | error |
| the instance has several NICs and the offer does not say which (`...listen`) | error |
| the app's network has no ACL attached (`security.acls`), or several | error |
| no dns helper, no zone, or more than one dns or ingress helper; no `serves` or `mesh` leg, or a leg on a NIC without a static address; no sign-on helper | error |
| the same name published twice; an instance declared twice | error |
| an app's `http` offer that no publish shows (helpers are not asked) | warning |
| the app's network is not declared in the files given, so its ACL cannot be shown | warning |
| no helper has role `mesh` | warning |
| no publish at all in the files given | warning |

## What it does not do

- **It applies nothing and reads no server.** It is a pure function of the files. Applying the renderings needs a decision on who holds the credential for reconcile jobs ([open question 7](offer-grant-join-design.md#open-questions)), and a way for the `network-acl` kind to mark a rule list as derived: that kind treats a direction it is given as the whole list, so a declared `ingress:` would erase a derived rule on the next plan.
- **It does not check the listener.** Whether an app listens only where it should (the bypass on a discovery leg) is the next check; an image with no shell, like `whoami`'s, will have to answer *unverifiable*, never *ok*.
- **Only web.** An `access` document (one instance using another's offer, an MQTT broker, say), paths that bypass sign-on (Home Assistant's companion app), names that are LAN-only, and a per-name authentication strength are not modelled. The platform's own routes (the sign-on portal, the directory's UI) are not published apps and stay hand-written.
