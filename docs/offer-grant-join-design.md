# Design: offers, grants and the join

**Status: a design note, 2026-10-13. Nothing in it is built.** It writes down a pattern that [what-tink-is.md](what-tink-is.md#offers-and-grants) names and that, until now, existed only in a working session: apps **offer**, the site **grants**, and a **join** works out who can actually reach what. It is for the author to review. Words are explained in [the glossary](glossary.md) (role, engine, helper, daemon helper, platform stack). Claims are marked **[docs]** (this repository), **[code]**, **[lab]** (seen on a real host), **[proposed]** (not built) or **[to confirm]**. Each decision has a status: **decided** (chosen over alternatives, for the recorded reason), **defaulted** (chosen without comparing; open to replacement) or **open**. Examples use generic names (`home.example.com`, `br0`), not the author's site. The YAML sketches take their shape from a two-file mock stack the author drafted (a platform stack and a smarthome app stack, not in this repository, never meant to be applied); every kind and key name in it is a sketch, and stays one here.

## The job

Several roles keep asking one question: who may reach or use what? Ingress asks it of a web app, single sign-on of a login, the mesh of a whole bridge, a network ACL of one instance talking to another. Today the answer is written by hand in each engine's own language, and nothing checks the answers against each other. In the code there is one half of the pattern: apps state what they offer to the ingress, and nothing grants or joins ([below](#what-exists-today-and-a-first-step)).

The pattern splits the question into three parts that have different authors and different lifetimes.

| Part | What it is | Written by | Lives |
|---|---|---|---|
| **offer** | a fact an app states about itself | whoever writes the app's stack | on the instance, under `user.tink.*`, and travels with the app |
| **grant** | the site's authority: who may use an offer, at what level | whoever runs the site | in the site's own stack, beside the thing it governs |
| **join** | a computation, reads only: offers, grants and topology in; effective access and findings out | tink, in `plan` | nowhere; it is recomputed from live state |

It follows the house rules: tink holds no state of its own, so the join is derived from what Incus reports **[docs: CLAUDE.md]**; a role's engine sits behind the role, so the join's output is engine-neutral and each role renders it into its engine's configuration ([roles and engines](what-tink-is.md#roles-and-engines)).

## The decisions at a glance

| # | Decision | Status |
|---|---|---|
| D1 | Offers are neutral facts the app states about itself | decided; key spelling open |
| D2 | A grant is the site's authority and sits next to the thing it governs | decided; the other placement is defensible too |
| D3 | Exposure and authorization are different layers | decided |
| D4 | The join computes effective access over hops it derives from topology; a hop protects only if it is a choke point | decided |
| D5 | Subjects are typed; grants for people name groups | decided |
| D6 | Two directions: exposure (offer to grant) and requirement (need to provide) | decided |
| D7 | Grants may carry levels, exclusivity, enforcement coverage and a freshness class | decided that the model carries them; details open |
| D8 | tink stays out of what it does not manage | decided |
| D9 | The ingress helper reaches apps through the host, behind an ACL on each app bridge | decided |

## D1. Offers are neutral facts the app states about itself (decided; spelling open)

An offer says what the app knows: it listens on 8123, speaks HTTP, wants OIDC with these redirect URIs. It never says who may use it. The keys name no engine, so the engine behind a role can be swapped: an app that offers "HTTP on 8123" is routable by Caddy today and by another proxy tomorrow without touching the app's stack.

```yaml
# SKETCH. Every key name is undecided.
kind: instance
name: ha
config:
  user.tink.offer.web.port: "8123"
  user.tink.offer.web.proto: http
  user.tink.offer.web.listen: eth0   # where the app is meant to listen; the join can check it
  user.tink.leg.eth1: discovery      # why this NIC is on the LAN: not a way in for users
```

An app that wants single sign-on states its OIDC registration the same way (redirect URIs, whether it reads claims from the ID token or from `userinfo`, whether it needs a JWT access token), as [what-tink-is.md](what-tink-is.md#what-this-means-for-tink) describes. The last two keys above go beyond port and protocol because the join needs them to tell a bypass from an intended listener ([D4](#d4-the-join-effective-access-over-hops-it-derives-from-topology-decided)): the first is a fact about the app, the second about the instance **[proposed]**.

- **Reason.** The app's author knows the app and not the site. Letting an offer say who may use it would let an app widen its own audience, and would make a shared stack carry one site's people ([the three reasons for the split](what-tink-is.md#offers-and-grants)).
- **Spelling: open.** `user.tink.offer.<name>.*` is a sketch, where `<name>` identifies one offer on the instance (an app can offer more than one). It stays under `user.tink.*`, which `cmd/tink/keynamespace_test.go` enforces **[code]**.
- **Ingress adopts the final form directly (decided).** No transitional keys: no intermediate spelling that exists only to be replaced. The old `user.ingress.*` names are still read, because instances in other repositories, on hosts in use, register with them, and a reconciler that stopped seeing them would delete their routes **[code: `internal/ingress/keys.go`]**. A notice already asks for the rename **[code]**.
- **Alternative.** Keep one key family per role (`user.tink.ingress.*`, `user.tink.sso.*`). Simple, and it is what ingress has today. It lets each role's keys drift into that engine's vocabulary, which is the coupling D1 exists to avoid.

## D2. Grants are the site's authority, placed next to the thing they govern (decided)

A grant lists who may use an offer. It is written by whoever runs the site. The decision is to place each grant next to the thing it governs, as a document of its own per offer, written right after the instance that makes the offer, and not in one site-wide list. Two sketched kinds cover the cases in the mock: `publish` for people (a host name that shows an offer, and the groups allowed) and `access` for another instance.

```yaml
# SKETCH. The kind names (publish, access) and every field name are not decided.
kind: publish
host: ha                           # the site's choice; the zone the dns helper serves adds the rest
offer: ha/web                      # the app's fact: port 8123, http
allow: [family, admins]            # groups, never a person
---
kind: access
offer: mosquitto/mqtt              # an offer that other instances use
allow: ["instance:ha", "instance:zigbee2mqtt"]   # quoted: see D5
```

In the mock, `publish` is the only document that names people, and no grant names a hop, a proxy or an engine: those are derived.

- **Reason.** Policy beside its subject leaves fewer omissions. An offer with no grant document is visible where it is declared, and an edit to one cannot be made without seeing the other. The review that a single list would give can be generated: tink prints the joined relation ([D4](#d4-the-join-effective-access-over-hops-it-derives-from-topology-decided)), so the whole site is readable in one place without being written in one place.
- **The other placement is defensible too, and the author said so.** One site-wide file of grants means one thing to review, one diff in a pull request, and a natural home for rules that span several offers (everything the family may use). Its cost is that an offer and its policy are far apart, so the omission is found by `plan` and not by looking. The trade-off is real, which is why the printed view and the open question on accepting it as input ([open question 5](#open-questions)) exist.
- **Where a grant sits when the app's stack is shared is not settled.** A grant is the site's, so it cannot ride inside a stack shared between sites. In the mock the grants are documents in the site's own stack for its apps, each right after the instance it governs, while offers are keys on the instance; a stack shared between sites could then carry the offers and leave the grants out **[to confirm]**.
- **This differs from the sketch in what-tink-is.md**, where one `kind: <role>` document holds an `allow:` list for the whole site. That sketch should be updated when this is built.

The kind names `access`, `publish` and `mesh` are sketches, not decisions. `publish` gives an offer a name for people, and from it tink renders the ingress route, the sign-on rule and the zone record; `mesh` names the gateway, the control plane it needs and what it may reach (in the mock, the ingress helper and nothing else).

## D3. Exposure and authorization are different layers (decided)

- **Exposure:** can a packet get there? Bridge membership, a network ACL, a mesh route, a proxy route.
- **Authorization:** is this subject allowed? A single sign-on group.

"Only family may reach HA" is an authorization statement. It is enforced at the single sign-on hop, and no exposure layer can state it, because exposure layers do not see people. A source-address rule on a proxy is exposure: it says which networks, and identifies nobody.

- **Reason.** Conflating them is how a firewall rule gets read as a statement about people, and how a site believes it has restricted something to a group when it has restricted it to a subnet.
- **In the mock** the `allow` of a `publish` is the authorization statement, enforced at the sign-on hop. Whether a client can reach the host at all (a LAN leg, or the mesh gateway to the ingress helper) is the platform's, and is not written in the grant.
- **Consequence.** A grant for people needs at least one hop on every path that can see people. If none can, that is a finding ([D4](#d4-the-join-effective-access-over-hops-it-derives-from-topology-decided)).

## D4. The join: effective access over hops it derives from topology (decided)

```
offers  (instance config)  ---+
grants  (the site's stack) ---+--> join --> effective access: (subject, target, level)
topology (Incus, helpers)  ---+         \-> findings
```

The join returns an engine-neutral relation of (subject, target, level), and findings. It reads Incus, the way `plan` already does, and writes nothing **[docs: CLAUDE.md]**.

- **Hops are derived from topology and never written by users.** Reason: a bypass is by definition a path nobody declared, so paths cannot come from declarations; a hand-written list of hops would also be a second copy of the topology, going stale. Topology comes from Incus (NICs, networks, ACLs, proxy devices, instance state) and from each role's helper (routes, policy), plus a look inside an instance where only the instance knows (an open listener; see the [example](#worked-example-home-assistants-intended-path)) **[proposed]**.
- **A path** is an ordered list of hops from where a subject starts to the target.
- **A hop** is a point where something can say no. It has a **layer** (exposure or authorization), an **enforcer** (an Authelia rule, an Incus ACL, a Caddy matcher, a mesh policy) and the **subject kinds it can check** ([D5](#d5-subjects-are-typed-decided)).
- **A blind hop** cannot tell who is asking. A network ACL sees addresses, not a person.
- **Access holds only if every hop on the path permits.**
- **A hop protects only if it is on every path to the target.** That is a **choke point**. Single sign-on in front of an app protects nothing if a second route to the app skips it.
- **An extra unprotected path is a bypass.** The join reports it as a finding. A deliberate one is recorded with `accept:` and a reason, which turns the finding into a note that is still printed.

```yaml
# SKETCH. Where accept: sits and its fields are not decided.
accept:
  - {finding: bypass, path: "lan > ha:8123", reason: "no way yet to stop the discovery leg listening"}
```

- **A path tink cannot close is still reported.** Frigate's WebRTC media (port 8555) goes from the browser straight to the relay inside Frigate, around the proxy and single sign-on. The mock leaves that offer commented out. Enabled, the join should say it is a path it cannot protect, and the site could record it with `accept:` **[proposed]**.
- **Topology spans stacks, so the join reads live Incus and not one stack's file.** The ingress helper sits on the platform bridge and the apps on bridges of their own, which belong to other stacks, so no single stack's file shows a path from one to the other. [D9](#d9-the-ingress-helper-reaches-apps-through-the-host-behind-an-acl-on-each-app-bridge-decided) records how that path is made and filtered.

**Findings** (all **[proposed]**): an offer with no grant (a note: unreachable, the safe default); a grant with no offer (a warning); a grant that names a group the directory does not have (the check [identity-projector-design.md](identity-projector-design.md) already plans for `plan`); a grant for a subject kind that no hop on the path can check (nothing enforces it); a bypass; a hop whose enforcer cannot be installed here ([D7](#d7-levels-exclusivity-coverage-and-freshness-decided-that-the-model-carries-them)).

**What the join can promise.** Only about the paths tink can see. A hand-written route in a proxy tink does not manage is invisible to it ([D8](#d8-tink-stays-out-of-what-it-does-not-manage-decided)), so a clean join means "no unprotected path among the known ones", and its output should say so.

## D5. Subjects are typed (decided)

| Subject | Means | Checked by |
|---|---|---|
| `family` (a bare name) | a group in the directory | single sign-on, the mesh policy |
| `instance:sso` | an instance | a network ACL on its NIC, a listener's source. The directory's LDAP offer is granted to the sign-on helper alone in the mock |
| `anyone` | no identity required | any hop; the grant is only about exposure. The login page itself is published to `anyone` in the mock, because it must be reachable before anyone is logged in |
| `cidr:192.0.2.0/24` | a network range | a proxy matcher, an ACL, the mesh |

- **Grants for people name groups, never a single person, and there is no subject registry.** Reason: the directory owns people, groups and credentials, and tink only checks that a named group exists ([what the identity work decided](what-tink-is.md#what-this-means-for-tink)); a registry of subjects would be state tink was built not to keep.
- **A typed subject must be quoted in a YAML flow list.** `["instance:ha"]` is a string; unquoted, a YAML 1.1 parser reads `instance:ha` as a mapping. Found while drafting the mock; the loader needs to say so when it meets a mapping where a subject was meant **[to confirm]**.
- **`allow: cidr` is an acceptable alternative to single sign-on** for a path that is deliberately trusted by network (a printer proxy with no login, say). It is honest about what it does: nobody is identified, and the join prints it that way.

## D6. Two directions: exposure and requirement (decided)

- **Exposure (offer to grant):** what I provide, and who may use it. Most of this note.
- **Requirement (need to provide):** what I need, and what provides it. There is no subject here; the question is whether something answers.

Requirement is already in the code and is already join-shaped:
- A volume's `backup.copies` must name a `kind: backup-target` in the stack, checked at load **[code: `internal/resolve/backuptarget.go`]** ([volume-backup.md](volume-backup.md)).
- A `${secret:NAME}` in an instance's `environment.*` names a secret that must exist in the store **[code: `internal/resolve/secretrefs.go`]** ([secrets.md](secrets.md)).
- The mesh gateway's control plane is a provided need: the gateway needs a server address and a key, and tink's default provider is a Headscale helper, though Tailscale's hosted service or someone else's Headscale also provides it **[docs: what-tink-is.md]**.

**A need can name a provider in another stack, and the check is per stack today.** The mock's volumes name backup targets that are declared in a different stack, and load-time validation rejects a copy whose target is not in the stack **[code]**. This is the cross-stack reference question ([open decision 7](what-tink-is.md#open-decisions)) arriving through the join. A smaller case of the same shape: HA's `trusted_proxies` must name the ingress helper's address on the app bridge, which another stack owns.

Reason for putting both under one join: a need with no provider and a grant with no offer are the same mistake seen from two sides, and `plan` should report them the same way.

## D7. Levels, exclusivity, coverage and freshness (decided that the model carries them)

| Property | What it adds | Example |
|---|---|---|
| **Level or attribute** | a grant says how much, not only whether: read or admin | `family` may use HA; `admins` may administer it. Level names are open |
| **Exclusive, or a cardinality** | one holder per device, or per static address | a USB radio dongle; a static address outside the router's pool, as the dns lab's helper has on `br0` **[lab]**. The mock picks four LAN-leg addresses by hand and nothing records which are taken |
| **Enforcement coverage** | whether the hop's enforcer can be installed here at all | an unmanaged bridge such as `br0` cannot take an Incus ACL **[lab]**, so the join says "not enforceable here" and does not count the hop as protection |
| **Freshness class** | how fast a change reaches the enforcer | below |

- **Exclusivity has a precedent in the code.** Ingress already refuses a contested domain: "claimed by multiple instances, skipping all of them", so no claimant wins by accident **[code: `internal/ingress/discover.go`]**. An exclusive grant held twice is reported the same way.
- **Coverage includes the east-west case.** Filtering between two instances on one bridge needs the ACL on the NIC, not on the bridge, and ACL groups and `@internal` selectors are not supported on bridges **[docs: network-resources-design.md]**.
- **Freshness classes, observed:** Authelia reads LDAP groups live (a change reached an existing session within the 1-minute refresh), but its own configuration, the OIDC clients and the access rules, is static until a restart **[lab]**. Headscale policy holds groups as a static snapshot, so a directory change reaches the mesh only when the snapshot is re-projected and set **[docs: identity-projector-design.md]**. That needs a periodic reconcile in the daemon helper, with naive polling first **[proposed]**, using the `check_output` guard so the policy converges to what is declared ([exec-output-check-design.md](exec-output-check-design.md)). The join can then say that a person removed from a group loses single sign-on access within about a minute and the mesh hop at the next reconcile **[proposed]**.

## Worked example: Home Assistant's intended path

The intended path, as the author stated it on 2026-10-13: **LAN or mesh, then Caddy, then Authelia, then Home Assistant**, with **no public path**. The mesh reaches Caddy and not HA directly. HA's second interface, on the LAN bridge, exists only so HA can hear multicast, SSDP and zeroconf discovery (which the author decided matters for HA in general); it is not a way in for users and must not listen. The design follows the intended path. A bypass is the exception, and the join's job is to report it.

```
client on the LAN  ---> ingress helper (Caddy) ---> sso helper (Authelia) asked by Caddy ---> host routing ---> ACL at the app bridge ---> ha
client on the mesh ---> mesh helper (gateway) ---> ingress helper ---> sso helper ---> host routing ---> ACL at the app bridge ---> ha
                                 ha's second leg, on br0:  discovery only, must not listen
```

**The documents** are the ones in the [D1](#d1-offers-are-neutral-facts-the-app-states-about-itself-decided-spelling-open) and [D2](#d2-grants-are-the-sites-authority-placed-next-to-the-thing-they-govern-decided) sketches. In the mock they are everything the site writes about who can reach HA: its offer (port, protocol, where it is meant to listen, why its second NIC exists) and one `publish` that grants `family` and `admins` the host people touch. Nothing in the app stack names a proxy, a route, an Authelia rule, a zone record or a mesh route: the platform stack supplies the helpers, and tink renders the routes, rules and records from the `publish` documents of every stack (four reconcile jobs; see [open question 7](#open-questions)). The mesh declaration lets the gateway reach the ingress helper and nothing else. The hops are derived:

| Hop | Layer | Enforcer | Can check | Note |
|---|---|---|---|---|
| client to ingress, from the LAN | exposure | the LAN's reachability; a Caddy source matcher | `cidr` | blind to people |
| client to ingress, from the mesh | exposure | the gateway's advertised routes, then the mesh policy | the ingress helper's address only; mesh groups (a snapshot) | after the gateway masquerades, later hops see the gateway and not the person **[to confirm]** |
| ingress route for the host | exposure | Caddy | `cidr`; hands the person check to the next hop | rendered from the `publish` |
| single sign-on | **authorization** | an Authelia access rule | `group`, `anyone` | the only hop that sees a person; rendered from the `publish` |
| ingress to ha, through the host | exposure | a network ACL at the app bridge's boundary | `cidr` (the ingress helper's address) | no leg on the app bridge ([D9](#d9-the-ingress-helper-reaches-apps-through-the-host-behind-an-acl-on-each-app-bridge-decided)); the ACL is rendered from the `publish`, rejects every other source by default, and covers instances added to the bridge later **[lab]** |

The join prints one line per path:

```
family  -> ha.home.example.com  lan:   client > ingress > sso > ha                   ok
family  -> ha.home.example.com  mesh:  client > mesh > ingress > sso > ha            ok
admins  -> ha.home.example.com  lan:   client > ingress > sso > ha                   ok   (and the same over the mesh)
(any)   -> ha:8123 on eth1      br0:   client > ha                                   BYPASS: ha listens on its discovery leg
```

No levels appear: the mock does not use them ([D7](#d7-levels-exclusivity-coverage-and-freshness-decided-that-the-model-carries-them)).

**Why sign-on is only a choke point if the last line is absent.** Authelia protects HA only if every path passes it. Caddy to HA is one path. HA's own second leg is another, if HA listens there: any host on the LAN could reach port 8123 and skip both Caddy and Authelia. The join knows which listener is intended from the offer's `listen` and which NIC is a discovery leg from its stated purpose, and a listener on a leg that is not for serving is the finding.

**Why the join has to look.** In HA 2026.9.4 the HTTP settings (`server_host` and the rest) are stored config: a YAML `http:` block is migrated once at first boot and ignored afterwards (`homeassistant/components/http/config.py`, and the repair issue `yaml_still_present_after_migration`) **[docs: read in upstream source]**. Adding `server_host` to the YAML after first boot changed nothing, and HA listened on all interfaces **[lab]**. So HA's configuration file cannot say where it listens, and the join checks the fact: `netstat -ltn` inside the instance (the tool is in the HA image **[lab]**) and a listener on the discovery leg is the finding **[proposed]**. It is a read-only command run through the exec API, as `check_output` already runs `policy get` during `plan` **[docs]**.

**What is not proven** **[to confirm]**:
- that setting the listen scope the supported way (the UI or websocket, with its pending-then-promote step) closes the listener;
- whether discovery still works on a leg with no IPv4 address;
- that placing a `configuration.yaml` with `http:` before the first start (the placement #78 does) is the only declarative way to set the scope for a new HA instance. This is inferred from the migrate-once code and untested. The mock places one, as a seed that tink never compares or overwrites afterwards, and says in a comment that HA and the user own the file from then on.

**Today is not this path.** A read-only survey of the author's site on 2026-10-13 found HA behind Caddy with a source-address gate, no single sign-on in its path, and Caddy proxying to HA's LAN address **[lab]**. Mesh users arrive through a gateway that masquerades, which probably makes them look like local clients **[to confirm]**. The note records the target. How far a site is from it is what the join is for.

## Trying the pattern against the other roles

| Role | Offer | Grant | Where it fits | Where it is thin |
|---|---|---|---|---|
| ingress | port, protocol | which names are published, and for whom | the only offer implementation today **[code]**; the conflict handling is the model for exclusivity | `user.tink.ingress.domain` is a choice about publication and not a fact the app knows, so under the split it is the site's `publish`, as the mock has it **[proposed]**. Hand-written routes must coexist ([D8](#d8-tink-stays-out-of-what-it-does-not-manage-decided)) |
| single sign-on (Authelia) | the OIDC registration | which groups may sign in to which client | the authorization hop; the access rules and the client list are rendered from offers and grants | the client secret travels both ways (plaintext into the app, a salted digest into the engine); clients are static, so a change needs a restart **[docs: what-tink-is.md]**. The mock assumes Authelia merges a static file with a generated one **[to confirm]** |
| identity (LLDAP) | none | none | it is the source of subjects: the join checks that each named group exists | the pattern does not describe it; it supplies the join |
| mesh | a port reachable over the mesh | which mesh groups may reach it | the gateway advertises routes to the apps that offer a port **[docs: glossary]**; in the mock it reaches the ingress helper only, so no app stack mentions the mesh | group membership is a snapshot; the gateway masquerading erases who is asking for every later hop; its control plane is a need ([D6](#d6-two-directions-exposure-and-requirement-decided)) |
| east-west ACLs on a bridge | a port | `instance:<name>` as the subject | the one role where `instance:` is the natural subject; these are ACLs on the NIC, and the bridge-boundary ACL of [D9](#d9-the-ingress-helper-reaches-apps-through-the-host-behind-an-acl-on-each-app-bridge-decided) is the other kind. In the mock the message broker allows anonymous clients, so its only protection is an ACL admitting the named consumers | no ACL kind is built; ACL rules need addresses (groups and selectors are unsupported on bridges), and the mock gives HA a static one, for its own configuration to name; none on an unmanaged bridge **[docs]**. Password auth would need a secret in a file |
| backup | none | none | requirement ([D6](#d6-two-directions-exposure-and-requirement-decided)): a copy needs a target | nothing to authorize |
| secrets | none | none | requirement: a reference needs a secret | whether an instance needs a grant to use a secret is not asked yet **[to confirm]**; secrets in files are open |
| devices | a device on the host | which instance may hold it, once | the cardinality case ([D7](#d7-levels-exclusivity-coverage-and-freshness-decided-that-the-model-carries-them)); the mock also notes that two servers must never share one Matter fabric | tink has no inventory of host devices; offers come from instances and not from the host **[proposed]** |
| shares | a volume or a share | which instances mount it, read or write | the level case | a share on a system tink does not manage is out of scope ([D8](#d8-tink-stays-out-of-what-it-does-not-manage-decided)) **[to confirm]** |
| notifications | none | none | requirement: a failure needs somewhere to go | nothing built or chosen; [open decision 6](what-tink-is.md#open-decisions) |
| dns (proposed) | none; the app offers a port and the site picks the name | what is published, and where each name points | a `publish` becomes zone records; the dns helper lab proved the recipe **[lab]** | below |

**DNS in more detail.** A `publish` is the site's decision to give an offer a name, and DNS is one place the name is rendered. The dns helper lab (CoreDNS 1.14.7, pinned by digest, one instance, files placed before the first start) showed **[lab: dns-lab D1]**:
- the `hosts` plugin re-reads its file every 5 seconds; 8 of 8 changes made by a tink apply were visible within 5.0 seconds (median 1.8);
- `hosts` alone answers SERVFAIL for an unknown name; `fallthrough` plus a SOA-only `file` zone gives NXDOMAIN. tink would render that skeleton once and the `hosts` file whenever what is published changes;
- with the container stopped, names in the zone failed after 30.0 seconds on a Mac, and other names were unaffected.

It is thin in one respect: DNS is not an access control. Everyone who can query sees the names, so DNS has no grant, only the question of who sees which answer. A name has a LAN view and a mesh view, and one `publish` would have to render both (CoreDNS for the LAN, Headscale `extra_records` for the mesh) **[proposed]**. The zone name also appears in several files of the skeleton, and the LAN resolver's forward rule is prescribed by tink but applied by hand. The router forward, Windows, a client on another subnet, IPv6 and browsers with their own DNS-over-HTTPS were not tested.

## D8. tink stays out of what it does not manage (decided)

Cameras, the printer, TrueNAS and the hand-written Caddy routes are not tink's. They have no offers, tink does not read their topology, and the join does not model them. Reason: the author's rule that tink fills gaps and does not take over what it did not make ([glossary](glossary.md#the-principle)); a half-model of something tink cannot enforce would be a false assurance. The cost is stated in D4: a clean join speaks only of the paths tink can see. A hand-written route that proxies the same app is exactly the kind of path it cannot report. In the mock the hand-written routes live on a volume of their own that tink never writes to, imported by the proxy next to the routes tink generates, so the two coexist and stay separate.

## D9. The ingress helper reaches apps through the host, behind an ACL on each app bridge (decided)

**Decision (2026-10-13).** The ingress helper has **no leg on an app bridge**. It reaches an app through the host, which routes between managed Incus bridges without NAT, so the app sees the ingress helper's own address. An ACL on each app bridge, rendered by the join from the `publish` documents, allows only the ingress helper's address to the published port. The path in the worked example is therefore: client, ingress helper (its LAN leg, or the mesh gateway), host routing, the ACL at the app bridge's boundary, the app.

- **The problem.** The ingress helper sits on the platform bridge and each app on a bridge of its own. A leg on the app bridge would need that bridge to exist before the platform stack is applied, and it belongs to another stack, where `depends_on` does not cross.
- **Reason.** The other options each cost something this one does not: tink adding the leg when it sees a `publish` changes a platform-owned instance from outside; the app stack creating the bridge and the platform stack referencing it needs an apply order across stacks. The ACL sits on the bridge the app already has, and nothing in another stack is touched **[from the mock's gap list]**.
- **Alternatives weighed, not chosen:** a leg declared in the platform stack; tink adds the leg when it sees a `publish`; one ingress helper per bridge; apps take a second NIC on the platform bridge.
- **Evidence [lab]** (an ACL lab on two managed lab bridges, 2026-10-13): an allowed source reached the app in 0.1 s; another source on another bridge was rejected in about 1 s; the allowed source on another port was refused; traffic inside the bridge and outbound traffic were untouched; the host's own probes were filtered too; the real source address survived, with no NAT in the connection table; and an instance added to the bridge afterwards was covered with no step taken.
- **Corrected facts.** Routing between managed bridges keeps the client's source address, because Incus accepts traffic leaving through any managed bridge before its masquerade rule **[lab]**. It does not isolate: every managed bridge can reach every other by default, so the ACL is what makes "only the ingress helper may reach this" true. Bridge ACLs take plain CIDRs and have no name selectors **[docs]**, and they filter only at the bridge boundary, so east-west rules inside a bridge are ACLs on the NIC **[docs, lab]**.
- **What it carries.**
  - The ingress helper's address must be static, and the ACL names it as a CIDR. The app sees that address (HA's `trusted_proxies` is the ingress helper's own address on the platform bridge).
  - Names do not resolve across bridges, so the ingress helper reaches an app by address, which the ingress reconcile reads from the app.
  - The host's own probes (health checks, monitoring) hit the same boundary and need an allow rule. Which source address the host presents was not checked **[to confirm]**.
  - The lab set the egress default to allow, so an app's cloud access kept working. Rejecting egress by default was not tried.
  - A discovery leg on an unmanaged bridge such as `br0` takes no ACL, so a listener there is reported and not filtered ([D4](#d4-the-join-effective-access-over-hops-it-derives-from-topology-decided)).

## What exists today, and a first step

As of 2026-10-13 **[code]**: one implementation of an offer (`ingress.Registration`, built by `ingress.Discover` from `user.tink.ingress.*`, with the legacy names still read); no grants; nothing that joins them. Backup targets and secrets are requirement-shaped but have no common type with the above.

**A first step that changes no behaviour (proposed),** extending the list in [what-tink-is.md](what-tink-is.md#what-exists-in-the-code-today):
1. A shared `Offer`, built per role from the final key spelling. Ingress moves to it first, directly and with no transitional keys, with its existing tests as the guard.
2. The join with offers only, and its two cheapest findings: an offer with no grant, and a grant with no offer.
3. The first grant arrives with the first role that has one, which is single sign-on (Authelia's access rules and OIDC clients).
4. Topology and hops after that, beginning with the check that is worth most: the listener on a leg that must not listen. Rendering the ACLs of D9 comes later still, because it needs a kind for an ACL first ([open question 8](#open-questions)).

## Open questions

Not decided here.

1. **Key spellings.** `user.tink.offer.<name>.*` is a sketch. Also how an offer's attributes are spelled (protocol, where it listens, OIDC registration), the key that says why a NIC exists (`user.tink.leg.<nic>`, with `discovery` and `serves` as the two values in the mock), the role stamp (`user.tink.role`), and how a grant refers to an offer (`ha/web` is a sketch).
2. **Kind names.** `access`, `publish` and `mesh` are sketches, and so is every field name in the YAML above. The mock's `place:` and `seed:` on a file (put it before the first start; create it and never compare it again) are sketches too and are not part of this design.
3. **The removal model.** This is where report-never-remove is most dangerous. Removing a grant must remove the access it gave, but today a key removed from a stack is left behind (the dns lab saw a stale marker keep a warning alive **[lab]**) and an ACL rule removed from the YAML stays until removed by hand **[docs: network-resources-design.md]**. A rendered file such as Authelia's is rewritten whole, so removal there is natural; an Incus ACL or a policy delivered by exec is not. See [open decision 1](what-tink-is.md#open-decisions).
4. **Secrets in files.** An OIDC signing key or a bootstrap password file cannot be placed today, because `${secret:}` works only in `environment.*` ([open decision 8](what-tink-is.md#open-decisions)). Single sign-on offers and grants will need it, and so does the rest of the mock: the directory's password file, Authelia's OIDC keys, a camera's credentials inside Frigate's config, and a message broker's password file.
5. **Whether a generated site-wide view of grants should also be accepted as input.** tink prints the joined relation. Whether that printed form can also be read back as a declaration, so a site that prefers one file of grants (the other placement in D2) could keep one, is open.
6. **How `accept:` expires.** A deliberate bypass should not become permanent unseen. A date, a re-ask when the topology changes, or never are all possible.
7. **Which helper holds the credential for reconcile jobs.** Today only the daemon helper holds a certificate with the reach of root. The mock needs four renderings from the `publish` documents of every stack: the proxy's routes, the dns helper's hosts file, Authelia's access rules and OIDC clients, and the gateway's routes. Each needs the Incus API. Whether a role helper that reconciles holds its own certificate, narrowed to a project, or the daemon helper stays the one that acts, is [open decision 2](what-tink-is.md#open-decisions)(a).
8. **A resource kind for an ACL.** tink has none, and the `kind: incus` shim is blocked under `--remote`, so the ACL lab drove `incus` directly on the host. D9 cannot be built until a kind exists that can attach an ACL to a network (for the boundary) and to a NIC (for east-west). [network-resources-design.md](network-resources-design.md) proposes `kind: network-acl` and is not built.
9. **Stable addresses for instance subjects.** A grant that names `instance:<name>` becomes a CIDR in a NIC ACL, because bridge ACLs have no name selectors **[docs]**. In the mock HA has a static address and the other apps take DHCP, so those rules need static addresses, or an address set that tink keeps current (address sets are supported for nftables bridges **[docs]**; untried).

**Raised while drafting** (the working session did not settle them) **[to confirm]**:
- Where a grant sits when the app's stack is shared between sites (D2). The mock puts the grants in the site's own stack.
- Whether `user.tink.ingress.*` (the current names) keeps being read once ingress moves to offers, or only the older `user.ingress.*` does. D1 says there are no transitional keys; the code constraint covers only the older names.
- The name of the command that prints the joined relation.
