# Design: offers, grants and the join

**Status: a design note, 2026-10-13. Nothing in it is built.** It writes down a pattern that [what-tink-is.md](what-tink-is.md#offers-and-grants) names and that, until now, existed only in a working session: apps **offer**, the site **grants**, and a **join** works out who can actually reach what. It is for the author to review. Words are explained in [the glossary](glossary.md) (role, engine, helper, daemon helper, platform stack). Claims are marked **[docs]** (this repository), **[code]**, **[lab]** (seen on a real host), **[proposed]** (not built) or **[to confirm]**. Each decision has a status: **decided** (chosen over alternatives, for the recorded reason), **defaulted** (chosen without comparing; open to replacement) or **open**. Examples use generic names (`ha.example.com`, `br0`), not the author's site.

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

## D1. Offers are neutral facts the app states about itself (decided; spelling open)

An offer says what the app knows: it listens on 8123, speaks HTTP, wants OIDC with these redirect URIs. It never says who may use it. The keys name no engine, so the engine behind a role can be swapped: an app that offers "HTTP on 8123" is routable by Caddy today and by another proxy tomorrow without touching the app's stack.

```yaml
# SKETCH. The spelling is not decided.
kind: instance
name: ha
config:
  user.tink.offer.web.port: "8123"
  user.tink.offer.web.protocol: http
```

An app that wants single sign-on states its OIDC registration the same way (redirect URIs, whether it reads claims from the ID token or from `userinfo`, whether it needs a JWT access token), as [what-tink-is.md](what-tink-is.md#what-this-means-for-tink) describes.

- **Reason.** The app's author knows the app and not the site. Letting an offer say who may use it would let an app widen its own audience, and would make a shared stack carry one site's people ([the three reasons for the split](what-tink-is.md#offers-and-grants)).
- **Spelling: open.** `user.tink.offer.<name>.*` is a sketch, where `<name>` identifies one offer on the instance (an app can offer more than one). It stays under `user.tink.*`, which `cmd/tink/keynamespace_test.go` enforces **[code]**.
- **Ingress adopts the final form directly (decided).** No transitional keys: no intermediate spelling that exists only to be replaced. The old `user.ingress.*` names are still read, because instances in other repositories, on hosts in use, register with them, and a reconciler that stopped seeing them would delete their routes **[code: `internal/ingress/keys.go`]**. A notice already asks for the rename **[code]**.
- **Alternative.** Keep one key family per role (`user.tink.ingress.*`, `user.tink.sso.*`). Simple, and it is what ingress has today. It lets each role's keys drift into that engine's vocabulary, which is the coupling D1 exists to avoid.

## D2. Grants are the site's authority, placed next to the thing they govern (decided)

A grant lists who may use an offer. It is written by whoever runs the site. The decision is to place each grant next to the thing it governs, as a per-offer `access` block, and not in one site-wide list.

```yaml
# SKETCH. The kind names (publish, access) and every field name are not decided.
kind: publish
offer: ha/web                      # the app's fact: port 8123, http
name: ha.example.com               # the site's choice
access:                            # the grant, beside what it governs
  - {subject: family, level: user}
  - {subject: admins, level: admin}
---
kind: access
offer: ha/web                      # the offer itself is reachable only through the proxy
allow:
  - {subject: instance:caddy}
```

- **Reason.** Policy beside its subject leaves fewer omissions. An offer with no `access` block is visible where it is declared, and an edit to one cannot be made without seeing the other. The review that a single list would give can be generated: tink prints the joined relation ([D4](#d4-the-join-effective-access-over-hops-it-derives-from-topology-decided)), so the whole site is readable in one place without being written in one place.
- **The other placement is defensible too, and the author said so.** One site-wide file of grants means one thing to review, one diff in a pull request, and a natural home for rules that span several offers (everything the family may use). Its cost is that an offer and its policy are far apart, so the omission is found by `plan` and not by looking. The trade-off is real, which is why the printed view and the open question on accepting it as input ([open question 5](#open-questions)) exist.
- **Where the block sits when the app's stack is shared is not settled.** The block is the site's, so it cannot ride inside a stack shared between sites. Whether it is a field of `publish` or its own document, and which file holds it, are open **[to confirm]**.
- **This differs from the sketch in what-tink-is.md**, where one `kind: <role>` document holds an `allow:` list for the whole site. That sketch should be updated when this is built.

The kind names `access`, `publish` and `mesh` are sketches, not decisions. `publish` is what the site calls a thing and where it is reachable (an ingress route, a zone record); `mesh` is a gateway per bridge.

## D3. Exposure and authorization are different layers (decided)

- **Exposure:** can a packet get there? Bridge membership, a network ACL, a mesh route, a proxy route.
- **Authorization:** is this subject allowed? A single sign-on group.

"Only family may reach HA" is an authorization statement. It is enforced at the single sign-on hop, and no exposure layer can state it, because exposure layers do not see people. A source-address rule on a proxy is exposure: it says which networks, and identifies nobody.

- **Reason.** Conflating them is how a firewall rule gets read as a statement about people, and how a site believes it has restricted something to a group when it has restricted it to a subnet.
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

**Findings** (all **[proposed]**): an offer with no grant (a note: unreachable, the safe default); a grant with no offer (a warning); a grant that names a group the directory does not have (the check [identity-projector-design.md](identity-projector-design.md) already plans for `plan`); a grant for a subject kind that no hop on the path can check (nothing enforces it); a bypass; a hop whose enforcer cannot be installed here ([D7](#d7-levels-exclusivity-coverage-and-freshness-decided-that-the-model-carries-them)).

**What the join can promise.** Only about the paths tink can see. A hand-written route in a proxy tink does not manage is invisible to it ([D8](#d8-tink-stays-out-of-what-it-does-not-manage-decided)), so a clean join means "no unprotected path among the known ones", and its output should say so.

## D5. Subjects are typed (decided)

| Subject | Means | Checked by |
|---|---|---|
| `family` (a bare name) | a group in the directory | single sign-on, the mesh policy |
| `instance:caddy` | an instance | a network ACL on its NIC, a listener's source |
| `anyone` | no identity required | any hop; the grant is only about exposure |
| `cidr:192.0.2.0/24` | a network range | a proxy matcher, an ACL, the mesh |

- **Grants for people name groups, never a single person, and there is no subject registry.** Reason: the directory owns people, groups and credentials, and tink only checks that a named group exists ([what the identity work decided](what-tink-is.md#what-this-means-for-tink)); a registry of subjects would be state tink was built not to keep.
- **`allow: cidr` is an acceptable alternative to single sign-on** for a path that is deliberately trusted by network (a printer proxy with no login, say). It is honest about what it does: nobody is identified, and the join prints it that way.

## D6. Two directions: exposure and requirement (decided)

- **Exposure (offer to grant):** what I provide, and who may use it. Most of this note.
- **Requirement (need to provide):** what I need, and what provides it. There is no subject here; the question is whether something answers.

Requirement is already in the code and is already join-shaped:
- A volume's `backup.copies` must name a `kind: backup-target` in the stack, checked at load **[code: `internal/resolve/backuptarget.go`]** ([volume-backup.md](volume-backup.md)).
- A `${secret:NAME}` in an instance's `environment.*` names a secret that must exist in the store **[code: `internal/resolve/secretrefs.go`]** ([secrets.md](secrets.md)).
- The mesh gateway's control plane is a provided need: the gateway needs a server address and a key, and tink's default provider is a Headscale helper, though Tailscale's hosted service or someone else's Headscale also provides it **[docs: what-tink-is.md]**.

Reason for putting both under one join: a need with no provider and a grant with no offer are the same mistake seen from two sides, and `plan` should report them the same way.

## D7. Levels, exclusivity, coverage and freshness (decided that the model carries them)

| Property | What it adds | Example |
|---|---|---|
| **Level or attribute** | a grant says how much, not only whether: read or admin | `family` may use HA; `admins` may administer it. Level names are open |
| **Exclusive, or a cardinality** | one holder per device, or per static address | a USB radio dongle; a static address outside the router's pool, as the dns lab's helper has on `br0` **[lab]** |
| **Enforcement coverage** | whether the hop's enforcer can be installed here at all | an unmanaged bridge such as `br0` cannot take an Incus ACL **[lab]**, so the join says "not enforceable here" and does not count the hop as protection |
| **Freshness class** | how fast a change reaches the enforcer | below |

- **Exclusivity has a precedent in the code.** Ingress already refuses a contested domain: "claimed by multiple instances, skipping all of them", so no claimant wins by accident **[code: `internal/ingress/discover.go`]**. An exclusive grant held twice is reported the same way.
- **Coverage includes the east-west case.** Filtering between two instances on one bridge needs the ACL on the NIC, not on the bridge, and ACL groups and `@internal` selectors are not supported on bridges **[docs: network-resources-design.md]**.
- **Freshness classes, observed:** Authelia reads LDAP groups live (a change reached an existing session within the 1-minute refresh), but its own configuration, the OIDC clients and the access rules, is static until a restart **[lab]**. Headscale policy holds groups as a static snapshot, so a directory change reaches the mesh only when the snapshot is re-projected and set **[docs: identity-projector-design.md]**. That needs a periodic reconcile in the daemon helper, with naive polling first **[proposed]**, using the `check_output` guard so the policy converges to what is declared ([exec-output-check-design.md](exec-output-check-design.md)). The join can then say that a person removed from a group loses single sign-on access within about a minute and the mesh hop at the next reconcile **[proposed]**.

## Worked example: Home Assistant's intended path

The intended path, as the author stated it on 2026-10-13: **LAN or mesh, then Caddy, then Authelia, then Home Assistant**, with **no public path**. The mesh reaches Caddy and not HA directly. HA's second interface, on the LAN bridge, exists only so HA can hear multicast, SSDP and zeroconf discovery (which the author decided matters for HA in general); it is not a way in for users and must not listen. The design follows the intended path. A bypass is the exception, and the join's job is to report it.

```
client on the LAN  ---> ingress helper (Caddy) ---> Authelia ---> ha
client on the mesh ---> mesh helper (gateway) ---> ingress helper ---> Authelia ---> ha
                                 ha's second leg, on br0:  discovery only, must not listen
```

**The grants** are the two in the [D2](#d2-grants-are-the-sites-authority-placed-next-to-the-thing-they-govern-decided) sketch: people (`family`, `admins`) are granted the published name, which is the thing they touch, and HA's own offer is granted to `instance:caddy` only. The hops are derived:

| Hop | Layer | Enforcer | Can check | Note |
|---|---|---|---|---|
| client to ingress, from the LAN | exposure | the LAN's reachability; a Caddy source matcher | `cidr` | blind to people |
| client to ingress, from the mesh | exposure | the mesh policy, through the gateway | mesh groups (a snapshot) | after the gateway masquerades, later hops see the gateway and not the person **[to confirm]** |
| ingress route for `ha.example.com` | exposure | Caddy | `cidr`; hands the person check to the next hop | |
| single sign-on | **authorization** | an Authelia access rule | `group`, `anyone` | the only hop that sees a person |
| ingress to ha | exposure | the bridge the two share | `instance` | `not enforceable here` on an unmanaged `br0`; on a managed bridge, an ACL on the NIC **[docs]** |

The join prints one line per path:

```
family  -> ha.example.com  user   lan:   client > caddy > authelia > ha                   ok
family  -> ha.example.com  user   mesh:  client > gateway > caddy > authelia > ha         ok
admins  -> ha.example.com  admin  lan:   client > caddy > authelia > ha                   ok
(any)   -> ha:8123         -      br0:   client > ha                                       BYPASS: ha listens on its discovery leg
```

**Why Authelia is only a choke point if the last line is absent.** Authelia protects HA only if every path passes it. Caddy to HA is one path. HA's own second leg is another, if HA listens there: any host on the LAN could reach port 8123 and skip both Caddy and Authelia.

**Why the join has to look.** In HA 2026.9.4 the HTTP settings (`server_host` and the rest) are stored config: a YAML `http:` block is migrated once at first boot and ignored afterwards (`homeassistant/components/http/config.py`, and the repair issue `yaml_still_present_after_migration`) **[docs: read in upstream source]**. Adding `server_host` to the YAML after first boot changed nothing, and HA listened on all interfaces **[lab]**. So HA's configuration file cannot say where it listens, and the join checks the fact: `netstat -ltn` inside the instance (the tool is in the HA image **[lab]**) and a listener on the discovery leg is the finding **[proposed]**. It is a read-only command run through the exec API, as `check_output` already runs `policy get` during `plan` **[docs]**.

**What is not proven** **[to confirm]**:
- that setting the listen scope the supported way (the UI or websocket, with its pending-then-promote step) closes the listener;
- whether discovery still works on a leg with no IPv4 address;
- that placing a `configuration.yaml` with `http:` before the first start (the placement #78 does) is the only declarative way to set the scope for a new HA instance. This is inferred from the migrate-once code and untested.

**Today is not this path.** A read-only survey of the author's site on 2026-10-13 found HA behind Caddy with a source-address gate, no single sign-on in its path, and Caddy proxying to HA's LAN address **[lab]**. Mesh users arrive through a gateway that masquerades, which probably makes them look like local clients **[to confirm]**. The note records the target. How far a site is from it is what the join is for.

## Trying the pattern against the other roles

| Role | Offer | Grant | Where it fits | Where it is thin |
|---|---|---|---|---|
| ingress | port, protocol | which names are published, and for whom | the only offer implementation today **[code]**; the conflict handling is the model for exclusivity | `user.tink.ingress.domain` is a choice about publication and not a fact the app knows, so under the split it reads as the site's `publish` **[to confirm]** |
| single sign-on (Authelia) | the OIDC registration | which groups may sign in to which client | the authorization hop; the client list is rendered from offers and grants | the client secret travels both ways (plaintext into the app, a salted digest into the engine); clients are static, so a change needs a restart **[docs: what-tink-is.md]** |
| identity (LLDAP) | none | none | it is the source of subjects: the join checks that each named group exists | the pattern does not describe it; it supplies the join |
| mesh | a port reachable over the mesh | which mesh groups may reach it | the gateway advertises routes to the apps that offer a port **[docs: glossary]** | group membership is a snapshot; the gateway masquerading erases who is asking for every later hop |
| east-west ACLs on a bridge | a port | `instance:<name>` as the subject | the one role where `instance:` is the natural subject | no ACL kind is built; address-based rules go stale with DHCP; none on an unmanaged bridge **[docs]** |
| backup | none | none | requirement ([D6](#d6-two-directions-exposure-and-requirement-decided)): a copy needs a target | nothing to authorize |
| secrets | none | none | requirement: a reference needs a secret | whether an instance needs a grant to use a secret is not asked yet **[to confirm]**; secrets in files are open |
| devices | a device on the host | which instance may hold it, once | the cardinality case ([D7](#d7-levels-exclusivity-coverage-and-freshness-decided-that-the-model-carries-them)) | tink has no inventory of host devices; offers come from instances and not from the host **[proposed]** |
| shares | a volume or a share | which instances mount it, read or write | the level case | a share on a system tink does not manage is out of scope ([D8](#d8-tink-stays-out-of-what-it-does-not-manage-decided)) **[to confirm]** |
| notifications | none | none | requirement: a failure needs somewhere to go | nothing built or chosen; [open decision 6](what-tink-is.md#open-decisions) |
| dns (proposed) | none; the app offers a port and the site picks the name | what is published, and where each name points | a `publish` becomes zone records; the dns helper lab proved the recipe **[lab]** | below |

**DNS in more detail.** A `publish` is the site's decision to give an offer a name, and DNS is one place the name is rendered. The dns helper lab (CoreDNS 1.14.7, pinned by digest, one instance, files placed before the first start) showed **[lab: dns-lab D1]**:
- the `hosts` plugin re-reads its file every 5 seconds; 8 of 8 changes made by a tink apply were visible within 5.0 seconds (median 1.8);
- `hosts` alone answers SERVFAIL for an unknown name; `fallthrough` plus a SOA-only `file` zone gives NXDOMAIN. tink would render that skeleton once and the `hosts` file whenever what is published changes;
- with the container stopped, names in the zone failed after 30.0 seconds on a Mac, and other names were unaffected.

It is thin in one respect: DNS is not an access control. Everyone who can query sees the names, so DNS has no grant, only the question of who sees which answer. A name has a LAN view and a mesh view, and one `publish` would have to render both (CoreDNS for the LAN, Headscale `extra_records` for the mesh) **[proposed]**. The router forward, Windows, a client on another subnet, IPv6 and browsers with their own DNS-over-HTTPS were not tested.

## D8. tink stays out of what it does not manage (decided)

Cameras, the printer, TrueNAS and the hand-written Caddy routes are not tink's. They have no offers, tink does not read their topology, and the join does not model them. Reason: the author's rule that tink fills gaps and does not take over what it did not make ([glossary](glossary.md#the-principle)); a half-model of something tink cannot enforce would be a false assurance. The cost is stated in D4: a clean join speaks only of the paths tink can see. A hand-written route that proxies the same app is exactly the kind of path it cannot report.

## What exists today, and a first step

As of 2026-10-13 **[code]**: one implementation of an offer (`ingress.Registration`, built by `ingress.Discover` from `user.tink.ingress.*`, with the legacy names still read); no grants; nothing that joins them. Backup targets and secrets are requirement-shaped but have no common type with the above.

**A first step that changes no behaviour (proposed),** extending the list in [what-tink-is.md](what-tink-is.md#what-exists-in-the-code-today):
1. A shared `Offer`, built per role from the final key spelling. Ingress moves to it first, directly and with no transitional keys, with its existing tests as the guard.
2. The join with offers only, and its two cheapest findings: an offer with no grant, and a grant with no offer.
3. The first grant arrives with the first role that has one, which is single sign-on (Authelia's access rules and OIDC clients).
4. Topology and hops after that, beginning with the check that is worth most: the listener on a leg that must not listen.

## Open questions

Not decided here.

1. **Key spellings.** `user.tink.offer.<name>.*` is a sketch. Also how an offer's attributes are spelled (protocol, OIDC registration) and how a published name refers to an offer (`ha/web` is a sketch).
2. **Kind names.** `access`, `publish` and `mesh` are sketches, and so are every field name in the YAML above.
3. **The removal model.** This is where report-never-remove is most dangerous. Removing a grant must remove the access it gave, but today a key removed from a stack is left behind (the dns lab saw a stale marker keep a warning alive **[lab]**) and an ACL rule removed from the YAML stays until removed by hand **[docs: network-resources-design.md]**. A rendered file such as Authelia's is rewritten whole, so removal there is natural; an Incus ACL or a policy delivered by exec is not. See [open decision 1](what-tink-is.md#open-decisions).
4. **Secrets in files.** An OIDC signing key or a bootstrap password file cannot be placed today, because `${secret:}` works only in `environment.*` ([open decision 8](what-tink-is.md#open-decisions)). Single sign-on offers and grants will need it.
5. **Whether a generated site-wide view of grants should also be accepted as input.** tink prints the joined relation. Whether that printed form can also be read back as a declaration, so a site that prefers one file of grants (the other placement in D2) could keep one, is open.
6. **How `accept:` expires.** A deliberate bypass should not become permanent unseen. A date, a re-ask when the topology changes, or never are all possible.
7. **Which helper holds the credential for reconcile jobs.** Today only the daemon helper holds a certificate with the reach of root. Whether a role helper that reconciles (the mesh policy, a dns zone) holds its own, narrowed to a project, or the daemon helper stays the one that acts, is [open decision 2](what-tink-is.md#open-decisions)(a).

**Raised while drafting** (the working session did not settle them) **[to confirm]**:
- Where the `access` block sits when the app's stack is shared between sites (D2).
- Whether `user.tink.ingress.*` (the current names) keeps being read once ingress moves to offers, or only the older `user.ingress.*` does. D1 says there are no transitional keys; the code constraint covers only the older names.
- Whether `user.tink.ingress.domain` becomes the site's `publish` (the ingress row above).
- The name of the command that prints the joined relation.
