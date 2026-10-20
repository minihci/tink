# What tink is: the loop, the roles and the engines

**Status: describes what is built, last updated 2026-10-20.** The proposed parts of the earlier draft (role helpers, the platform recipe, identity and single sign-on, the generic offer and grant types) live in issues: see [where the rest went](#where-the-rest-went). The one status table is the README's capability table; this page does not keep a second one. Words are explained in [the glossary](glossary.md). Claims are marked **[docs]** (this repository), **[lab]** (seen on a real host) or **[to confirm]**.

## The idea

tink is declarative. You describe a site in YAML, `plan` shows what would change, and `apply` makes it so. It orchestrates technologies chosen for their strengths and fills the gaps between them, so a home lab or a small self-hosted site gets pieces of infrastructure (backups that are verified, secrets, names for its apps) at close to no cost. It does not replace the technologies it uses. The one hard requirement is that Incus exists and tink has access to it.

## The loop

1. **Declare** in a stack: documents of different kinds, applied together.
2. **Plan**: compare the declaration with what Incus reports. Say what would change, what is blocked and why, and what looks wrong.
3. **Apply**: make the change. Anything that could interrupt running work is blocked unless the declaration allows it.
4. **Keep and verify**: the daemon helper runs the work that must keep happening (scheduled backup copies) and says when it cannot. See [helpers](#helpers).

What tink must remember it writes on the objects themselves, under `user.tink.*`, so the state stays with the thing it describes **[docs]**.

**Where the loop is incomplete today.** It creates and updates, but it never deletes: a thing the stack no longer declares is reported as an extra and left alone. The one exception is the prefix tink owns, `user.tink.publish.` ([join.md](join.md#the-owned-prefix-and-the-one-place-tink-removes-a-key)). That was a safe first rule and it hardened into a stronger one than it should be; an ownership stamp on everything tink creates would make an explicit, scoped removal safe. That is a proposal: #133, with the first step in #127.

## One pattern: a capability

The pieces that exist share a shape: a declaration, something that acts on it, and checks that say when it is wrong.

| Capability | Declaration | Acts | Checks |
|---|---|---|---|
| backup | `backup:` on a volume | the daemon helper's scheduler | no daemon helper, missing remote, a 3-2-1 note |
| secrets | `${secret:NAME}` in an instance's `environment.*` | resolved at apply | the redactor, the load-time refusal anywhere else |
| network | `kind: network`, `kind: network-acl` | apply | a disruption guard, an unnamed-NIC warning, an attached ACL that exists nowhere |
| names for apps | an offer on the instance, `kind: publish` in the site's stack | `tink join` prints; `tink join apply dns` writes the dns records | the join's findings ([join.md](join.md#findings)) |

Whether each is built, and how far it has been run live, is in the README's capability table. Remote access and identity with single sign-on follow the same shape but are not built (#131).

## Roles and engines

A **role** is a job tink needs done that more than one technology can do. An **engine** is the technology that does it today. The platform, Incus, is not a role: it is required.

The rule that keeps this contained is that tink's code talks to a role, and an engine sits behind it. Today the code is written for one engine per role, so the role boundary is where a second engine would be added. The engines tink's code is written for:

| Role | Engine | What tink does with it |
|---|---|---|
| platform | Incus | everything; the one API for containers and VMs |
| backup transport | an Incus remote | copies use Incus's own copy ([volume-backup.md](volume-backup.md)) |
| secrets | age | the store behind `${secret:NAME}` ([secrets.md](secrets.md)) |
| dns | CoreDNS | `join apply dns` writes the `hosts` file the helper answers from |
| reverse proxy | Caddy | `join` prints the routes; they are applied by hand |
| single sign-on | Authelia | `join` prints the access rules; applied by hand |
| mesh control plane | Headscale | `join` prints the records; applied by hand |

The reasons for those engines, the alternatives compared, and the roles that are not built (identity store, mesh gateway) are in #131.

## Helpers

A **helper** is a container Incus keeps running for tink. It is named by its role: an *ingress helper*, a *dns helper*, an *identity helper*. The bare "the helper" is ambiguous and is not used; what the rest of the documentation calls "the helper" is the **daemon helper**.

**Where the idea came from.** The first job was backups, which need something long-running to schedule them. Writing init scripts for each init system was not something tink wanted to take on. Incus is already started at boot, so a container that runs `tink daemon run` is a supervised long-running process with nothing for tink to install. That is still the whole mechanism.

**The daemon helper** is the one tink installs and runs: the backup scheduler and the job executor, under a certificate with the reach of root ([helper.md](helper.md)). `kind: helper` in a stack adopts one that is there and installs one when there is none. It is a generic periodic runner, and the jobs belong to the roles that need them.

**The others** (dns, ingress, sign-on, the mesh) are ordinary `kind: instance` documents that the site writes. tink does not manage them beyond reading what they state about themselves, as stamps: `user.tink.role`, `user.tink.leg.<nic>` and `user.tink.dns.zone`, which `tink join` reads to know where a rendering goes ([join.md](join.md)). A platform stack that declares them for a whole site is written by hand today. The proposal to ship it as a recipe is #131.

## Offers and grants

An **offer** says what an app can provide. It is written by whoever writes the app's stack and travels with the app. A **grant** says who may use it. It is written by whoever runs the site and lives in one place that is reviewed as a whole.

Why split them, in order of weight at this scale:
1. **A stack stays shareable.** An app's stack says "I serve a web UI on 8123". Your people and rules stay in your own file.
2. **`plan` can join them.** An offer with no grant is unreachable (the safe default); a grant with no offer is a mistake worth a warning.
3. **The audience is not widened by the app.**

What is built is the first step, for web apps: `user.tink.offer.<offer>.port|proto|listen` on an instance is an offer, `kind: publish` is the grant (a name and the groups that may open it), and `tink join` joins them to the helpers' own facts and prints the dns records, mesh records, proxy routes, sign-on rules and ACL rules. `tink join apply dns` writes the dns records. `plan` stamps each publish onto its instance as `user.tink.publish.<name>`, and `tink join --from-server` reads the declarations back. See [join.md](join.md).

Not built, and proposed in #130: the join as a stage of `plan` instead of a command, offers and grants beyond web (an `access` document, typed subjects), the per-role engine interface, and the rendering of the other four outputs. The self-registration that was the first, ingress-only offer was removed ([removals/ingress-self-registration.md](removals/ingress-self-registration.md)).

## Where the rest went

The earlier draft of this page held decisions and lab findings for features that are not built. They were moved, not dropped:

| What | Where |
|---|---|
| Roles and engines compared; role helpers; the platform stack as a shipped recipe; identity and single sign-on (the LLDAP and Authelia lab); open decisions on helpers | #131 |
| The generic `Offer`, `Grant` and `Join` types and the per-role engine interface | #130 |
| Removal and the ownership stamp | #133 |
| Secrets in files | #137 |
| Notification: how a failure reaches a person | #135 |
| Remote access and its defaults | #136 |
| Delivering a policy to the mesh control plane with a `check_output` guard | built: [exec-output-check-design.md](exec-output-check-design.md) |
| How narrow the daemon helper's certificate is | [roadmap.md](roadmap.md), Phase 5; a restricted certificate is a boundary only inside a project that is also `restricted=true` ([helper.md](helper.md)) |
