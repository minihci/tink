# What tink is: the loop, the roles and the engines

**Status: a draft for discussion, last updated 2026-10-13.** It records what has been decided, what has only been defaulted, and what is open. Words are explained in [the glossary](glossary.md). Claims are marked **[docs]** (this repository), **[lab]** (seen on a real host), **[proposed]** (not built) or **[to confirm]**.

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
| network | `kind: network`, `kind: network-acl` | apply | disruption guard, unnamed-NIC warning, an attached ACL that exists nowhere | built **[docs]**; the ACL kind not yet applied to a real server |
| remote access | offers on apps, grants in a site resource | a gateway container per bridge | a grant with no offer, a route wider than needed | proposed |
| identity and single sign-on | group names in grants; offers on apps | the identity engine's configuration, rendered by tink | a grant that names a group the directory does not have | proposed; the engine pair was verified in a lab |

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
| mesh control plane | Headscale | defaulted | Tailscale's hosted service | reviewed in use: works; API keys have no scope. Policy delivery was tried on a throwaway: `policy set` inside the container in database mode needs no key, is validated and immediate, and keeps the previous policy on failure; file mode plus a SIGHUP also works **[lab, source]** |
| single sign-on | Authelia | decided | Authentik, Kanidm, Pocket ID, Keycloak | compared on documentation, then verified with LLDAP in a lab ([below](#identity-what-the-lab-showed-2026-10-12-and-13)); OIDC clients are static configuration; the groups claim reaches an app through `userinfo`, or the ID token with a claims policy ([below](#identity-what-the-lab-showed-2026-10-12-and-13)) |
| identity store | LLDAP | decided | Authelia's own user file (what runs today), Kanidm, Authentik | a directory with an API and a declarative bootstrap script; people, groups and credentials; one volume of state |
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

### What exists in the code today

As of 2026-10-12 the pattern is a name for something to build, not something already in the code **[code]**.

- **Offers: one implementation.** `ingress.Registration` (name, project, domain, port, address) is built by `ingress.Discover` from `user.tink.ingress.{enabled,domain,port,address}` across all projects (`internal/ingress/keys.go`, `discover.go`). The old `user.ingress.*` names are still read. It is specific to ingress; there is no shared type or interface. A backup policy on a volume looks similar but is a different thing: the owner's policy for its own volume, not something offered to others.
- **Grants: none.** Access control is written by hand. `configs/authelia/configuration.yml` holds a default-deny policy, per-domain rules and the OIDC clients; the users are in a host-owned `users_database.yml`. `tink deploy` applies them (`internal/bootstrap`), outside the resolver and its kinds.
- **Nothing joins the two.** Ingress rendering contains no authentication, so an app that registers gets a route and no access rule.

**A first step that changes no behaviour (proposed):**
1. A shared `Offer` (role, instance, port, attributes), built per role from `user.tink.<role>.*` keys.
2. A shared `Grant` (group, target instance, port), read from the site's declaration.
3. One `Join` that returns the findings for `plan`: ok, offer with no grant, grant with no offer, too wide.
4. A per-role engine interface that renders the result into the engine's own configuration.

Ingress moves first, with its existing tests as the guard. The first grant would arrive with the first role that has one.

## People and groups

**Decision (2026-10-13):** identity is a role like the others. The default engines are LLDAP, the directory (people, groups, credentials), and Authelia on top of it (login, access rules, OIDC). They were compared on their documentation and then tried together in a lab.

### Identity: candidates examined (2026-10-12)

What each one provides, and what tink could drive it with. From the projects' own documentation, read on this date **[docs]**. LLDAP with Authelia was then run (next section); the others were not.

| Candidate | Provides | Declarative handle | Footprint |
|---|---|---|---|
| LLDAP + Authelia | LLDAP: a directory (users, passwords, groups) with a web UI where users change their own details. Authelia: login portal, OIDC, access rules. LLDAP has no OIDC of its own | LLDAP: a GraphQL API, and a bootstrap script that enforces users, groups and attributes from a file. Authelia lists an `lldap` implementation preset | two small services; SQLite by default for each |
| Authelia alone (what runs today) | the same Authelia, with users in a file it also writes back, for example on a password reset | the file; it is not watched unless `watch` is set | one service |
| Kanidm | OIDC provider, read-only LDAP gateway, RADIUS, passkeys; administered mostly through its CLI | none mentioned in its README | one service with its own database and replication |
| Authentik | an identity provider with its own users and groups | blueprints: declarative YAML in a watched directory, an OCI registry or the database, applied in one transaction | needs PostgreSQL; at least 2 CPU cores and 2 GB RAM are stated |
| Pocket ID | an OIDC provider, passkey-only; groups decide who may sign in to which client | its REST API and LDAP/SCIM pages exist but were not read | a single binary or Compose |

Sources: github.com/lldap/lldap; authelia.com (LDAP, file, storage and OpenID Connect provider configuration, LDAP integration); github.com/kanidm/kanidm; docs.goauthentik.io (blueprints, Docker Compose install); pocket-id.org/docs.

### Identity: what the lab showed (2026-10-12 and 13)

LLDAP v0.6.3 and Authelia 4.39.28, pinned by digest, in a project of their own on a private bridge, on a real host, declared and applied with tink **[lab]**.

- **The stack.** A project, a bridge, a profile, two volumes and two instances. The first plan said six creates, a second said `no changes`.
- **LLDAP is scriptable.** Its API creates and deletes users, groups and memberships (groups are addressed by integer id, so a declarative layer maps names to ids). **No API call sets a password**: that is a separate OPAQUE exchange, driven by `lldap_set_password`, which the image ships and which takes a token and the password from the environment. A password hash cannot be imported from another system as a result (not tested).
- **A declarative path exists in the image.** `bootstrap.sh`, with the tools it needs, reads one JSON file per group and user (a password can come from a `password` or a `password_file` field). It issues an update for every user on every run and sets a password on every run when the file carries one. **Removal is report-only by default** (`[WARNING] User "x" is not declared in config files`) and **deletion is opt-in per class** (users, groups, memberships), and it never touches the admin user or the built-in groups. Run with the users flag, it deleted the undeclared user and nothing else.
- **Authelia with `implementation: lldap` worked first time.** For three users and three hostnames the decisions were the expected ones (allowed where a group matched, forbidden where it did not, unauthenticated refused), and the groups reached the proxy in a `Remote-Groups` header. **A group change in LLDAP was seen by an existing session within the refresh interval** (60 s with one minute), with no restart and no new login.
- **State is two volumes:** LLDAP's data, and Authelia's configuration directory with its database (second-factor secrets live there, not in the directory). Both survived deleting and re-applying both instances. The new instances got new DHCP addresses and nothing broke, because Authelia reaches the directory by name.
- **OIDC works with LLDAP's groups.** Two confidential clients, registered statically in Authelia's configuration. Discovery lists the `groups` scope and claim. With the default claims the ID token carries **no groups**, but `userinfo` does; with a **claims policy** on the client the ID token carries them (and the email and name claims). A user with no groups gets `groups: null`, not an empty list. The access token is opaque unless the client asks for a signed (JWT) one, which some apps need. The signing key was generated inside the container and never passed through tink; a wrong client secret is refused.
- **tink said what it did not do.** Changing the instance's environment and its config file updated both, did not restart Authelia, and the apply output said so and named `restart: true`, which restarts on apply (not exercised).
- **Not tested:** a registered second factor surviving a recreation, importing a password hash, `restart: true`, and backups of the two volumes.

### What this means for tink

1. **Direction (replaces the earlier "tink owns people and groups"):** the engine owns people, groups and credentials. Tink's YAML names groups in grants, and `plan` checks that each named group exists in the directory (a read; one API call or LDAP search) **[proposed]**.
2. **Declaring people is optional, and not built.** The engine's bootstrap script is a ready-made declarative path with the removal model tink wants. Using it needs two things tink lacks (below).
3. **Two projectors,** not a translation layer: Authelia's configuration (clients and access rules, from offers and grants), and the mesh control plane's group list.
4. **Two gaps the lab found.** (a) Secrets cannot be placed in files: `${secret:}` works only in `environment.*`, and `kind: file` content is world-readable, so the bootstrap `password_file`, or an OIDC signing key, cannot be expressed. Authelia itself was fine, because its template filter reads environment variables. (b) There is no resource for directory content.
5. **OIDC fits offers and grants.** Authelia's clients are static configuration with no dynamic registration, so registering an app means rendering configuration and restarting. What an app *offers* is its registration: redirect URIs, scopes, whether it reads claims from the ID token or from `userinfo` (a claims policy), and whether it needs a JWT access token. What the site *grants* is which groups may sign in to it (a per-client authorization policy on group membership). The client secret goes both ways: the plaintext into the app (a tink secret in its `environment.*`), a digest into the engine. The digest is salted, so it cannot be recomputed on every run; tink would have to keep it, or change it only when the secret changes.

## Where it is strong, and where it is weak

As of 2026-10-12, from the migration lab.

- **Found and fixed:** a service that needs a file at start raced tink's file push on first creation (the app exits, Incus restarts it in a loop, and tink's calls that land in a gap fail): 4 of 12 first applies failed in the lab. Fixed by placing an instance's files before its first start (#78, [design](first-start-files-design.md)): 11 of 11 converged **[lab]**.
- **Strong:** the reconcile discipline (a build with these features planned 30 real stack files identically to the previous one, apart from the lines it added **[lab]**; a two-service identity stack was planned, applied, deleted, re-applied and re-planned exactly **[lab]**); encoded operating knowledge, such as the warning for an OCI NIC without `name:` **[lab]**; claims that are tagged by how they were checked.
- **Weak:** removal is report-only (LLDAP's bootstrap script shows a better shape, see open decision 1); secrets cannot be placed in files, and nothing can declare directory content **[lab]**; cross-stack references have no clean expression (`depends_on` stays inside a stack **[docs]**); nothing tells you when something fails (no notifications **[docs]**).
- **Possibly wrong:** a single helper that gains reach with each job it takes on, under a certificate with the reach of root **[docs]**.

## Open decisions

1. **Removal.** Replace "report, never remove" with an explicit, scoped operation based on the ownership stamp. A shape that already works in the identity engine's bootstrap script: report by default, deletion opt-in per class (users, groups, memberships), and the system's own objects excluded.
2. **Helpers.** A helper is a tink container that does a role's work. The first one happens to be a long-running scheduler. Whether each role gets its own, and whether they share one `tink` project.
3. **Remote access v1** supports project-private bridges only.
4. **Name of the remote-access feature:** `overlay`, `mesh` or `vpn` (see the collisions in the glossary).
5. **Whether tink also declares people and groups** (through the engine's bootstrap script, which needs secrets in files), or only reads them to validate grants.
6. **Notification.** How a failure reaches a person.
7. **Cross-stack references**, for shared infrastructure.
8. **Secrets in files.** `${secret:}` is limited to `environment.*`; a secret that must be a file (a password file, a signing key) cannot be placed. Whether to add a file kind with a restrictive mode that accepts secrets.
9. **Client secrets across two sides.** Where the salted digest of an OIDC client secret is kept, so that `plan` stays stable while the plaintext is a tink secret in the app.
10. **Delivering a policy to the mesh control plane.** Database mode with `policy set` run through the exec API is the default the lab supports. The exec guard it needed is built and verified (`check_output`, #79; [design](exec-output-check-design.md)): converged when `policy get` equals the declared file, so it heals from drift and a bad policy fails the apply with the previous one kept. A separate `policy check` is not needed in database mode, where the set validates.
