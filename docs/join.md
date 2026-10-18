# `tink join`: what a published name needs from the site's helpers

**Status: built. `tink join` prints; `tink join apply dns` is the first renderer that writes; `plan apply` stamps each publish onto its instance and `tink join --from-server` reads them back.** All are unit-tested and the file-based join has been run on the author's own platform and canary stacks, where all five renderings below matched the pieces that were written by hand, including the control plane's live file **[live]**. The stamps and `--from-server` were run against a real Incus 7.5.1 server on 2026-10-17 **[live]**: `plan apply` wrote `user.tink.publish.<name>` as the compact, sorted JSON described below, and a second plan said no changes; changing a publish's groups updated the stamp, and dropping a publish removed only its stamp (`user.foo`, `user.tink.publisher` and `user.tink.role` on the same instance were left alone); a hand-written `user.tink.publish.*` key and a publish whose instance is not in the stack were refused; `tink join --from-server` printed exactly what the file-based join printed, and all five renderings matched the golden files (with one address substituted for the host bridge the lab does not have); a stamp with an extra field was an error naming its instance, exit 1, with nothing rendered; `join apply dns --dry-run --from-server` refused, writing nothing, on a helper without the `/db.<zone>` layout. **Not run:** `join apply dns` writing to a real CoreDNS helper. Only the dns records can be applied so far (see [Applying: dns](#applying-dns)). The design is in [offer-grant-join-design.md](offer-grant-join-design.md).

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
name: whoami            # the host label: whoami.home.example.org is this plus the dns helper's zone
instance: whoami        # the instance that makes the offer
offer: web              # which of its offers
allow: [family]         # groups, never people; a typed subject such as group:family is refused
```

A publish is a **declaration**. `tink plan` leaves it out of the dependency graph exactly as it leaves out `kind: stack`: it has nothing to create, nothing waits for it, and it may share its name with the instance it publishes (the host label and the instance are very often the same word, and the resolver's names are one flat namespace per stack). Two publishes with one name are an error, because a published name is unique in the zone.

### Stamps: how the server learns of a publish

`tink plan` and `tink plan apply` also **stamp** every publish onto the instance it names, as one config key per publish, so that whatever acts for the site later (the daemon helper) can read the declarations from the server and does not need the stack files:

```
user.tink.publish.whoami:        {"allow":["family"],"offer":"web"}
user.tink.publish.whoami-admin:  {"allow":["admins"],"offer":"web"}
```

- The key suffix is the publish's host label. The value is compact JSON with its keys sorted and the groups in the order the publish declares them, so the same stack always stamps the same bytes and a re-plan reads `no changes`. It carries **no secret**: a publish holds group names only. It names no instance (the key is on it).
- The stamps are **derived into the instance's desired config**, so the ordinary instance plan and apply converge them: a new publish is a planned `config.user.tink.publish.<name>: "" -> "{...}"` on the instance, a changed one a normal change. The publish itself stays out of the plan graph exactly as before.
- **A publish must be declared in the same stack and project as its instance.** A grant sits next to the thing it governs, and the server holds it there. If the instance is not in the stack (or is in another project) `plan` and `plan apply` stop with an error that says so. `tink join FILE...` is not held to this: it still reads a publish in one file and its instance in another, as it always did, but only a publish beside its instance reaches the server, and so only that one is seen by `--from-server`.
- **Declaring a key under `user.tink.publish.` by hand is refused** at load time: the stamp is derived, and a hand-written one would be overwritten or removed.

#### The owned prefix, and the one place tink removes a key

Everywhere else tink never removes a config key it is no longer told about: a key dropped from the YAML stays on the instance and `plan` reports it as "not in the stack" (see [plan-extras.md](plan-extras.md)). A stamp is different, because one that stays after its publish is deleted keeps the name **rendered**: still routed, still let in. So `user.tink.publish.` is **owned by tink**, and for it alone:

- when `plan` or `plan apply` handles an instance **that the stack declares**, every live key under `user.tink.publish.` that the stack no longer derives is a planned change, `config.user.tink.publish.<name>: "{...}" -> (unset)`, and `plan apply` removes it in the same update that sets the others (also on an instance rebuilt onto a new image);
- **no other key is touched**: not another `user.tink.*` key (`user.tink.role`, `user.tink.helper.status`, `user.tink.publisher`), not a foreign `user.*` key, not an Incus key. The rule is a prefix match with the trailing dot, so `user.tink.publish` and `user.tink.publisher` are not under it;
- it applies **only to instances in the stack being planned**. An instance the stack does not declare is never read for stamps, whatever it carries;
- a stack that has dropped *all* its publishes therefore removes all the stamps on its instances, which is what "stop rendering them" means;
- stamps are never listed as "not in the stack" (they are either declared by derivation or a planned removal), and `tink export` writes them back as `kind: publish` documents, so a stack exported from a server applies without taking the grants away.

**The helpers' own facts**, which the join needs to fill in the rest. Helpers are found by role, never by name:

| Key | On | Says |
|---|---|---|
| `user.tink.role: dns` / `ingress` / `sso` / `mesh` | each helper | which job it does. `ingress` must be exactly one and `sso` at least one; `dns` and `mesh` are optional, at most one each (without a `dns` helper there are no dns records to render; without `mesh` there is no mesh view) |
| `user.tink.dns.zone: home.example.org` | the dns helper, or the ingress helper when there is no dns helper | the zone every published name ends with. With both stated they must agree |
| `user.tink.leg.<nic>: serves` | the ingress helper | LAN clients reach it on this NIC: the address the dns records answer with. Needed when there is a dns helper |
| `user.tink.leg.<nic>: mesh` | the ingress helper | mesh clients reach it on this NIC: the address the mesh view answers with, and a source address the apps' ACLs admit (these coincide by design: the gateway advertises exactly that address **[to confirm]** if a site ever needs them apart). Needed when there is a mesh helper |
| `user.tink.leg.<nic>: public` | the ingress helper | the internet reaches it on this NIC, through the host's address: proxy devices on the host (`nat` on, so the helper sees the client's address) connect to it. A source address the apps' ACLs admit. Nothing is rendered for it: the names live in public DNS, outside tink |
| `user.tink.leg.<nic>: discovery` | any | why a NIC exists when it is not a way in for users (not read by the join) |

A NIC may have several purposes, separated by commas (`mesh, public`), and an address that is both is admitted once. At most one NIC has each of `serves`, `mesh` and `public`. A site needs at least one source address for its ACLs, so it has a `mesh` leg or a `public` leg; a LAN-only site with neither has no way for the ingress helper to be admitted, and that is an error rather than an ACL that admits nobody.

**A site with no dns helper** (a VPS whose names are in public DNS) states the zone on the ingress helper and gets `caddy`, `sso` and `acl` only. The join prints one warning naming the names that belong in public DNS and the address that carries them. It was written for the shape in `internal/join/testdata/public/`, whose hand-written pieces are `internal/join/testdata/public/golden/`. The `public` leg also checks, as warnings, that a proxy device on the host connects to its address and has `nat` on; see [public-exposure.md](public-exposure.md).

## Running it

```
tink join platform/platform.yaml canary/canary.yaml
tink join --only dns platform/platform.yaml canary/canary.yaml      # one section, no heading: easy to diff
```

Each argument is one stack (that is what gives its resources the stack's default project); a stack split across files is given as `a.yaml,b.yaml`. Output, for two publishes of one app:

```
== dns: records for the dns helper's hosts file (zone home.example.org)
192.0.2.231 whoami.home.example.org
192.0.2.231 whoami-admin.home.example.org

== mesh: ...                                  a JSON array of A records, the format of Headscale's extra_records file

== caddy: routes for the proxy, to sit inside the wildcard site block ...
@whoami host whoami.home.example.org
handle @whoami {
	import sso
	reverse_proxy 10.10.60.10:80 {
		import no_portal_cookie
	}
}
...
== sso: rules for the sign-on helper, to sit under access_control.rules
- domain: 'whoami.home.example.org'
  policy: one_factor
  subject: 'group:family'
...
== acl: rules for the ACLs on the apps' bridges, under ingress:
# network-acl canary0-in (network canary0)
ingress:
  - {action: allow, source: 10.10.50.3/32, protocol: tcp, destination_port: "80", state: enabled, description: "ingress helper publishes whoami, whoami-admin"}
```

### From the server

```
tink join --from-server                       # the same five renderings, from what the server holds
tink join --from-server --only dns
tink --remote lab join --from-server          # --remote and --socket work as for the other commands
```

With `--from-server` the files are not read at all (and giving both is an error: two sources would leave it unclear where a rendering came from). The join is built from the live server, **read-only** (only list calls are made; nothing is written):

| What | Read from |
|---|---|
| instances | every project: name, project, and the **stored** config, devices and profiles (not Incus's expanded view, which already has the profiles merged in) |
| profiles | every project, because an offer's NIC may come from a profile |
| networks | the managed ones (server-global); the ACL is found from `security.acls` |
| publishes | one per `user.tink.publish.*` stamp, in the project of the instance that carries it |

The same `Build` and the same renderers then run, so for stacks that have been applied the output is the same as the file-based join's (a test builds a server from the fixture stacks, applies the stamps the way `plan` does, and compares each section with the hand-written golden files). The finding texts are the file-based ones: "which no file given declares" then means "which the server does not hold".

**A stamp that cannot be read is an error that names its instance, and nothing is rendered.** The value must be one JSON object with exactly the fields `allow` and `offer`, each once, nothing after it, and the publish it describes must pass the same rules a `kind: publish` document is held to at load time (a DNS label for the name, a plain group name, no quote or control character, and so on). Anything else is reported as

```
error: instance/whoami: (project canary) user.tink.publish.whoami: "instance" is not a field of a publish stamp (known: allow, offer)
```

and the exit status is 1. A stamp is never skipped and never repaired: a name half-read would be half-rendered. Two instances stamping one name are the join's own duplicate error.

What the daemon will read is exactly this: `tink join apply dns --from-server` makes the dns helper's `/hosts` equal to the `dns` rendering of the server's stamps, with the same refusals as below. Zero stamps is refused like zero publishes in files (the `--allow-empty` flag says it is meant), because an empty server also looks like a server that `plan apply` has not stamped yet.

What each section is shaped for, and what it assumes:

- **dns**: a hosts-file line per name. Fits CoreDNS's `hosts` plugin; every name answers with the ingress helper's `serves` address. Left out when no helper has role `dns`.
- **mesh**: Headscale's `extra_records` entries, answering with the ingress helper's `mesh` address. Left out, with a warning, when no helper has role `mesh`.
- **caddy**: `handle` fragments for the **wildcard** site, not a site block per name: a site block of its own would ask for its own certificate instead of using the wildcard one **[docs, from memory]**. The upstream is the app's **address**, because names do not resolve across bridges. They import two snippets the platform's Caddyfile defines by hand: `sso` (the forward-auth call to the sign-on helper) and `no_portal_cookie` (which strips the portal's zone-wide session cookie before a request reaches the app, so an app cannot replay a user's portal session against another host).
- **sso**: one Authelia rule per name, `one_factor` (a publish does not choose; a field for it arrives when something needs two factors). Several groups render as an OR of single-group lists, `[['group:a'], ['group:b']]`, which Authelia's own policy checker confirmed means "either" **[live]**.
- **acl**: one rule per port per ACL: the ingress helper (from its `mesh` and `public` legs) may reach it; two sources are written as one comma-separated list. Several publishes of one port collapse into one rule whose description names them. The ACL is found from the network the app's NIC is on (`security.acls` on the `kind: network`).

## Applying: dns

```
tink join apply dns platform/platform.yaml canary/canary.yaml
tink join apply dns --dry-run platform/platform.yaml canary/canary.yaml      # show the change, write nothing
tink join apply dns --from-server                                            # from the stamps on the server, with no files
```

It makes the dns helper's record file equal to the `dns` rendering and nothing else, through the Incus API with the certificate of whoever runs it (`--remote` works); the same code is meant to be run by the daemon helper on a timer.

**The layout it needs on the dns helper** (measured against CoreDNS 1.14.7 **[lab]**):

- **`/hosts` is the generated file and holds nothing else.** CoreDNS's `hosts` plugin answers from it first and re-reads it every few seconds, so a change needs no restart: a new name answered in about 5 s and a removed one stopped in about 6 s. The file is rewritten whole, so a name that is no longer published disappears.
- **The platform's own static names go in the zone file, `/db.<zone>`** (the sign-on portal, the directory's UI, the helper itself). `hosts` falls through to it for a name it does not have. An empty `/hosts` therefore takes down no platform name.
- **The two cannot be two hosts files.** CoreDNS refuses to start with a second `hosts` plugin in a server block (`this plugin can only be used once per Server Block`): found when a lab instance crashed on it.
- **Where a name is in both, `/hosts` wins.** An app published as `auth` would silently take the portal's name.

**What it refuses, writing nothing:**

| Refusal | Why |
|---|---|
| the join has any error | a partial rendering looks like a complete one |
| no published name is found | applying would replace every record with none, which usually means the wrong files were given (or, with `--from-server`, that nothing is stamped yet); `--allow-empty` says it is meant |
| a published name is already a record in the zone file | the generated record would answer first and take the name over |
| `/hosts` exists and does not begin with the note the join writes | it may hold hand-written records; move the platform's own to the zone file, then give `--adopt` once |
| the zone file is missing, or any read fails | a failed read is an error, never an empty file |

After a write it reads the file back and fails if it does not hold what was written. A second run reports `no changes`. The author's platform was moved over in three steps: the static names into the zone file (one planned restart of the dns helper, about 7 s), a dry run, then `--adopt` once.

**What it needs from Incus** (so the credential it runs under can be judged): it reads two files and writes one, all in the dns helper instance and its project: `/db.<zone>`, `/hosts` and `/hosts` again to check. No instance, profile, network, volume or trust object is read or changed **[code]**. The entitlement is the instance file access one (`can_access_files` on that instance **[docs, from memory; to be measured]**).

With `--from-server` it also **reads** the instances of every project, the profiles of every project and the networks (list calls only), to build the join. That is a wider read than the file-based apply needs, and it is what the daemon helper's certificate will have to be able to do **[code; the entitlements are not yet measured]**. Writing is unchanged: the same two files in the dns helper.

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
| no ingress helper, or more than one ingress or dns helper; no zone (on the dns helper, or on the ingress helper when there is no dns helper), or two that differ; no `serves` leg when there is a dns helper, no `mesh` leg when there is a mesh helper, or neither a `mesh` nor a `public` leg; two NICs with one purpose; a leg word that is not `serves`, `mesh`, `public` or `discovery`; a read leg on a NIC without a static address; no sign-on helper | error |
| the same name published twice; an instance declared twice | error |
| `--from-server`: a publish stamp that is not valid JSON, has a field it should not or lacks one, or breaks a rule of `kind: publish` (the message names the instance, its project and the key) | error |
| `plan`: a publish whose instance is not in the same stack and project | error (from `plan`, not from `join`) |
| an app's `http` offer that no publish shows (helpers are not asked) | warning |
| the app's network is not declared in the files given, so its ACL cannot be shown | warning |
| no helper has role `mesh`, though a `mesh` leg is stamped | warning |
| no helper has role `dns`: the names that belong in public DNS | warning |
| the `public` leg has no host proxy device connecting to its address, or some of those lack `nat` | warning |
| no publish at all in the files given | warning |

## What it does not do

- **`join` itself writes nothing**: with files it is a pure function of them and reads no server; with `--from-server` it reads the server and still writes nothing. Only the dns rendering can be applied. The others need a decision on who holds the credential for reconcile jobs ([open question 7](offer-grant-join-design.md#open-questions)) when the daemon helper runs them, and the ACL rules need a way for the `network-acl` kind to mark a rule list as derived: that kind treats a direction it is given as the whole list, so a declared `ingress:` would erase a derived rule on the next plan. The mesh records are written on the control plane's host, which is its own decision. The daemon loop itself, which would run `join apply dns --from-server` on a timer, is not built.
- **It does not check the listener.** Whether an app listens only where it should (the bypass on a discovery leg) is the next check; an image with no shell, like `whoami`'s, will have to answer *unverifiable*, never *ok*.
- **Only web.** An `access` document (one instance using another's offer, an MQTT broker, say), paths that bypass sign-on (Home Assistant's companion app), names that are LAN-only, and a per-name authentication strength are not modelled. The platform's own routes (the sign-on portal, the directory's UI) are not published apps and stay hand-written.
