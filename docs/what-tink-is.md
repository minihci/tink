# What tink is: the loop, the roles and the engines

**Status: a draft for discussion, 2026-10-12.** It records what has been decided, what has only been defaulted, and what is open. Words are explained in [the glossary](glossary.md). Claims are marked **[docs]** (this repository), **[lab]** (seen on a real host), **[proposed]** (not built) or **[to confirm]**.

## The idea

tink is declarative. You describe a site in YAML, `plan` shows what would change, and `apply` makes it so. It orchestrates technologies chosen for their strengths and fills the gaps between them, so a home lab or a small self-hosted site gets pieces of infrastructure (backups that are verified, ingress, secrets, remote access) at close to no cost. It does not replace the technologies it uses. The one hard requirement is that Incus exists and tink has access to it.

## The loop

1. **Declare** in a stack: documents of different kinds, applied together.
2. **Plan**: compare the declaration with what Incus reports. Say what would change, what is blocked and why, and what looks wrong.
3. **Apply**: make the change. Anything that could interrupt running work is blocked unless the declaration allows it.
4. **Keep and verify**: a helper runs the work that must keep happening (copies, route reconciliation) and says when it cannot.

What tink must remember it writes on the objects themselves, under `user.tink.*`, so the state stays with the thing it describes **[docs]**.

**Where the loop is incomplete today.** It creates and updates, but it never deletes: a thing the stack no longer declares is reported as an extra and left alone. That was a safe first rule and it hardened into a stronger one than it should be. The ownership stamp (`user.tink.stack`) is what would make an explicit, scoped removal safe. See [open decisions](#open-decisions).

## One pattern: a capability

The pieces that exist, and the one being designed, share a shape: a declaration, something that acts on it, and checks that say when it is wrong.

| Capability | Declaration | Acts | Checks | Status |
|---|---|---|---|---|
| backup | `backup:` on a volume | the helper's scheduler | no helper, missing remote, a 3-2-1 note | built **[docs]** |
| ingress | `user.tink.ingress.*` on an instance | the helper (or the host daemon) renders routes | bad address skipped with a warning | built **[docs]** |
| secrets | `${secret:NAME}` in an instance's `environment.*` | resolved at apply | **[to confirm]** | built, narrow **[docs]** |
| network | `kind: network` | apply | disruption guard, unnamed-NIC warning | built **[docs]** |
| remote access | offers on apps, grants in a site resource | a gateway container per bridge | a grant with no offer, a route wider than needed | proposed |
| single sign-on | offers on apps, grants for groups | the identity engine | the same join | proposed |

## Roles and engines

A **role** is a job tink needs done that more than one technology can do. An **engine** is the technology that does it today. The platform, Incus, is not a role: it is required.

The rule that keeps this contained is that tink's code talks to a role, and an engine sits behind it. Today the code is written for one engine per role, so the role boundary is where a second engine would be added.

**Status of an engine**
- **decided**: chosen over alternatives, for a recorded reason.
- **defaulted**: in use and understood well enough, chosen without comparing. Open to replacement.
- **unexamined**: in use or planned, not yet looked into.

| Role | Default engine | Status | Alternatives | Notes |
|---|---|---|---|---|
| platform | Incus | decided | none | one API for containers and VMs; the only hard requirement |
| reverse proxy | Caddy | defaulted | nginx, Traefik | automatic certificates and a small text config that tink renders |
| mesh | WireGuard-based, stock Tailscale clients | decided | NetBird, ZeroTier | a common pairing for a home site plus a VPS |
| mesh control plane | Headscale | defaulted | Tailscale's hosted service | reviewed in use: works; API keys have no scope; `policy set` works only in database mode **[lab, source]** |
| single sign-on | Authelia | unexamined | Authentik, Keycloak | chosen without a comparison |
| backup transport | an Incus remote | decided **[to confirm]** | none | copies use Incus's own copy |
| secrets | age | decided **[to confirm]** | not compared | see the secrets design note |

## Offers and grants

An **offer** says what an app can provide. It is written by whoever writes the app's stack and travels with the app. A **grant** says who may use it. It is written by whoever runs the site and lives in one place that is reviewed as a whole.

Why split them, in order of weight at this scale:
1. **A stack stays shareable.** An app's stack says "I serve a web UI on 8123". Your people and rules stay in your own file.
2. **`plan` can join them.** An offer with no grant is unreachable (the safe default); a grant with no offer is a mistake worth a warning.
3. **The audience is not widened by the app.**

```yaml
# the app's stack
kind: instance
name: ha
config:
  user.tink.<role>.expose: "8123"
```
```yaml
# the site's stack
kind: <role>
name: home
allow:
  - {group: family, to: ha, port: 8123}
```
```
ha:8123      offered, granted to group:family     ok
grafana:3000 offered, no grant                    note: unreachable
mqtt:1883    granted to group:admin, not offered  warning: grant with no offer
```

**Granularity.** Default: a grant names the instance and port. It is explicit, and an app cannot widen its own audience. Alternative: the app labels an offer and grants map groups to labels. It scales better, but the app classifies itself.

## People and groups

**Direction (not settled):** tink owns people and groups, and translates them into the shape each role needs: users and groups for the identity engine, users and policy groups for the mesh control plane. Reason: one source of truth, while each engine keeps its own model. Alternative: let one engine be the source and translate outward.

Open inside this: where they are declared (a kind of their own, or a block in the stack), how a person's credentials are handled without putting them in the YAML (tink secrets), and how a person's devices are enrolled.

## Where it is strong, and where it is weak

As of 2026-10-12, from the migration lab.

- **Strong:** the reconcile discipline (a build with these features planned 30 real stack files identically to the previous one, apart from the lines it added **[lab]**); encoded operating knowledge, such as the warning for an OCI NIC without `name:` **[lab]**; claims that are tagged by how they were checked.
- **Weak:** removal is report-only; cross-stack references have no clean expression (`depends_on` stays inside a stack **[docs]**); nothing tells you when something fails (no notifications **[docs]**).
- **Possibly wrong:** a single helper that gains reach with each job it takes on, under a certificate with the reach of root **[docs]**.

## Open decisions

1. **Removal.** Replace "report, never remove" with an explicit, scoped operation based on the ownership stamp.
2. **Helpers.** A helper is a tink container that does a role's work. The first one happens to be a long-running scheduler. Whether each role gets its own, and whether they share one `tink` project.
3. **Remote access v1** supports project-private bridges only.
4. **Name of the remote-access feature:** `overlay`, `mesh` or `vpn` (see the collisions in the glossary).
5. **Where people and groups are declared.**
6. **Notification.** How a failure reaches a person.
7. **Cross-stack references**, for shared infrastructure.
