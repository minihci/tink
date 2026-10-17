# Platform recipe, stage 1: accounting for a real stack, and the shape gaps already in tink

**Status: working notes for [platform-recipe.md](platform-recipe.md), 2026-10-17. Nothing here is built.** Claims are marked **[code]** (read in this repository, not run), **[docs]**, **[lab]** or **[proposed]**.

Stage 1 of that note is "account for every literal in `platform.yaml` as recipe, fact, generated or outside the stack, with no code". Part 1 does that for the author's platform stack for the home server (24 documents, about 390 lines of content). Part 2 asks the same question of tink itself: what already has the shape of **recipe plus facts**, in the wrong form or in the wrong place.

## Part 1: the accounting

**Method.** A script extracted every IP and CIDR, MAC, hostname, directory DN, image digest, secret reference, size and time zone from the content lines (comments dropped) and counted them; the rest (names, flags, ports, schedules, file bodies) was classified by hand from a read of all 24 documents. Four classes: **recipe** (tink would write it, whatever the site), **fact** (only the user can say), **generated** (derived from other declarations by `tink join`), **outside** (not a resource in the stack today).

### Every literal

| Literal | Count | Class | Source or rule |
|---|---|---|---|
| `10.10.50.1/24`, DHCP range `.100-.250` | 2 | derived | one fact, the platform subnet. Convention: `.1` gateway, `.2-.99` static for helpers, `.100-.250` DHCP |
| `10.10.50.3`, `.10`, `.11`, `.12` | 4 | derived | a per-role offset table in the recipe (ingress 3, identity 10, sso 11, mesh 12) |
| `10.10.50.3/32` (route, two ACL rules), `10.10.50.11/32` | 4 | derived | the role graph: who may call whom (ingress to sso and identity, sso to identity, mesh advertises ingress) |
| `10.0.142.230`, `.231` (and their `/24` forms, and the zone-file A records) | 6 | **fact** | the two LAN addresses, outside the router's DHCP pool; `/24` comes from the LAN CIDR |
| `10.0.142.0/24` | 1 | **fact** | the LAN CIDR (here it also guards the directory's UI to LAN clients) |
| `1.1.1.1`, `8.8.8.8` | 2 | recipe default | public resolvers for the ACME check, needed because the dns helper is authoritative for the zone |
| `10.10.60.10` | 2 | **generated** | the canary app's address and port in a hand-written route; `tink join` renders it from the app's offer |
| MACs `02:1a:2b:00:23:30`, `:31` | 2 | derivable, or unneeded | a prefix plus a per-role byte. They keep a recreated NIC's MAC stable; whether they are needed at all with static addresses is an open question |
| `home.xlii.co` (5 bare, the rest as a suffix), `hostmaster.`, SOA | 18 | **fact** | the zone. Labels `auth`, `id`, `ns`, `db.<zone>`, `hostmaster` are recipe |
| `whoami.`, `whoami-admin.` | 4 | **generated** | the canary's two `publish` documents |
| `identity.incus`, `sso.incus` | 4 | recipe | the helpers' names on the platform bridge |
| `hs.xlii.co` | 1 | **fact** | the mesh control plane's URL |
| `DC=home,DC=xlii,DC=co` (and the user DN) | 3 | derived from the zone | **fixed at birth**: Authelia names users by it, so it is hard to change later |
| `nothing.invalid` | 1 | recipe | Authelia refuses `default_policy: deny` with no rules, so one rule keeps it honest (found live) |
| image digests | 5 | recipe | pins |
| `${secret:...}` | 9 | 7 generated, 2 **supplied** | JWT secrets, keys, session and storage secrets and the admin and bind passwords can be minted; the Cloudflare token and the mesh auth key cannot |
| `256MiB` x4, `128MiB` | 5 | recipe | per-role limits |
| `America/Denver` | 1 | **fact** | the time zone (also the helper's) |
| ports `3890`, `17170`, `9091`, `53` | 4 | recipe | each engine's contract |
| backup `0 3 * * *`, `14d`; copies `0 4`/`0 5`, `30d` | 18 | recipe default | the same block on three volumes |
| backup targets `nas` (same-host pool `nas2510`), `offsite` (remote `vps`) | 2 documents | **fact** | where copies go, and the truth about whether it is the same host |
| restore checks `test -s /data/users.db`, `/data/db.sqlite3`, `test -d /data/caddy` | 3 | recipe | engine knowledge: what "this engine's data restored" looks like |
| `tink`, `tink0`, ACL, volume and instance names | ~25 | recipe | conventions (a site name may prefix them) |
| `features.images: "false"` | 1 | fact, or recipe default | images cached in the default project's store |
| the helper block: project `tink-helper`, network `incusbr0`, `remotes: [host, vps]`, time zone | 1 document | **fact** | where the daemon helper lives and what it may reach |
| `restart: true`, `depends_on` | 4, and the `depends_on` lines | derived | from what each engine reads only at start, and from the graph |

Nothing was left unclassified. The facts, stripped of their repetitions, are about fifteen: the zone, the time zone, the platform subnet (which can default), the LAN's parent NIC, CIDR and two addresses, the certificate challenge and its token, the mesh control plane's URL, the backup targets, where the helper lives and what remotes it holds, and whether images are shared.

### Document by document

| # | Document | Owner | Note |
|---|---|---|---|
| 1-2 | stack, project `tink` | recipe | `features.images` is a fact |
| 3 | network `tink0` | recipe, from the subnet | |
| 4-5 | profiles `tink-base`, `default` (root disk) | recipe | the second is a workaround for a `verify` bug fixed in #89 and run live on 2026-10-17; a recipe can omit it |
| 6, 11, 12 | ACLs `tink0-in`, `identity-in`, `sso-in` | recipe, derived from the role graph | nobody would edit these per site |
| 7-8 | backup targets | **facts** | |
| 9, 13, 20 | volumes `identity-data`, `sso-data`, `caddy-data` | recipe | one block, three times |
| 22 | volume `mesh-state` | recipe | `none:` with a reason, because the node re-enrols |
| 10, 14, 16, 19, 23 | the five helpers | recipe, with facts | limits, restart, role stamp, NIC and ACL attachment are the same lines each time |
| 15 | `sso-config` (50 lines) | recipe skeleton, **fact** (zone), **generated** (access rules) | the rules are hand-written here for the canary |
| 17 | `dns-zone` | recipe skeleton, **facts** (zone, addresses) | static names only; published names go to a generated `/hosts` |
| 18 | `dns-corefile` | recipe | |
| 21 | `ingress-caddyfile` (52 lines) | recipe skeleton, **facts** (zone, LAN CIDR, challenge), **generated** (routes) | three owners in one file; the clearest split point |
| 24 | `kind: helper` | **facts** | adopts the existing helper in place |

By size: the five instances and the four `kind: file` documents are about three quarters of the content. The file documents (135 lines, a third) are engine configuration written as text, and two of them hold hand-written copies of what `join` renders.

### What this settles

1. **"Facts in, stack out" holds for this stack.** About fifteen facts, repeated dozens of times, account for every site-specific literal.
2. **Four documents mix owners and need splitting**: `sso-config`, `dns-zone`, `ingress-caddyfile` and, in a smaller way, `dns-corefile`. The dns helper already splits generated (`/hosts`) from declared (the zone file); the Caddyfile and Authelia do not.
3. **Some facts are fixed at birth**: the zone (through the base DN and the cookie domain), the platform subnet and role offsets (through ACLs and clients), the LAN addresses (through every resolver rule), the project and instance names (through volumes and stamps). `plan` should refuse to change them without an explicit flag, as it does for a bridge that instances use (`allow_disruption`).
4. **Four things are outside the stack today**: seeding the directory's bind account, approving the mesh route on the control plane, the router or resolver forward rule, and creating people. The recipe has to list them as "still to do", since two of them happen on another system.
5. **The draft facts file in the main note was missing**: where the helper lives and what remotes it holds, whether images are shared, and the backup targets' `engine`. It is updated there.

## Part 2: what tink has that should have this shape but does not

Searched in `cmd`, `internal`, `configs`, `build` and `docs` of tink at `2351d59`, and in the author's tron repository for the lab and platform files. Ordered by how much they block a shipped recipe.

| # | What exists | Shape it has | What is wrong with the form | Evidence |
|---|---|---|---|---|
| 1 | **`tink deploy`**, `configs/`, `deploy.env` | A facts file (ten facts: domains, the bridge, the API address, two static IPs, the pool, an admin, a registry) plus a recipe (Go and text templates) for Caddy, Authelia and Incus UI | Imperative, text-templated, no `plan`, it deletes and relaunches the three instances on every run **[docs]**, replaces the whole server config with `incus config edit`, and shells out. It is also **a second, different definition of "the platform"** next to the author's stack (LLDAP, Authelia, CoreDNS, Caddy, Tailscale) | `internal/bootstrap/instances.go`, `configs/deploy.env.example`, `configs/daemon/server-config.yaml` |
| 2 | **Ingress routes, twice.** `tink ingress reconcile` and `tink join`'s Caddy rendering | Both turn declarations on apps into Caddy routes | Two mechanisms with two shapes: `user.tink.ingress.*` found by the instance **name** `ingress` (`--ingress-instance`), writing to a route directory whose host-path default names the pool `default` and a volume `ingress-routes`; and `user.tink.offer.*` plus `kind: publish`, found by role stamp. The README says ingress "has not moved to the shared offer" | `internal/ingress/ingress.go`, `internal/join/render.go` |
| 3 | **Role contracts that live in comments and constants** | A role (dns, ingress, sso, identity) is something code talks to | The contract is implicit: `/hosts` and `/db.<zone>` are Go constants; the Caddy snippets `sso` and `no_portal_cookie` are "the platform's to define, by hand" (a comment in `join/render.go`); the engine ports (3890, 17170, 9091) appear only in each site's hand-written ACLs. Nothing states a role's contract as data that `plan` could check | `internal/join/apply.go`, `internal/join/render.go`, tron's `platform.yaml` |
| 4 | **The stack language has no defaults** | A shared shape for helpers | Every stack repeats it: `boot.autostart` and `boot.autorestart` (26 occurrences across tron's stacks), `name: eth0` (24; Incus needs it for the OCI network setup), `limits.memory` (18), `user.tink.role` (14), the NIC-level ACL egress line, and the same eight-line backup block (the `0 3 * * *` schedule appears 12 times across three stack files, three identical blocks in the platform stack alone) | tron's `platform.yaml`, `smarthome`, labs |
| 5 | **Per-engine knowledge written per site** | "How to tell this engine's data restored" and "what this engine must have" are the engine's, not the site's | Each site hand-writes the restore check for LLDAP, Authelia and Caddy; limits; the one-rule workaround; CoreDNS's single `hosts` plugin; the Caddy cookie-stripping snippet (tested nine ways, then copied by hand) | `platform.yaml` lines 98, 164, 385, 235, 408 |
| 6 | **The helper's authorization scriptlet** | A policy that is a function of the stack: which volumes, which copy targets, which status volume | tink ships a hand-written 59-line script that only checks the protocol. The author's lab has a **generator** that builds the rule set the helper needs from a table (volumes, their targets, the status volume, the helper's fingerprint; the lab measured ten rules for the copying work). That table is derivable from the stack itself | `configs/daemon/authorization.star`; tron `authz-lab/gen-helper-scriptlet.py` |
| 7 | **`tink helper install`** and `kind: helper` | A recipe for one role, with facts (project, network, pool, time zone, remotes) | Imperative: it makes the project, volumes, instance and enrolment outside the plan graph. Its defaults are by name (`helper`, `tink-helper`) and it falls back to an unpinned `alpine:3`. The platform stack has to **adopt** it in place, because it lives in a different project and on a different bridge from the other helpers | `internal/helper/install.go` |
| 8 | **The restore-check project** | A small recipe: a restricted project, its own `default` profile with a root disk and no NIC | It exists as prose in `volume-backup.md` that each site copies, and `verify --check-project` needs it to exist first | `docs/volume-backup.md` |
| 9 | **Pins** | A decision about when an engine moves | Inconsistent: `deploy` uses `caddy:2.11.4` by tag and `authelia:latest` unpinned; the helper uses `alpine:3`; the author's platform stack uses digests, with `on_image_change: report` | `internal/bootstrap/instances.go`, `internal/helper/install.go` |
| 10 | **Mesh policy** | Groups to a policy is derived state | tink has a projector for Headscale's `groups`, but the policy itself (`policy.hujson`) is hand-written per site, and the control plane is hand-run on the VPS and not in any stack | `internal/mesh`, tron `platform/headscale-policy/` |
| 11 | **`tink daemon install` and the `tink-daemon` unit** | The older way to run the reconcile | Superseded by the helper, but `deploy` still installs the unit and warns that the two would reconcile the same routes; a branch (`retire-daemon-install`) exists | `internal/bootstrap/reconciler_daemon.go` |

### What already has the shape

Worth keeping as the pattern: **`tink join`** (a pure function from declarations to renderings, with hand-written golden files); **the backup opinion** (a default, a check that notices departure, a reasoned opt-out, machinery that makes following it cheap); **`kind: helper`** (adopt what exists, change nothing); **role and leg stamps** (`user.tink.role`, `user.tink.leg.*`), which are the beginning of a role contract; and **`${secret:}` with `--generate`**.

One finding is a relief: tink's code carries **no site literals**. The only hits for `xlii` or a home LAN range in non-test code are comments and fixtures. The coupling is in the files, the names and the paths above, not in constants.

### What this adds to "what has to be true first"

The main note lists six preconditions. This audit adds two, and sharpens one:

- **A role contract as data** (item 3): for each role, the stamps it must carry, the files and ports it exposes, the snippets it must define, and what `join` renders into it. `plan` checks a helper against it. This is also what makes "bring your own helper" real.
- **Stack-level defaults, or a helper shape** (item 4): without them a recipe emitting resources in the stack language is still thirty lines of boilerplate per helper, and users who eject inherit it. Incus profiles can hold the instance-config half (restart flags, the time zone, stamps, the root disk) but not NICs, volume policy, file contents or ACLs; see the [design note](platform-recipe.md#what-a-profile-can-hold-as-a-default).
- **Sharpened:** the generated-content split is two separate mechanisms today (item 2), so the first change is to **pick one** (the offer and publish path) and move `tink ingress` onto it, before the recipe depends on either.

### Suggested order

1. Ingress onto the shared offer (item 2): it unblocks splitting the Caddyfile.
2. Role contracts as data (item 3), starting with dns, ingress and sso, whose contracts `join` already assumes.
3. Stack-level defaults for the repeated blocks (item 4).
4. Scriptlet generation from the stack (item 6), because the table already exists in the lab.
5. Fold `deploy` into the recipe (item 1), as the opinion-as-code note's stage 4 says, once the recipe plans clean against the live platform stack.
