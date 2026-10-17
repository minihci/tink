# The platform stack as a shipped recipe: site facts in, stack out

**Status: a design note for discussion, 2026-10-17. Nothing in it is built.** Claims are marked **[docs]** (this repository), **[lab]** (seen on a real host), **[code]** (read in the code, not run) or **[proposed]**. The evidence is the author's real platform stack for the home server (`tron/platform/platform.yaml`, 517 lines, read in full on 2026-10-17; its own STATUS.md says a second `plan` of it was quiet on 2026-10-13 **[lab]**; it was not re-planned for this note), and a fresh VPS (Incus 7.5.1, public IP) used for the live validation of the helper work the same day.

## The question

[what-tink-is.md](what-tink-is.md#the-platform-stack-proposed) decided that the platform stack starts as "a template you copy and own", with two levers against the copy falling behind (a thin template with the wiring in tink, and visible drift). The author now leans toward **shipping it**, for a reason that decision did not weigh enough: **the end user should not have to maintain the platform stack.** The difficulty is that a home server behind NAT or on a VLAN, and a VPS with a public IP, do not look alike, and it is not practical to design for every kind of site.

## The proposal in one paragraph

Split the work by who is able to do it. **tink owns the containers and the wiring between them** (the *recipe*). **The user owns facts about the site** (a short file: a zone, an address range, where backups go). **Policy stays where it already is**: apps state offers, the site publishes them to groups, people live in the identity helper. The recipe is a pure function from site facts to resources, written in Go (typed conditionals, golden-tested, like `tink join` already is **[docs]**), and `tink plan` compares its output with the server. Because no copy exists, nothing falls behind: a new tink has a new recipe, and `plan` says what would change. It is declared state, so it **reports and never heals itself**, which is the rule the helper already follows (derived state heals, declared state reports **[docs]**).

## What a real platform stack tells us

The tron stack was written by hand, step by step (P1 to P7 in its own log), and is the best evidence of what a recipe would have to produce. Read against the proposal:

| Part of the file | Owner | Evidence |
|---|---|---|
| The set of helpers, their images (by digest), limits, `user.tink.role` stamps, restart policy | **recipe** | the same shape for each of the five helpers |
| East-west ACLs (`identity-in`, `sso-in`, `tink0-in`): who may reach whom | **recipe**, derived from the role graph | three ACLs, each a function of which role calls which; nobody would edit them per site |
| Backup policy per volume (daily snapshot, a copy to each target, weekly restore check; `none:` with a reason for the regenerable mesh state) | **recipe defaults** over **site targets** | the same four lines, repeated for three volumes |
| Engine config skeletons: the Corefile and zone file, the Caddyfile (`no_portal_cookie`, `sso` forward-auth), Authelia's configuration | **recipe**, but see the first precondition below | hand-written inside `kind: file` content |
| The zone `home.xlii.co` | **site fact** | 27 occurrences, plus the LDAP base DN, the cookie domain and the wildcard certificate |
| The platform bridge, its subnet and the helpers' static addresses | **recipe**, from one site fact (a range) | 19 occurrences of `10.10.50.`; the offsets are a convention |
| The LAN leg: a parent NIC, a CIDR, two free addresses outside the router's DHCP pool, fixed MACs | **site facts** | `br0` and `10.0.142.` (14 occurrences); the author picked the addresses by ping and ARP probe |
| How certificates are obtained: DNS-01 through Cloudflare, with a token, an image that has the module, and public resolvers because the dns helper is authoritative for the zone | **site fact**, and it changes the engine image | one secret supplied by the user |
| Backup targets: a same-host pool (declared honestly as `same-host`) and an offsite Incus remote | **site facts** | two `kind: backup-target` documents |
| Mesh: join an **existing** control plane at a URL, advertise one route | **site fact** (the URL) | the control plane is not in the stack |
| The published apps' routes, access rules, dns records, mesh records, ACL rules | **generated** by `tink join` | the canary's two names are still written by hand in four places; only the dns records are applied by tink today **[docs]** |
| Secrets | **generated or supplied** | nine names; seven can be minted (JWT secrets, keys, session and storage secrets), two cannot (the Cloudflare token, the mesh auth key) |
| Seeding the directory's bind account, approving the mesh route on the control plane, the router's resolver rule, creating people | **outside the stack today** | `ldap-seed.sh`, a `headscale` command, manual steps |

The count that matters: of 415 non-comment lines, the site-specific values are a handful of facts repeated dozens of times. That is what makes "facts in, stack out" plausible. A flat facts file for tron would be about fifteen lines (field names are illustrative, not decided):

```yaml
kind: site
name: tron
zone: home.xlii.co
timezone: America/Denver
roles: [identity, sso, dns, ingress, mesh, daemon]
platform: {bridge: tink0, subnet: 10.10.50.0/24}
lan: {parent: br0, cidr: 10.0.142.0/24, addresses: {dns: 10.0.142.230, ingress: 10.0.142.231}}
certificates: {challenge: dns-cloudflare, token: ${secret:platform-cloudflare-dns}}
mesh: {control_plane: https://hs.xlii.co}
backup:
  targets:
    - {name: nas, location: same-host, pool: nas2510}
    - {name: offsite, location: offsite, remote: vps}
```

## The site facts: a revised list

The first guess named six facts (how clients reach ingress, how certificates are obtained, names, the mesh role, where the Incus API listens, the pool driver). The real stack confirms those and adds what was missed:

1. **An addressing plan.** Static addresses outside the router's DHCP pool, and MACs. The recipe can derive the platform bridge's addresses; the LAN's it cannot, and a probe (the author's ping and ARP check) is a candidate for a tink check.
2. **Backup targets**, and the honesty that goes with them (`same-host` versus `other-host`, so the 3-2-1 check judges the truth).
3. **Which secrets are minted and which are supplied.** A user who must hand-mint seven secrets is maintaining the stack. First apply needs generation (`tink secret --generate` exists **[docs]**) and one clear list of what only a person can give.
4. **A site is more than one server.** The home site's control plane, its offsite backup target and, in the watching design, its alert endpoint all live on the VPS. So the recipe is **per server**, selecting the roles that server hosts, and servers refer to each other by URL or remote name, never by cross-stack reference (which tink does not have **[docs]**). The VPS is not "the VPS template"; it is `{mesh control plane, offsite target, alerts}` plus whatever public ingress the site wants. That also removes most of the homelab-versus-VPS worry: one recipe, different role selections, different facts.
5. **Reach is a set of legs**, not a mode. tron serves a LAN leg and a mesh leg and has no public one. A public host would have a `public` leg, which `tink join` does not model yet (`serves`, `mesh`, `discovery` **[docs]**).
6. **Where the Incus API may listen**, because it is a security fact that depends on the host: this VPS has a public IP and no host firewall, so a wildcard listener would expose the API; the live run used the bridge's own address **[lab]**.

## Escape hatches, cheapest first

1. **The facts file.**
2. **Per-role overrides.** A resource's own fields win, as `project:` already does **[docs]**.
3. **Bring your own helper.** `tink join` finds helpers by role stamp, never by name **[docs]**, and `kind: helper` adopts one that exists and changes nothing. A container the user built that carries `user.tink.role: ingress` satisfies the role. This is what makes enumerating site types unnecessary: the contract is the **role** (its stamps and what it must answer), and the recipe supplies a default for each.
4. **Eject.** Print the full stack; the user owns it from then on.

## What has to be true first

These are gaps in tink today, in the order they block a shipped recipe:

1. **Generated content must live apart from the recipe's skeleton.** The dns helper already does this (`/hosts` is generated, the zone file is declared, and the applier refuses a clash **[docs]**). The Caddyfile and Authelia's rules do not: published routes and access rules are hand-written inside the same `kind: file` the recipe would own, so `plan` and `join` would fight over them. Each engine needs an include for generated fragments, and `join` needs to apply all five renderings, not only dns.
2. **First apply.** Generated secrets, and the steps that are not resources today (seeding the directory, approving the mesh route, the resolver rule). `kind: exec` with a `check` covers some; a step on another server (the control plane) is a report ("still to do"), not an action.
3. **Ownership stamps and scoped removal** ([ownership-stamps-design](https://github.com/minihci/tink/blob/docs/ownership-stamps-design/docs/ownership-stamps-design.md)), or an upgrade can add and update a helper but never drop or rename one.
4. **A version policy for the daemon helper.** The tron stack adopts a helper still on v0.1.4, which predates `tink join`; `plan` never upgrades it **[docs]**. A recipe rendered by a newer tink and a helper running an older one disagree. The recipe has to say which tink it needs, and `plan` has to say when the helper is behind.
5. **Watching.** "The user does not maintain it" holds only if tink tells them when it breaks. Nothing watches the helpers, certificates or backups today **[docs]**; the author's watching design (a monitor at home, a heartbeat and alert delivery on the VPS) is the first candidate role.
6. **Pins.** Ship image digests with the recipe, and have `plan` report newer upstream digests through `on_image_change: report`, so a security fix does not have to wait for the tink release that carries a new recipe. A separate pins file is a later option.

## Traps

- **The templating trap.** Values plus templates grow into a language. Keep the facts flat (strings, numbers, short lists), no expressions, and add a field only when two real sites need it.
- **Policy in the facts file.** Access rules and OIDC clients are rendered from offers and publishes, not typed into facts.
- **A recipe that is a text template.** `deploy` renders `${VAR}` into text today **[docs]**. Typed Go keeps the conditionals testable.

## Proving it

The author has two real sites, which is the test. **(1)** `recipe(tron's facts)` must `plan` as *no changes* against the live tron stack (the criterion of the opinion-as-code note's stage 3, on branch `platform-design`). **(2)** `recipe(the VPS's facts)` must produce something sound on a clean VPS. If one function does both, the axes are right; if one site needs another knob, that knob is a real axis. Both become golden fixtures, as reference sites, not as templates anyone maintains.

Stages, each checkable on its own:

0. This note, argued with.
1. By hand: account for every literal in `platform.yaml` as recipe, fact, generated or outside the stack. No code.
2. The pieces from "What has to be true first" that are independent of the recipe (1, 3 and 4), each as its own change.
3. `tink platform plan FACTS` (read-only; name not decided) for tron, until it says no changes.
4. The same for the VPS, then `apply` there.
5. Only then: `tink platform eject`, pins, and whether the daemon helper reports drift between recipe and server.

## Open questions

- Is the facts file a `kind: site` document in a stack, or a separate file or flags? A document keeps one mechanism; a separate file keeps the facts out of the stack the recipe produces.
- How much of the LAN addressing can tink discover, and how much must the user type?
- Does the recipe live in `internal/` as Go (preferred here) or as embedded YAML with substitution (what `deploy` does)?
- A `public` leg: what does `join` render for it, given there is no dns helper answering for public names?
- Which roles are optional per server, and who decides that a server with no dns helper is still a complete site?
- Is declared-state drift between recipe and server reported by `plan` only, or also by the daemon helper's status?
