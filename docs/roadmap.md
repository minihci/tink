# Roadmap: tink's capabilities, the proving grounds that test them, and what to build next

**Status: a draft for the author to edit, 2026-10-17. First written by an outside reviewer at `a7ec084` (main after #103), then reframed after the author's review: the plan is organised by tink's capabilities, and the sites and workloads tink has been run on are listed as proving grounds, not as deliverables.** Nothing here is decided. Claims are marked **[code]** (read in this tree, and `go build`, `go vet` and `go test ./...` pass at `a7ec084`), **[pr]** (the merged pull request), **[docs]** (what a doc in this repository says, not re-checked), **[tron]** (the author's tron repository, `~/my/projects/tron`, read-only), **[ssh]** (a host looked at read-only over ssh on 2026-10-17), **[session]** (the author's own notes from the 2026-10-17 VPS sessions, not in any repository) and **[inferred]** (reasoning; check it). Sizes: **S** (a small PR), **M** (a few PRs, or one with a live lab), **L** (a design plus several PRs and labs). No dates.

## In one page

1. **Tink is the tool. tron, the edge host and the Rust VPS are where it is proven.** Three sites with three different shapes, and a dozen workloads, have run on it. This plan is ordered by what tink must be able to do. A site's or workload's own rollout (the Rust stack, a tron upgrade) is listed once, in section 5, only to show the order, and is not a tink deliverable.
2. **A gate (section 3) decides what gets built.** A capability is built when a named proving ground needs it now and its exit test can run there. It becomes generic (a recipe field, a new kind, a new verb) only with a second ground behind it, or a stated reason. The author is the only user today, so nothing is built for users who do not exist yet.
3. The binary is in better shape than the docs suggest. Resolver, backups, secrets, `--remote`, the daemon helper and `join` are built and have been run live. Most of what feels "in flux" is design that has not been built, plus about a dozen docs whose status lines are out of date.
4. **Nothing merged since 2026-10-07 has been released.** `v0.1.4` is the newest tag, with 91 commits on top of it: `join`, `kind: publish`, `kind: helper`, the status volume, `--api-via network`, `--check-project`. A release build installs the helper image of its own version, so no ground can run the new helper work without `--binary` until there is a release **[code, git tags]**.
5. **Three shapes have been tried (section 2):** a LAN host behind NAT (tron, the full spine); a public edge on one address (`incus.xlii.co`, a live site built before the recipe, with the old ingress daemon and `tink deploy`); and a non-web, stateful, UDP workload on a clean public host (the Rust VPS). The `join` public-site fixture in #103 was synthetic. The edge host is the real thing it imitates.
6. **Anything stateful needs a stop that saves.** Rust is the ground that found it; the databases on the other grounds probably share it **[inferred]**. The first form is a hand-written host unit, already measured. The tink feature (`stop:` plus one verb) follows, and its value is in tink's own stops (`on_image_change: rebuild`, `restart: true`), which kill the app without a save today.
7. **The platform recipe is the right direction, built one role at a time from the roles the grounds share,** and with fixtures taken from the live hosts. Not from tron's full spine, and not from a daemon-helper-only first step either.
8. Two pairs of mechanisms overlap and should be merged before the recipe depends on them. **Ingress:** `tink ingress reconcile` and `join`'s Caddy rendering. **"The platform":** `tink deploy` and the recipe. The edge host runs both old mechanisms, so freezing them has a cost that decision 5 has to carry.
9. Some designs have no ground behind them. Park the verify helper with its ledger, `kind: network-forward`, profile-carried defaults and `access` documents.
10. Ownership stamps should be written from birth on a fresh host. Minimal watching is the quiet precondition of "reliable", and nothing is built. The docs disagree about two decisions that matter (copied template or shipped recipe; whether a restricted certificate is a boundary), and both should be settled in the docs first.

## 1. Where things stand: tink's capabilities

### Built and exercised live

| Capability | Evidence |
|---|---|
| `tink plan` / `plan apply` (graph, levels, BLOCKED, image drift, extras) | `internal/resolve` **[code]**; Nextcloud, Nightscout, Immich, Mosquitto and Matter stacks, the tron platform stack (second plan quiet, 2026-10-13) **[docs, tron]** |
| `tink run`, `tink export` | README; [export.md](export.md) "Verified live" **[docs]** |
| `tink deploy` | built `incus.xlii.co`, the edge host (Incus 7.4): `/root/incus-host/scripts/deploy.sh`, its authorization scriptlet and a tink build of 2026-10-09 are there **[docs: README; ssh]**. Not used on tron or the Rust VPS |
| `tink ingress reconcile` and the host `tink-daemon` unit | live on `incus.xlii.co` **[docs: README]** |
| Secrets (`${secret:}` in `environment.*` only, age store, `--generate`) | #9; Immich **[docs]**; the Rust RCON password on the VPS **[session]** |
| Volume backup: snapshot policy, copies to pool and remote targets, restore, verify, 3-2-1 check | #10-#13, #18, #19, #22, #23; TrueNAS and remote runs in [volume-backup-design.md](volume-backup-design.md) **[docs]** |
| `backup verify --check-project` | #96; run on Incus 7.5.1, 2026-10-17 **[pr: #99]** |
| Daemon helper: install from the published image, scheduled copies, restart after kill, drain and upgrade | #21, #26-#33, #56, #57 **[pr]**; lab host **[docs: helper.md]**. The helper's whole job history on tron is `backup-run` **[tron: STATUS.md]** |
| Helper status on its own volume; host API over the network (`--api-via`) | #94, #95; run on the VPS, 2026-10-17 **[pr: #99]** |
| `kind: helper` (adopt and report) | #92; it adopts tron's helper (still on `v0.1.4`) **[tron: platform.yaml]** |
| `--remote`, `tink remote` | #15, #17; from a laptop **[docs]** |
| `kind: network`, `kind: network-acl` | #74, #87; each run live once **[docs]** |
| Files placed before first start, and under a disk mount | #78 (11 of 11 in the lab), #85 **[docs]** |
| `kind: exec` with `check_output` | #79; a throwaway Headscale **[docs]** |
| `tink identity groups` | #80; run once against a lab LLDAP **[docs]** |
| `tink join` (print) and `join apply dns` | #91, #92; tron's `/hosts` is generated by it, run by hand **[tron: LOG W3.2]** |
| Publish stamps, `join --from-server` | #97; on the VPS, output identical to the file-based join and to the golden files **[pr: #99]** |
| NAT proxy warnings (static address, overlapping listeners) | #103; read-only plans on the Rust VPS **[pr]**. A `nat` proxy stack was also applied there: a clean re-plan, and the app saw the client's real address (TCP only) **[session]** |
| OCI NIC `name:` warning | #77; checked against the lab host **[docs]** |
| Authorization scriptlet under deploy's routing | #93; Incus 7.5.1 only **[pr, docs: README]**. The edge host runs the earlier 7.4 form of it **[ssh]** |
| Releases: binaries and helper image | `v0.1.0`-`v0.1.4`, all dated 2026-10-07 **[code: tags]** |

### Built, tested only against fakes or fixtures

| Capability | Evidence |
|---|---|
| `join apply dns` against a real CoreDNS from `--from-server` | "Not run" **[docs: join.md]** |
| `join` for a public-only site (`public` leg, optional dns and mesh helpers) | #103; fixtures in `internal/join/testdata/public/` **[code]**. No real public-only platform exists yet |
| Install after a remove that kept the volumes | the bug was found live; the fix (#98) is unit-tested **[pr]** |
| File update under a mount while the instance is stopped | #90; fakes only **[docs: first-start-files-design.md]** |
| `allow_disruption: true`; a bridge update; an ACL on a NIC; a `reject` default | **[docs: network.md, network-acl.md]** |
| `on_image_change: rebuild` | `internal/resolve/rebuild.go` **[code]**; I found no live run recorded **[to confirm]** |
| The helper's ingress half (`--ingress`, through the file API) | **[code]**; no live run recorded. Off on tron **[tron: STATUS.md]** |
| Helper survives a host reboot | "not yet tried" **[docs: helper.md]** |
| `tink.exe` on Windows | built in CI, never run **[docs: remote.md]** |

### Designed, not built

| Design | Where | Note |
|---|---|---|
| Graceful stop: a `stop:` field, a verb, a host unit | PR #104 (open), `docs/graceful-stop.md` on `graceful-stop-design` | the measurements are solid; nothing is coded |
| Platform stack as a shipped recipe (`kind: site`, `tink platform plan`) | [platform-recipe.md](platform-recipe.md), [platform-recipe-accounting.md](platform-recipe-accounting.md) (#100) | stage 1 (the accounting) is done; stages 2-5 are not |
| Ownership stamps and scoped prune | `docs/ownership-stamps-design.md` on `docs/ownership-stamps-design` (unmerged) | only volumes carry `user.tink.stack` today **[code: backupmeta.StackKey]** |
| Daemon loop running `join apply dns --from-server` | [join.md](join.md), [helper.md](helper.md) | `internal/daemon` does not import `internal/join` **[code]** |
| Applying the caddy, sso, acl and mesh renderings | [join.md](join.md#what-it-does-not-do) | needs a generated-include split in Caddy and Authelia, a derived-rules marker in `network-acl`, and a decision on who writes them |
| Listener and bypass check on discovery legs | [offer-grant-join-design.md](offer-grant-join-design.md) D4 | `netstat` through exec; *unverifiable* on images with no shell |
| Narrow helper credential (restricted certificate, generated scriptlet) | [what-tink-is.md](what-tink-is.md) decision 2(a); tron `authz-lab/` | measured: 10 rules, no instance rights **[tron]**. Three of tron's policy volumes are in `default`, which cannot be restricted |
| Scheduled restore checks (a verify helper and a ledger) | `docs/scheduled-verify-design.md` on `docs/scheduled-verify-design` (unmerged) | |
| Authoritative apply, layers 2 and 3 | [authoritative-apply-design.md](authoritative-apply-design.md) | layer 1 (extras) is built |
| `kind: network-forward` | [public-exposure.md](public-exposure.md), [network-resources-design.md](network-resources-design.md) | unmeasured |
| Secrets in files (phase 2) | [secrets-design.md](secrets-design.md#later-phases) | blocks Authelia's OIDC keys and LLDAP's password file in a recipe |
| Watching and alerts (Gatus, a heartbeat, ntfy) | tron `platform/watching-design.md` | nothing built or applied **[tron]** |
| Retiring `tink daemon install` | branch `retire-daemon-install`, 82 commits behind main | |
| Restic job engine (backup step 4) | [volume-backup-design.md](volume-backup-design.md) | nothing in the code refers to it **[docs]** |

### Proposed or idea only

OIDC attributes on offers; `kind: access` (one instance using another's offer); per-name authentication strength; `accept:` for a known bypass; project-level `user.tink.site.*` stamps; role contracts as data; profiles carrying defaults (needs `plan`, `join` and `export` to read expanded config); a helper version policy (`on_version_change`); a role stamp that finds any helper; `kind: platform` and `tink platform eject`; the mesh gateway and control plane as helpers; a name for the remote-access feature; declaring people through LLDAP's bootstrap; notifications; cross-stack references; `tink mongo snapshot` (a stub command that returns "not yet designed" **[code]**).

### Docs whose status line is out of date, or that contradict each other

| Doc | What it says | What is true |
|---|---|---|
| [what-tink-is.md](what-tink-is.md) (dated 2026-10-13) | "nothing yet stamps the declarations onto the server" | stamps are built (#97) and run live (#99) |
| same, "The platform stack (proposed)" | "A template you copy and own (decided)" | [platform-recipe.md](platform-recipe.md) leans the other way. Not reconciled |
| [glossary.md](glossary.md) | platform stack "starts as a template you copy and own" | same conflict |
| [offer-grant-join-design.md](offer-grant-join-design.md) | header: "Nothing in it is built"; open question 8: "tink has none [no ACL kind]"; third step: "Not yet run against a real server" | three build steps are recorded below that header; `network-acl` is #87; #99 ran it live |
| [helper.md](helper.md) | "A restricted certificate is not a boundary on current Incus" | [what-tink-is.md](what-tink-is.md) and tron's `authz-lab` measured that it is a boundary when the project is also `restricted=true`. The two docs disagree |
| [helper.md](helper.md) | the design is PR #16, "an open pull request" | #16 is still open, 86 commits behind, last touched 2026-10-08. The doc it links to is not on main |
| [daemon-jobs.md](daemon-jobs.md) "Not here yet" | jobs can only be reached where the jobs directory is | `tink helper jobs|log|cancel` work remotely (#56) **[code: cmd/tink/helperjobs.go]** |
| [platform-recipe.md](platform-recipe.md) fact 5 and its open questions | a `public` leg "`tink join` does not model yet" | #103 added it the same day |
| `docs/ownership-stamps-design.md` (branch) | publish stamps are "unmerged" | merged in #97 |
| [design-principles.md](design-principles.md) pattern 6 | the opinion-as-code note is the argument | that note (branch `platform-design`, 2026-10-08) proposes "opinions ship as stacks". The platform recipe supersedes it in part; nothing says which parts |
| README | `incus.xlii.co` is "the reference deployment" | the author's working sites are now tron and the new VPS. The README never mentions either |

The host facts that are not capabilities (where each ground stands) are in section 2.

**Leftover branches** that are merged in substance or abandoned: `public-exposure-checks` (#103), `claude/integration-abcf` (scratch), `docs/helpers-and-platform-stack` and `claude/docs-helper-does-not-verify` (their content is on main), `opinions-spike`, `headscale-stack` (78 behind). **[inferred]** None of these is worth rebasing except `retire-daemon-install` and the two design notes (ownership stamps, scheduled verify).

## 2. Proving grounds: where tink has been run

### What tink is aiming to be

Tink is a declarative layer over Incus for one person's sites: the plan and apply loop for what exists, opinions that make good practice cheap (verified backups, least privilege, refusals instead of guesses), and the small set of long-running services a self-hosted site needs. It should not become a platform people maintain.

**The binary** does every one-shot thing: plan and apply, run and export, backup run, restore and verify, secret, join, and the helper commands. It holds no state. **The platform stack** is the set of long-running instances that provide the standing version of the binary's work. The **daemon helper** (tink itself) tends. The **role helpers** (dns, ingress, sso, identity, mesh, and later monitor) serve and hold no credential. The rule is "derived state heals, declared state reports" [helper.md](helper.md#what-the-helper-is-for). Today the daemon helper does exactly one thing for real: it makes backup copies.

**The direction**, as I read it. Ship the platform stack as a recipe: tink owns the containers and the wiring, the user writes about fifteen site facts, and policy stays in offers and publishes. The grounds below are golden fixtures, not templates anyone maintains ([platform-recipe.md](platform-recipe.md#proving-it)). I agree with it, with one change of order (section 4, item 5).

### The grounds

| Ground | Shape | What is on it | Capabilities it exercises | State |
|---|---|---|---|---|
| **tron** | A LAN host behind NAT, with VLANs. The full spine: dns, ingress, sso, identity, mesh gateway, daemon helper | The owner's home server: Home Assistant, Immich, Nextcloud, Mosquitto, Matter, zigbee2mqtt and others **[docs, tron]** | Plan and apply, networks and ACLs, files, secrets, backups with a helper, `join` (the dns half by hand) | Live. Its helper is on `v0.1.4` **[tron]** |
| **The edge host**, `incus.xlii.co` (208.87.131.230) | A public edge on one address, Incus 7.4, 8 CPU, 31 GB, a 400 GB btrfs pool with 7.7 GB used. One shared bridge, `incusbr-ns` (10.77.20.0/24), for every project **[ssh]** | Project `default`: authelia, headscale (the mesh control plane), incus-ui, `ingress` (Caddy, owns :80 and :443 through userspace proxy devices carried in a profile), ts-gw. Project `nightscout`: app, its own Caddy, mcp and mongo. Project `tink-backup`: 48 copy volumes of tron's data, with tron's helper in the trust list **[ssh]** | `tink deploy`, `tink ingress`, the old `user.ingress.*` discovery, the authorization scriptlet, being a backup target | Live, built before the recipe. Only `headscale` carries any `user.tink` keys (three). The tink build on it dates from 2026-10-09 **[ssh]** |
| **The Rust VPS**, `rust.wyomarus.com` | A clean public host, Incus 7.5.1, one public address, a btrfs pool with images stored on it. No platform stack yet **[session]** | The legacy rootless Podman Rust server (stopped and masked), lab tests of the real image under Incus | NAT proxies, a stateful non-web app, graceful stop, offsite copies, the helper on a clean host | Newly set up. Tink at main `a7ec084`. No helper installed |
| **Labs** | Throwaway hosts and projects | The Headscale and CoreDNS labs, the `authz-lab` and `ha-eval` projects, Nextcloud, Immich, Mosquitto and Matter trials **[docs, tron]** | One feature at a time, as the design notes record | Torn down or kept for evidence |

### What "reliable enough to rely on" means, per ground

- **Rust VPS.** The Rust server runs from a stack in project `rust`; `plan` says no changes. A host reboot saves the world first and brings the server back. Snapshots are taken on a schedule, a copy leaves the machine, and one restore has been verified. An image update saves before it stops. Something outside the VPS tells the phone when the helper or the server is down. A `tink` project holds the daemon helper from a released image.
- **tron.** The spine keeps working as it does now. The daemon helper runs a current release. The dns records are kept right on a timer, not by hand. The routes and sign-on rules for published names stop being hand-written. Something tells the phone when tron goes quiet.
- **Edge host.** Not defined yet. It depends on decision 5.

### The workloads, and what each taught

A workload is not a tink feature. It is listed because it showed tink something, and its stack lives with the workload.

| Workload | Where | Shape it showed |
|---|---|---|
| Nightscout | edge | Four instances in one project: the app, its own Caddy sidecar, an MCP server, and MongoDB with one volume. It uses the old `user.ingress.*` stamps, and a database password sits in plain `environment.*` because it predates `${secret:}` **[ssh]** |
| Rust game server | Rust VPS | Stateful, UDP, non-web. Killed without a save by `incus stop`; saved by RCON `quit` ([graceful-stop.md](graceful-stop.md) on the branch of PR #104) **[session]** |
| Home Assistant | tron | A discovery leg that must not listen; the case for the listener check **[docs]** |
| Immich, Nextcloud | tron | Large volumes and databases: backups, restore checks, and a database that also needs a stop that saves **[inferred]** |
| Headscale, Authelia, LLDAP, Caddy | tron, edge | The platform's own roles. They are what the recipe would ship **[docs]** |

### What the grounds say

- **The edge host is the real public platform; the Rust VPS is a clean lab.** The first draft of this plan took the Rust VPS as the "VPS shape", which made the recipe's first fixture a daemon helper and little else. The edge host already runs ingress, sign-on and the mesh control plane on one public address **[ssh]**. It is the better second fixture, and `tink export` (built) is a way to start from what it runs **[inferred]**.
- **The edge host's ingress sees every client as 127.0.0.1.** Its proxy devices are the userspace kind, `connect: tcp:127.0.0.1:80`, set in a profile **[ssh]**, and [public-exposure.md](public-exposure.md) measured that this mode hides the client address **[inferred]**. The `nat` form that #103 checks and that `join`'s `public` leg expects is not what the real site uses.
- **tron's offsite copies and the mesh control plane share one machine,** the edge host, whose tink build is eight days old and whose Incus is a minor version behind **[ssh]**. Decision 5 has to carry that.
- **The old conventions live on the edge host:** `user.ingress.*` instead of offers, devices carried in a profile, one shared bridge, secrets in plain environment keys, `tink deploy` instead of plan and apply **[ssh]**. None of it is wrong for what it is. It is what the recipe has to be able to replace, and what `tink export` has to be able to read.
- **tron's own notes call the edge host "the VPS"** **[tron]**. The Rust VPS is a second VPS. The docs would be clearer if the grounds had names (tron, edge, rust) and "the VPS" was retired.

## 3. The gate: what gets built

1. **A capability is built when a named ground needs it for something that ground does now or next.** "It might be useful" goes to the parking lot.
2. **Every capability names its ground and an exit test that runs there.** No ground, no exit test: the item is a design note, not a deliverable.
3. **Going generic takes a second ground.** A new kind, a verb the user must learn, or a recipe field is built for one ground only as the smallest thing that works (a printed unit, a hand-written stack, a doc). It becomes a feature when a second ground needs the same thing, or when the author says why one is enough.
4. **Workload files stay with the workload.** The Rust stack, the RCON script, the Nightscout stack and a Home Assistant config are fixtures that tink's docs point to. Tink takes only what generalises.
5. **Evidence is measured on a ground, with the version written down.** A design resting on one measurement says so ([section 7](#7-risks-and-unknowns)).

How the capabilities in section 5 stand against it:

| Capability | Ground that needs it now | Second ground | Verdict |
|---|---|---|---|
| Release | all | n/a | Build |
| Prove the helper on a clean host | Rust VPS | edge, if it moves | Build (it is a test, not a feature) |
| UDP through a `nat` proxy | Rust VPS | none | Measure only. Add a check if it fails |
| Stop that saves: the unit | Rust VPS | none | Build as a hand-written unit |
| Stop that saves: `stop:` and a verb | Rust VPS | tron and edge databases **[inferred]** | Build after the unit has run a real reboot and an image update |
| Apply the join renderings | tron | edge | Build for tron first |
| Platform recipe | tron, Rust VPS | edge | Build, role by role, with a fixture per ground that has the role |
| Ownership stamps, phase 1 | Rust VPS (fresh) | tron and edge adopt later | Build phase 1 only |
| Minimal watching | all | n/a | Build the minimum |
| Narrow helper credential | tron | Rust VPS | Build in parallel, not on the critical path |
| Verify helper, `network-forward`, profile defaults, `access` | none | none | Parking lot |

## 4. Designs that should change shape

1. **Release before more design (S).** All the helper work since 2026-10-07 is unreleased, and tron's helper is on `v0.1.4` **[tron]**. Cut a release (`v0.2.0`). Add a release step to the definition of done for any feature the helper runs. *Evidence:* `git tag`; helper.md "With no flag, a release build installs `ghcr:minihci/tink-helper:<its own version>`".

2. **Merge the two ingress mechanisms: keep the writer, replace the discovery.** `internal/ingress` already has what applying `join`'s Caddy rendering needs: writes through the file API, writes before removals, one reload, and a failed read treated as an error **[code: ingress.go, helper.md]**. What is wrong with it is the discovery (`user.tink.ingress.*`, by instance name) and the template (a site block per domain, no sign-on) **[code: render.go]**. Recommendation: `join apply caddy` reuses the ingress writer for the `handle` fragments, aimed at an include directory in the platform Caddyfile. Then `tink ingress` is frozen, and deleted once `incus.xlii.co` moves or is retired. *Evidence:* [platform-recipe-accounting.md](platform-recipe-accounting.md) part 2, item 2, already says "pick one". The edge host runs the old one: `user.ingress.*` on the Nightscout instances and an `ingress` instance whose routes come from a `routes` volume **[ssh]**.

3. **Freeze `tink deploy`; do not use it on the VPS.** It is a second definition of "the platform" (Caddy, Authelia, Incus UI). It deletes and relaunches three instances on every run, replaces the server config whole, and shells out **[docs: README, accounting item 1]**. All of that is the opposite of plan and apply. Keep it alive for the edge host, which was deployed with it **[ssh]**, and add nothing. Delete it when the recipe covers that host, or when the host is retired (decision 5). Merge or redo `retire-daemon-install` at the same time (S).

4. **Graceful stop: build it smaller, and in a different order.** The design ([PR #104](https://github.com/minihci/tink/pull/104)) proposes a field, two verbs (`tink stop` and `tink graceful-stop --all`), use in apply, and a host-unit installer. Recommended shape:
   - **First, with no tink code:** a hand-written host unit, `Requires=` and `After=incus-startup.service`, whose `ExecStop` runs `incus exec rust -- perl /rcon.pl quit` and waits. This is exactly what was measured. It protects the world at the next reboot.
   - **Then one verb** (`tink stop [INSTANCE...|--all]`) and the `stop:` field. **Use them in tink's own stops first.** `on_image_change: rebuild` runs pull, stop, rebuild, start, and `restart: true` restarts the instance **[code: rebuild.go, apply.go]**. Today both kill Rust without a save (for the restart, **[inferred]** from the stop measurement: a restart stops the instance the same way). That matters more than the host hook, because tink does it every time.
   - **Measure `server.save` followed by a plain stop** before adopting `quit` **[inferred]**. `quit` leaves the instance stopped only with `boot.autorestart: "false"` [graceful-stop.md], so a crashed server would not come back. A save followed by a stop would let autorestart stay on. Unmeasured.
   - Print the unit rather than install it, as `daemon install` does. Then there is no `tink host` command to design.

5. **The platform recipe: build it role by role from the roles the grounds share, and take the fixtures from the live hosts.** The preconditions in [platform-recipe.md](platform-recipe.md#what-has-to-be-true-first) (split generated content, first-apply steps, ownership, a helper version policy, watching, role contracts, pins) are what tron's five role helpers need. The first draft of this plan proposed a v0 with only the daemon helper, on the Rust VPS. That is too thin: an expander shaped by one trivial role will have the wrong shape for the second. The grounds share more than that: **ingress and sign-on** (tron and the edge host), the mesh control plane (the edge host, serving tron), and the daemon helper (all three, eventually). Recommendation:
   - The recipe is an **expander at load time**: a `kind: site` document expands into ordinary resources that the normal `plan` converges. `tink platform render` prints the expansion, and that printout is the eject. Do not build a second plan path.
   - **Stage 0, before any expander:** write each ground's platform stack by hand as plain YAML. For the edge host, start from `tink export` **[inferred]** and bring it to plan-and-apply shape. These are the golden fixtures.
   - **Then add roles one at a time,** starting with the ones two grounds share (ingress, sign-on). A role earns its place when its fixtures on two grounds both plan "no changes" against the live host.
   - Keep tron's hand-written `platform.yaml` as the oracle until the recipe reproduces it.

   *Evidence:* the accounting found about fifteen facts and no site literals in tink's code. The traps section already warns about growing a templating language.

6. **Park profile-carried defaults and the move to expanded config.** A recipe written in Go can emit the repeated helper boilerplate itself. Moving `plan`, `join` and `export` to expanded config breaks a rule `join` relies on ([join.md](join.md#from-the-server)), and profile edits need a new disruption guard. That is large and buys nothing the recipe cannot do. App stacks still repeat the boilerplate, which is a smaller pain. *Evidence:* [platform-recipe.md](platform-recipe.md#what-a-profile-can-hold-as-a-default), where a device does not merge.

7. **Cut the verify helper for now.** The scheduled-verify design adds a second helper, a ledger, eight rule groups and a role marker for helpers. Its own alternative (a person runs `backup verify --check-project`, and `plan` warns when a check is overdue) is what exists now. That suffices for two sites with a handful of volumes. Revisit once watching exists and checks are being forgotten. *Evidence:* `docs/scheduled-verify-design.md` §1 "ALTERNATIVE"; `plan` already warns when a check is stale **[docs: volume-backup.md]**.

8. **Cut `kind: network-forward` from the roadmap.** Proxy devices with `nat` are measured, tink passes them through, and #103 checks them. The forward's one advantage is that it does not need a device-edit right. That matters only if the daemon helper edits instances, and it is designed never to. *Evidence:* [public-exposure.md](public-exposure.md) cautions.

9. **Ownership stamps: split phase 1 from the rest, and do it before the VPS fills up.** Stamping on create is cheap on a fresh site and adds no adoption churn there. Report and prune (phases 2-3) wait until the recipe needs to drop or rename a helper. *Evidence:* the design's own phases, and its first-apply churn section.

10. **Do not let the credential work block the daemon loop.** The daemon helper already holds a certificate with the reach of root and makes copies with it. Letting it also run `join apply dns --from-server` adds two file reads and one write, plus list calls **[docs: join.md]**. Its reach does not grow. Narrowing the certificate (decision 2(a)) is a separate, parallel security track, and it is complicated on tron by volumes in `default` **[tron: W3.4]**.

11. **Settle the boundary statement.** [helper.md](helper.md) says a restricted certificate is no boundary. [what-tink-is.md](what-tink-is.md), [design-principles.md](design-principles.md) pattern 8 and `authz-lab` say it is one inside a restricted project. Correct helper.md (S, docs only).

12. **Close PR #16** and move whatever is still true in `helper-design.md` into [helper.md](helper.md). Today the user page points at an unmerged design that is two weeks behind what was built.

13. **Keep a game server off the helper's bridge.** On the edge host every instance shares one bridge **[ssh]**. The Rust VPS would repeat that if the daemon helper's API listens on `incusbr0` and the game server, an internet-facing process that loads third-party Oxide plugins, sits on the same bridge: it could reach the API port. The join design already puts apps on bridges of their own behind an ACL ([offer-grant-join-design.md](offer-grant-join-design.md) D9). Do the same here: one bridge for the platform project, another for the app project. This is a use of capabilities tink already has, and it belongs in the Rust stack and the recipe's defaults, not in new code.


## 5. Roadmap: by capability

Capabilities come first. A ground's own work is in the table at the end, and it is listed to show the order. **K0** and the grounds' first steps form the critical path to "a stateful app runs on a clean host under tink". **K5**, the recipe, is the largest piece and runs along the side of the others until it has a second role.

```
K0 release ──> K3 helper on a clean host ──> [rust stack, offsite copies] ──> K2a stop unit ──> K2b stop: in tink
      │                  K1 UDP via nat (measure, on the Rust VPS, before its cut-over)
      ├──> K4 join apply path (tron, then edge) ──> K9 retire the old mechanisms
      └──> K5 recipe: stage 0 fixtures ──> expander + first shared roles ──> more roles ──> tron through it
K6 ownership phase 1 (before K5's expander) · K7 watching · K8 credential narrowing (all parallel)
```

### K0: release and hygiene (S)

- **Needed by.** tron (its helper is on `v0.1.4`), the Rust VPS (a clean helper install), every later helper feature.
- **Deliverables.** First fix the stale docs in section 1 (docs only, S). Then tag `v0.2.0` and check that the image pulls without a login [helper.md]. Close PR #16, delete the leftover branches, and merge or close the two design notes.
- **Depends on.** Nothing. **Unblocks.** K3, K4.
- **Exit test.** `tink version` reports `v0.2.0`, and `tink helper install` with no flag works on a scratch server.

### K1: UDP through a `nat` proxy (S, a measurement)

- **Needed by.** the Rust VPS (28015/udp). **Gate.** One ground, so a measurement and a note, not a feature.
- **Deliverables.** Measure a UDP `nat` proxy on the Rust VPS (the rule, the client address, a real client joining). Record it in [public-exposure.md](public-exposure.md). Add a plan check only if the measurement finds a trap.
- **Depends on.** Nothing. **Unblocks.** The Rust stack's cut-over.
- **Exit test.** A client outside the host connects over UDP and the server sees its real address.

### K2: a stop that saves (S, then M)

- **Needed by.** the Rust VPS now; the databases on tron and the edge host probably **[inferred, unmeasured for them]**.
- **K2a (S): the unit.** A hand-written host unit, `Requires=` and `After=incus-startup.service`, whose `ExecStop` runs the app's quit command through `incus exec` and waits. This is what [graceful-stop.md](graceful-stop.md) measured. Its source is kept with the workload. *Exit:* a real `systemctl reboot`, not only `systemctl stop incus-startup.service` as measured: the log shows the save before the instance stops, and the server is back after boot.
- **K2b (M): `stop:` in tink.** The `stop:` field and its `user.tink.stop` stamp, one verb (`tink stop [INSTANCE...|--all]`), and use in tink's own stops: `on_image_change: rebuild` and `restart: true` **[code: rebuild.go, apply.go]**, which kill the app without a save today. Print the unit rather than install it, as `daemon install` does. Measure `server.save` against `quit` first. *Gate:* start only after K2a has run through a reboot and an image update, and a second stateful workload confirms the need. *Exit:* an image change applied with `rebuild` leaves a fresh save from before the stop.
- **Depends on.** the workload's stack (K2a), K2a and PR #104 (K2b). **Unblocks.** Image updates with `on_image_change: rebuild` for anything stateful.

### K3: the helper on a clean host (S)

- **Needed by.** the Rust VPS; it is also the first host reboot with a helper, which `helper.md` says was never tried.
- **Deliverables.** A `tink` project with a healthy helper installed from a stack (`kind: helper`), `core.https_address` on the bridge address with a port the legacy Caddy does not hold **[session]**, and the restore-check project from [volume-backup.md](volume-backup.md#running-the-check-in-a-project-of-its-own).
- **Depends on.** K0. **Unblocks.** Offsite copies, and K5's stage 0 on the Rust VPS.
- **Exit test.** `tink helper status --check` exits 0, a second `plan` says no changes, and the same holds after a deliberate host reboot.

### K4: the join apply path (M each)

- **Needed by.** tron (the dns half is run by hand today **[tron: LOG W3.2]**); the edge host, if it moves to offers.
- **Deliverables, in order.**
  - A daemon worker that runs `join apply dns --from-server`, with a field in the status document. First run it by hand against tron's real CoreDNS, which has never been done.
  - `join apply caddy` on the ingress writer (section 4, item 2), aimed at an include directory in the platform Caddyfile. Then Authelia's rules; confirm that Authelia merges two files, which [offer-grant-join-design.md](offer-grant-join-design.md) marks **[to confirm]**.
  - The ACL rendering, which needs the derived-rules marker in `network-acl` (M). The mesh records stay hand-applied on the control-plane host.
- **Depends on.** K0. **Unblocks.** K5's ingress and sign-on roles, K9.
- **Exit tests.** A publish added to a stack is answered within one interval with no hand step; tron's canary routes and rules are no longer in `platform.yaml`.

### K5: the platform recipe (L)

- **Needed by.** tron, the edge host, the Rust VPS. **Gate.** Three grounds, but built role by role (section 4, item 5).
- **Stage 0 (S to M per ground).** Each ground's platform stack as plain YAML that plans "no changes" against the live host. For tron this exists. For the edge host, start from `tink export`. For the Rust VPS it is K3's stack plus its bridges.
- **Stage 1 (M).** The `kind: site` expander and `tink platform render`, with pins shipped with the recipe, a `plan` note when the helper runs older tink than the recipe needs (a report only), and the first shared roles: ingress and sign-on.
- **Stage 2 (L in total, M per role).** Dns, identity, the mesh control plane, the monitor. For each: its contract written as data (stamps, files, ports), its default image pin, and its restore check. Secrets in files (phase 2 of secrets) when identity or OIDC needs them.
- **Stage 3 (M).** tron's `platform.yaml` is generated, not hand-written ([platform-recipe.md](platform-recipe.md#proving-it) stage 3).
- **Depends on.** K0, K3, K4 (for the split), K6. **Unblocks.** K9.
- **Exit tests.** Each role's fixtures plan "no changes" against the live hosts that have it; `render(tron facts)` plans "no changes" against tron; deleting a ground's hand-written file loses nothing.

### K6: ownership stamps, phase 1 (M)

- **Needed by.** the Rust VPS (stamp from birth), and the recipe before it renames or drops a helper.
- **Deliverables.** Phase 1 of the ownership design: stamp on create, `--adopt`, and the validate, export, extras and guard changes. Merge the design note first. Prune and report (phases 2 and 3) wait.
- **Depends on.** Nothing. **Unblocks.** K5's stage 1 upgrades, prune later. It is not on the critical path to a workload running.
- **Exit test.** The design's own: 30 real stack files plan the same apart from one line, `--adopt` restarts nothing, and the Rust VPS's objects carry stamps from birth.

### K7: minimal watching (M)

- **Needed by.** every ground, before it is called reliable.
- **Deliverables.** The heartbeat and ntfy half of tron's watching design. Running `tink helper status --check` from the helper or from cron counts as the first probe.
- **Exit test.** Stopping a helper, or a host, alerts the phone within the stated window.

### K8: narrow the helper credential (M to L)

- **Needed by.** tron, where the helper's reach is widest. **Gate.** Do not let it block K4: the helper already holds root-equivalent reach, so the dns loop adds none (section 4, item 10).
- **Deliverables.** Start on the Rust VPS, where every volume can live in a restricted project, then tron, where three policy volumes are in `default` and cannot be restricted.

### K9: retire the old mechanisms (S to M)

- **Needed by.** nothing urgent. It follows the edge decision.
- **Deliverables.** Delete `tink ingress` and `tink deploy` once the edge host moves or is retired (decision 5). Merge or redo `retire-daemon-install`. Remove the `tink mongo snapshot` stub (section 8).
- **Depends on.** K4, K5, decision 5.

### Ground-side work (not tink deliverables)

This is what the grounds do with the capabilities. It lives in each ground's own repository, and it is here only to show the order.

| Ground | Work | Waits for |
|---|---|---|
| Rust VPS | A project `tink` and a project `rust`, each on its own bridge | K3 |
| Rust VPS | The Rust stack: world and Oxide volumes with `backup:` blocks, the image pinned by digest with `on_image_change: report`, a static address and NIC `name:`, NAT proxies for 28015/udp and 28082/tcp, the RCON password as `${secret:}`, the RCON client as a `kind: file` | K1 for the cut-over; decisions 2 and 3 |
| Rust VPS | Migrate the legacy world, or start fresh (decision 2); then retire the Podman setup and decide about its 42 GB **[session]** | the Rust stack |
| Rust VPS | Offsite copies to the target chosen in decision 1, one `backup verify --check-project` that also checks the `.sav` file is not empty | K3, decision 1 |
| Rust VPS | The stop unit, kept in the Rust stack's repository | K2a |
| tron | `tink helper upgrade` to the release. Keep the old status shape, because moving to the status volume means re-enrolling with a new token **[tron: STATUS item 7]** | K0 |
| tron | Switch the dns loop on; then the Caddy and sign-on renderings | K4 |
| Edge host | Decide: freeze, move onto the recipe, or retire (decision 5). Meanwhile nothing new goes on `tink deploy` or `tink ingress` | decision 5 |

**Smallest sequences:**

- **A stateful app on a clean host, backed up, with a graceful stop (Rust):** K0, K3, the Rust stack with K1 before its cut-over, K2a, offsite copies. Five steps, about one M and four S, plus decision 1. K2b replaces the hand-written unit and makes image updates safe.
- **The platform as a recipe, on a ground that is not tron:** K0, K3, K5 stage 0 for the Rust VPS and the edge host, then K5 stage 1 with ingress and sign-on, where the edge host is the second fixture and tron the oracle.

## 6. Decisions the owner must make

1. **Where do the Rust copies go?**
   - Options: (a) the edge host (`incus.xlii.co`), tron's `offsite` target, which already holds 48 copy volumes in `tink-backup` and has room (400 GB pool, 7.7 GB used) **[ssh]**; (b) tron, reached over the mesh, which first needs a gateway on the Rust VPS; (c) a same-host pool only, with the 3-2-1 warning accepted.
   - Recommendation: (a) now, (b) later if the edge host is retired.
   - Blocks: the offsite copies (ground-side work).
2. **Migrate the legacy world or start fresh?**
   - Options: copy the Podman volumes (about 555 MB **[session]**) into the new Incus volumes by hand once; or wait for the next forced wipe.
   - Recommendation: migrate only if a wipe is not close. It is a one-off, so it is not a tink feature.
   - Blocks: the Rust stack's cut-over.
3. **Is RCON (28016) public?**
   - Options: public as in rustpod; not forwarded at all (RCON through `incus exec` and the Perl client); only over the mesh.
   - Recommendation: not forwarded. The graceful stop already uses RCON from inside the instance.
   - Blocks: the Rust stack.
4. **Does the Rust VPS's platform stack include ingress and sign-on now?**
   - Options: helper only; add ingress with a `public` leg for future web apps (ntfy, the watching heartbeat).
   - Recommendation: helper only, until K7 or a web app needs ingress. The edge host already covers the public web case.
   - Blocks: the scope of K5's stage 0 for the Rust VPS.
5. **What happens to the edge host, `tink deploy` and `tink ingress`?**
   - The host is live, serves the mesh control plane and sign-on, and is tron's offsite target **[ssh]**.
   - Options: keep it frozen on its current build; move it onto the recipe (K5, with its stage 0 fixture); retire it once the copies and the control plane have another home.
   - Recommendation: freeze now (nothing new on `deploy` or `ingress`), take its stage 0 fixture from `tink export`, and decide after K5's first shared roles.
   - Blocks: K9, and the choice of K5's second fixture.
6. **Copied template or shipped recipe?**
   - The docs say both (section 1).
   - Recommendation: shipped recipe with eject, as [platform-recipe.md](platform-recipe.md) argues. Then edit what-tink-is.md and the glossary.
   - Blocks: K5 stage 1.
7. **Is the recipe a load-time expander, or a separate `tink platform plan` path?**
   - Recommendation: the expander, with `render` as the eject (section 4, item 5).
   - Blocks: K5 stage 1.
8. **`quit`, or `server.save` then stop, for Rust?**
   - Decide after one measurement.
   - Blocks: K2b's `stop:` field shape, and whether autorestart stays on.
9. **Which ground leads?**
   - Options: the Rust VPS (clean, small, answers the stop and offsite questions); tron (the real spine, and the join apply path); both in parallel.
   - Recommendation: the Rust VPS leads on K1 to K3, and K0 and K4 run for tron alongside. The Rust VPS is the ground where mistakes cost least. Earlier in the sessions the author called the Rust work secondary, then returned to it, so this should be confirmed.
   - Blocks: nothing hard. It sets the order of attention.


## 7. Risks and unknowns

| Risk | Rests on | What to do |
|---|---|---|
| The 1-second stop window and the absent stop setting are Incus 7.5.1 behaviour | one day of measurement on one host [graceful-stop.md] | re-measure at every Incus upgrade, like the authorization scriptlet. A future `oci` stop setting could make the hook unnecessary |
| The host-unit ordering (`Requires=` with `After=`) was measured with `systemctl stop incus-startup.service`, not a real shutdown | one run | K2a's exit test is a real reboot |
| A UDP proxy with `nat` | never measured; #103 measured TCP | K1 |
| The authorization scriptlet's input changes between Incus versions | #93; README says so | already a documented re-test; a VPS with no deploy does not use it |
| The VPS firewall redirects were removed live; not tested across a reboot | [session] | check after K3's reboot that outbound HTTPS still works from a container |
| The helper has never been through a host reboot | helper.md | K3's exit test |
| The VPS has a public IP and no host firewall; the API listens on the bridge address only | [session], helper.md | keep the listener off the wildcard. Every container on `incusbr0`, the Rust server included, can reach the API port. Consider the NIC egress rule from helper.md, whose effect on traffic to the host is a **[hypothesis]** |
| The Rust image tag `latest-oxide` floats, and how the game updates inside the image is unknown **[to confirm]** | rustpod README | pin by digest; find out whether a new game build means a new image or an in-place update at start |
| Recipe scope creep into a templating language | [platform-recipe.md](platform-recipe.md) traps | a field only when two grounds need it; golden fixtures per ground |
| Authelia merging a static and a generated file | **[to confirm]** in the offer-grant design | lab it before K4's Caddy and sign-on step |
| `join --from-server` reads stored config; a role stamp in a profile is invisible | [join.md](join.md#from-the-server) | the reason to park profile defaults (section 4, item 6) |
| The tron helper is on `v0.1.4`, and moving it to the status volume needs a new token from `vps` | [tron: STATUS] | the tron helper upgrade defers the move |
| Single host for everything on tron, and a single VPS for Rust | [tron: STATUS] | accepted; K7 at least makes outages visible |
| Several "decided" claims were decided in the author's own working sessions and are recorded only in docs | design notes | where one blocks a phase, it is listed in section 6 |
| The edge host is one machine for the mesh control plane, sign-on, tron's offsite copies and the Nightscout stack, on Incus 7.4 and a 2026-10-09 tink build | [ssh] | Decision 5. Until then, nothing new goes on it, and K7 should watch it |
| The edge host's ingress uses userspace proxies, so the apps behind it see 127.0.0.1 as the client | [ssh]; [public-exposure.md](public-exposure.md) | Recorded as an observation. Moving it to `nat` is part of putting it on the recipe, not a fix to make now |
| A database password sits in plain `environment.*` on the Nightscout instances | [ssh] | Move it to `${secret:}` when that stack is next applied with a current tink. Do not copy the pattern |
| The first draft of this plan knew of two grounds; a third was found by looking | [ssh] | Ask whether there are other hosts or shapes the plan does not know about before section 5 is settled |


## 8. Parking lot

| Idea | Why not now |
|---|---|
| Verify helper and ledger (scheduled restore checks) | a person runs `verify`, and `plan` warns when one is overdue; no ground needs a second credential yet |
| `kind: network-forward` | proxy devices work and are checked; the advantage it has does not apply |
| Profile-carried defaults; expanded config in plan, join and export | the recipe emits the boilerplate; the change is large and breaks a `join` rule |
| `kind: access`, OIDC attributes on offers, per-name auth strength, `accept:` | no ground needs them yet; Home Assistant on tron will be the first |
| Listener and bypass check | valuable for Home Assistant's discovery leg; after K4 |
| Ownership phases 2-3 (report, prune) | needed when the recipe first drops or renames a helper |
| Authoritative apply, layers 2-3 | ownership phase 1 covers the immediate need |
| Project-level `user.tink.site.*` stamps | only for daemon-side drift checks of the recipe, which come after K5's stage 3 |
| Mesh gateway and control plane as recipe roles | the control plane runs on the edge host; move it only with decision 5 |
| Declaring people through LLDAP's bootstrap | needs secrets in files; the directory UI works |
| Restic engine | Incus-native copies cover every ground |
| Cross-stack references | `--from-server` stamps cover the case that mattered |
| `tink mongo snapshot` | a stub with no design. Under the "no deprecation for undeployed features" rule in CLAUDE.md, remove the command |
| A name for the remote-access feature | nothing to name until the mesh is a recipe role |
| `tink.exe` live testing | no site depends on it |
| Anything that no proving ground needs yet | The gate (section 3). A design note is enough |

