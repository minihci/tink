# Roadmap: where tink stands, what should change shape, and what to build next

**Status: a draft for the author to edit, 2026-10-17, written by an outside reviewer at `a7ec084` (main after #103).** Nothing here is decided. Claims are marked **[code]** (read in this tree, and `go build`, `go vet` and `go test ./...` pass at `a7ec084`), **[pr]** (the merged pull request), **[docs]** (what a doc in this repository says, not re-checked), **[tron]** (the author's tron repository, `~/my/projects/tron`, read-only), **[session]** (the author's own notes from the 2026-10-17 VPS sessions, not in any repository) and **[inferred]** (my reasoning; check it). Sizes: **S** (a small PR), **M** (a few PRs, or one with a live lab), **L** (a design plus several PRs and labs). No dates.

## In one page

1. The binary is in better shape than the docs suggest. Resolver, backups, secrets, `--remote`, the daemon helper and `join` are built and have been run live. Most of what feels "in flux" is design that has not been built, plus about a dozen docs whose status lines are out of date.
2. **Nothing merged since 2026-10-07 has been released.** `v0.1.4` is the newest tag, and 91 commits sit on top of it: `join`, `kind: publish`, `kind: helper`, the status volume, `--api-via network`, `--check-project`. A release build installs the helper image of its own version, so neither real site can run the new helper work without `--binary` until there is a release **[code, git tags]**.
3. **The Rust server needs almost none of the platform stack.** It is not a web app, so it needs no `join`, ingress, sign-on or dns. It needs a plain stack (built), NAT proxy devices (built, measured for TCP only), scheduled snapshots (built), an offsite copy (built, but it needs a reachable target), and a graceful stop (designed, not built). The shortest path to "Rust on the VPS under tink" is mostly YAML and one decision about where the copies go.
4. The graceful stop can ship first as a hand-written host unit. The unit's behaviour has already been measured. The tink feature (`stop:` plus a verb) comes next, and its real value is in tink's own stops (`on_image_change: rebuild`, `restart: true`), which kill Rust without a save today.
5. The platform recipe is the right direction, but its list of preconditions comes from tron's full spine. For the VPS the first recipe can be small: the daemon helper, its project and the restore-check project. Grow it one role at a time, each with a golden fixture per real site.
6. Two pairs of mechanisms overlap and should be merged before the recipe depends on them. **Ingress:** `tink ingress reconcile` and `join`'s Caddy rendering. Keep the old writer, replace the old discovery. **"The platform":** `tink deploy` and the recipe. Freeze deploy, and do not use it on the VPS.
7. Some designs are over-built for two sites. Park the verify helper with its ledger, `kind: network-forward`, profile-carried defaults (with the move to expanded config) and `access` documents until a real app forces them.
8. Ownership stamps should be written from birth on the VPS, which is a fresh site. Phase 1 of that design (stamp on create) is cheap there and spares the VPS a later adoption pass. Prune can wait.
9. Watching is the precondition "the user does not maintain it" quietly rests on, and nothing is built. Its minimum (a heartbeat out, a phone alert) is parallel work, and it should land before either site is called reliable.
10. The docs disagree about two decisions that matter. Is the platform stack copied and owned, or shipped? Is a restricted certificate a boundary? Settle both in the docs before building on either.

## 1. Where things stand

### Built and exercised live

| Capability | Evidence |
|---|---|
| `tink plan` / `plan apply` (graph, levels, BLOCKED, image drift, extras) | `internal/resolve` **[code]**; Nextcloud, Nightscout, Immich, Mosquitto and Matter stacks, the tron platform stack (second plan quiet, 2026-10-13) **[docs, tron]** |
| `tink run`, `tink export` | README; [export.md](export.md) "Verified live" **[docs]** |
| `tink deploy` | an Incus 7.4 host and `incus.xlii.co` **[docs: README]**. Not used on tron or the new VPS |
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
| NAT proxy warnings (static address, overlapping listeners) | #103; one read-only plan on the VPS **[pr]** |
| OCI NIC `name:` warning | #77; checked against the lab host **[docs]** |
| Authorization scriptlet under deploy's routing | #93; Incus 7.5.1 only **[pr, docs: README]** |
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

**Leftover branches** that are merged in substance or abandoned: `public-exposure-checks` (#103), `claude/integration-abcf` (scratch), `docs/helpers-and-platform-stack` and `claude/docs-helper-does-not-verify` (their content is on main), `opinions-spike`, `headscale-stack` (78 behind). **[inferred]** None of these is worth rebasing except `retire-daemon-install` and the two design notes (ownership stamps, scheduled verify).

## 2. The goal and the shape of the whole

**What tink is aiming to be** (my reading). Tink is a declarative layer over Incus for one person's sites: the plan and apply loop for what exists, opinions that make good practice cheap (verified backups, least privilege, refusals instead of guesses), and the small set of long-running services a self-hosted site needs. It should not become a platform people maintain.

**The binary** does every one-shot thing: plan and apply, run and export, backup run, restore and verify, secret, join, and the helper commands. It holds no state. **The platform stack** is the set of long-running instances that provide the standing version of the binary's work. The **daemon helper** (tink itself) tends. The **role helpers** (dns, ingress, sso, identity, mesh, and later monitor) serve and hold no credential. The rule is "derived state heals, declared state reports" [helper.md](helper.md#what-the-helper-is-for). Today the daemon helper does exactly one thing for real: it makes backup copies.

**The direction**, as I read it. Ship the platform stack as a recipe: tink owns the containers and the wiring, the user writes about fifteen site facts, and policy stays in offers and publishes. Both real sites are golden fixtures, not templates anyone maintains ([platform-recipe.md](platform-recipe.md#proving-it)). I agree with it, with one change of order (section 3, item 5).

**Three machines, not two.** tron's offsite copies go to a remote called `vps`, and its mesh control plane (`hs.xlii.co`) is hand-run on "the VPS" **[tron: platform.yaml, STATUS.md]**. Those existed before 2026-10-13, and rust.wyomarus.com was first set up on 2026-10-17. So the `vps` remote is a different machine from rust.wyomarus.com **[inferred, high confidence]**. It may be `incus.xlii.co`, the host the README calls the reference deployment, which runs `tink deploy` and the old ingress daemon **[inferred, low confidence]**. Either way, the roadmap has to keep that third machine working without investing in it.

**"Done enough to rely on":**

- **VPS (rust.wyomarus.com).** The Rust server runs from a stack in project `rust`; `plan` says no changes. A host reboot saves the world first and brings the server back. Snapshots are taken on a schedule, a copy leaves the machine, and one restore has been verified. An image update saves before it stops. Something outside the VPS tells the phone when the helper or the server is down. A `tink` project holds the daemon helper from a released image.
- **tron.** The spine keeps working as it does now. The daemon helper runs a current release. The dns records are kept right on a timer, not by hand. The routes and sign-on rules for published names stop being hand-written. Something tells the phone when tron goes quiet.

## 3. Designs that should change shape

1. **Release before more design (S).** All the helper work since 2026-10-07 is unreleased, and tron's helper is on `v0.1.4` **[tron]**. Cut a release (`v0.2.0`). Add a release step to the definition of done for any feature the helper runs. *Evidence:* `git tag`; helper.md "With no flag, a release build installs `ghcr:minihci/tink-helper:<its own version>`".

2. **Merge the two ingress mechanisms: keep the writer, replace the discovery.** `internal/ingress` already has what applying `join`'s Caddy rendering needs: writes through the file API, writes before removals, one reload, and a failed read treated as an error **[code: ingress.go, helper.md]**. What is wrong with it is the discovery (`user.tink.ingress.*`, by instance name) and the template (a site block per domain, no sign-on) **[code: render.go]**. Recommendation: `join apply caddy` reuses the ingress writer for the `handle` fragments, aimed at an include directory in the platform Caddyfile. Then `tink ingress` is frozen, and deleted once `incus.xlii.co` moves or is retired. *Evidence:* [platform-recipe-accounting.md](platform-recipe-accounting.md) part 2, item 2, already says "pick one".

3. **Freeze `tink deploy`; do not use it on the VPS.** It is a second definition of "the platform" (Caddy, Authelia, Incus UI). It deletes and relaunches three instances on every run, replaces the server config whole, and shells out **[docs: README, accounting item 1]**. All of that is the opposite of plan and apply. Keep it alive for the third machine and add nothing. Delete it when the recipe covers that machine, or when the machine is retired (decision 5). Merge or redo `retire-daemon-install` at the same time (S).

4. **Graceful stop: build it smaller, and in a different order.** The design ([PR #104](https://github.com/minihci/tink/pull/104)) proposes a field, two verbs (`tink stop` and `tink graceful-stop --all`), use in apply, and a host-unit installer. Recommended shape:
   - **First, with no tink code:** a hand-written host unit, `Requires=` and `After=incus-startup.service`, whose `ExecStop` runs `incus exec rust -- perl /rcon.pl quit` and waits. This is exactly what was measured. It protects the world at the next reboot.
   - **Then one verb** (`tink stop [INSTANCE...|--all]`) and the `stop:` field. **Use them in tink's own stops first.** `on_image_change: rebuild` runs pull, stop, rebuild, start, and `restart: true` restarts the instance **[code: rebuild.go, apply.go]**. Today both kill Rust without a save (for the restart, **[inferred]** from the stop measurement: a restart stops the instance the same way). That matters more than the host hook, because tink does it every time.
   - **Measure `server.save` followed by a plain stop** before adopting `quit` **[inferred]**. `quit` leaves the instance stopped only with `boot.autorestart: "false"` [graceful-stop.md], so a crashed server would not come back. A save followed by a stop would let autorestart stay on. Unmeasured.
   - Print the unit rather than install it, as `daemon install` does. Then there is no `tink host` command to design.

5. **The platform recipe: start from the VPS and grow it by role, rather than from tron's full spine.** The preconditions in [platform-recipe.md](platform-recipe.md#what-has-to-be-true-first) (split generated content, first-apply steps, ownership, a helper version policy, watching, role contracts, pins) are what tron's five role helpers need. A VPS recipe with only the daemon helper, its project and the restore-check project needs only "pins" and "a version policy". Recommendation:
   - The recipe is an **expander at load time**: a `kind: site` document expands into ordinary resources that the normal `plan` converges. `tink platform render` prints the expansion, and that printout is the eject. Do not build a second plan path.
   - Add roles one at a time. Each role carries a golden fixture per site that uses it, and the tron fixture must keep planning "no changes" against the live tron stack.
   - Keep tron's hand-written `platform.yaml` as the oracle until the recipe reproduces it.

   *Evidence:* the accounting found about fifteen facts and no site literals in tink's code. The traps section already warns about growing a templating language.

6. **Park profile-carried defaults and the move to expanded config.** A recipe written in Go can emit the repeated helper boilerplate itself. Moving `plan`, `join` and `export` to expanded config breaks a rule `join` relies on ([join.md](join.md#from-the-server)), and profile edits need a new disruption guard. That is large and buys nothing the recipe cannot do. App stacks still repeat the boilerplate, which is a smaller pain. *Evidence:* [platform-recipe.md](platform-recipe.md#what-a-profile-can-hold-as-a-default), where a device does not merge.

7. **Cut the verify helper for now.** The scheduled-verify design adds a second helper, a ledger, eight rule groups and a role marker for helpers. Its own alternative (a person runs `backup verify --check-project`, and `plan` warns when a check is overdue) is what exists now. That suffices for two sites with a handful of volumes. Revisit once watching exists and checks are being forgotten. *Evidence:* `docs/scheduled-verify-design.md` §1 "ALTERNATIVE"; `plan` already warns when a check is stale **[docs: volume-backup.md]**.

8. **Cut `kind: network-forward` from the roadmap.** Proxy devices with `nat` are measured, tink passes them through, and #103 checks them. The forward's one advantage is that it does not need a device-edit right. That matters only if the daemon helper edits instances, and it is designed never to. *Evidence:* [public-exposure.md](public-exposure.md) cautions.

9. **Ownership stamps: split phase 1 from the rest, and do it before the VPS fills up.** Stamping on create is cheap on a fresh site and adds no adoption churn there. Report and prune (phases 2-3) wait until the recipe needs to drop or rename a helper. *Evidence:* the design's own phases, and its first-apply churn section.

10. **Do not let the credential work block the daemon loop.** The daemon helper already holds a certificate with the reach of root and makes copies with it. Letting it also run `join apply dns --from-server` adds two file reads and one write, plus list calls **[docs: join.md]**. Its reach does not grow. Narrowing the certificate (decision 2(a)) is a separate, parallel security track, and it is complicated on tron by volumes in `default` **[tron: W3.4]**.

11. **Settle the boundary statement.** [helper.md](helper.md) says a restricted certificate is no boundary. [what-tink-is.md](what-tink-is.md), [design-principles.md](design-principles.md) pattern 8 and `authz-lab` say it is one inside a restricted project. Correct helper.md (S, docs only).

12. **Close PR #16** and move whatever is still true in `helper-design.md` into [helper.md](helper.md). Today the user page points at an unmerged design that is two weeks behind what was built.

## 4. Roadmap

Three tracks. **A** (Rust on the VPS) and **B** (the platform recipe, starting on the VPS) form the critical path. **C** (tron) and **D** (cross-cutting work) run in parallel and pick up the pieces A and B produce.

```
A0 release ──> A1 VPS helper ──> A2 Rust stack ──> A3 graceful stop (unit) ──> A4 backups out ──> A5 stop: in tink
      │                                                                              │
      └──> C1 tron helper upgrade ──> C2 dns on a timer ──> C3 caddy/sso applied      └──> D1 minimal watching (parallel; needed before "reliable")
B1 VPS platform by hand (from A1) ──> B2 ownership phase 1 ──> B3 recipe v0 (daemon helper) ──> B4 + ingress/sso roles ──> B5 tron through the recipe
```

### Phase A0: release (S)

- **Goal.** A helper image that has the status volume, `--api-via network`, `join` and `kind: helper`.
- **Deliverables.** First fix the stale docs in section 1 (docs only, S). Then tag `v0.2.0` and check that the image pulls without a login [helper.md].
- **Depends on.** Nothing. **Unblocks.** A1, C1.
- **Exit test.** `tink version` reports `v0.2.0`, and `tink helper install` with no flag works on a scratch server.

### Phase A1: the VPS gets its daemon helper (S)

- **Goal.** A `tink` project with a healthy helper, installed from a stack.
- **Deliverables.**
  - A host step: `core.https_address` on the bridge address with a port other than 8443, which the legacy Caddy holds **[session]**.
  - A small stack with `kind: helper`.
  - The restore-check project from [volume-backup.md](volume-backup.md#running-the-check-in-a-project-of-its-own).
- **Depends on.** A0. **Unblocks.** A2's copies, B1.
- **Exit test.** `tink helper status --check` exits 0, and a second `plan` says no changes. The first host reboot with the helper is done on purpose and recorded: helper.md says a reboot was never tried.

### Phase A2: the Rust app stack (M)

- **Goal.** The legacy server replaced by a tink stack in project `rust`.
- **Deliverables.**
  - Volumes for the world (identity) and Oxide data, with `backup:` blocks.
  - The instance pinned by digest, with `on_image_change: report`.
  - A static address on the bridge, and NIC `name:` set.
  - NAT proxy devices for 28015/udp and 28082/tcp. Decision 3 settles RCON on 28016.
  - The entrypoint with `${secret:}` for the RCON password, as measured **[session]**.
  - The RCON client as a `kind: file`.
  - Migrating or not migrating the legacy world: decision 2.
- **Depends on.** Nothing from A1 for the stack itself; copies need A1.
- **Unblocks.** A3, A4.
- **Exit test.** A client joins over the public address. `plan` says no changes, with no warning except a declared 3-2-1 shortfall. **Measure UDP through a `nat` proxy first:** #103 measured TCP only.

### Phase A3: graceful stop at host shutdown, by hand (S)

- **Goal.** A reboot does not lose the world.
- **Deliverables.** The hand-written unit from section 3, item 4, and its source kept in the Rust stack's repository.
- **Depends on.** A2. **Unblocks.** Calling the VPS reliable. A5 replaces it.
- **Exit test.** A real `systemctl reboot`, not only `systemctl stop incus-startup.service` as measured: the log shows `Saved N ents` before the instance stops, and the server is back after boot.

### Phase A4: backups leave the machine (S to M, depending on decision 1)

- **Goal.** 3-2-1 for the Rust volumes.
- **Deliverables.** A `kind: backup-target` with `location: offsite`, the helper given that remote (`tink helper remote add`), one `backup verify --check-project` with a check that the `.sav` file is not empty.
- **Depends on.** A1, A2, decision 1. **Unblocks.** "Backed up".
- **Exit test.** `tink helper status` shows no failing copy after a day. A restore into a scratch volume starts a server on the same map.

### Phase A5: `stop:` in tink (M)

- **Goal.** tink's own stops ask the app to save first.
- **Deliverables.**
  - The `stop:` field and its `user.tink.stop` stamp.
  - One verb.
  - Use in rebuild and in restart-on-apply.
  - The printed host unit, which replaces A3's.
  - Measure `server.save` against `quit` first.
- **Depends on.** A3 (the measurements), PR #104.
- **Unblocks.** Image updates for Rust with `on_image_change: rebuild`.
- **Exit test.** An image change applied with `rebuild` leaves a fresh save from before the stop, and a host reboot through the printed unit behaves as A3's did.

### Phase B1: the VPS platform stack, by hand (S)

- **Goal.** The second golden fixture exists as YAML.
- **Deliverables.** A1's stack, written as a platform stack: a project, a base profile, the helper, the restore-check project, and the backup targets. No ingress yet (decision 4).
- **Depends on.** A1. **Unblocks.** B3.
- **Exit test.** It plans "no changes" on the VPS and lives in a repository.

### Phase B2: ownership stamps, phase 1 (M)

- **Goal.** Every object tink creates names its stack.
- **Deliverables.** Phase 1 of the ownership design: stamp on create, `--adopt`, and the validate, export, extras and guard changes. Merge the design note first.
- **Depends on.** Nothing. **Unblocks.** B4 and B5 upgrades (renaming or dropping a helper), prune later.
- **Exit test.** The design's own: 30 real stack files plan the same apart from one line, `--adopt` restarts nothing, and the VPS's objects carry stamps from birth.

### Phase B3: recipe v0, the daemon helper role only (M)

- **Goal.** "Facts in, stack out" for the smallest real case.
- **Deliverables.**
  - The `kind: site` expander and `tink platform render`.
  - Pins shipped with the recipe.
  - A `plan` note when the helper runs an older tink than the recipe needs: the version-policy precondition, as a report only.
  - Golden fixtures for the VPS, and tron's helper block.
- **Depends on.** B1, A0. **Unblocks.** B4.
- **Exit test.** `render(VPS facts)` plans "no changes" against B1's live objects. Deleting B1's YAML loses nothing.

### Phase B4: recipe roles, one at a time: ingress, sso, dns, identity, mesh (L in total, M per role)

- **Goal.** The recipe covers what tron runs, plus the VPS's public-only shape.
- **Deliverables.**
  - For each role: its contract written as data (stamps, files, ports), its default image pin, and its restore check.
  - For ingress and sso: the generated-include split (Caddy include directory, Authelia's second file). Confirm that Authelia merges two files: [offer-grant-join-design.md](offer-grant-join-design.md) marks it **[to confirm]**.
  - First-apply secret generation. Secrets in files (phase 2 of secrets) when identity or OIDC needs them.
- **Depends on.** B3, C3 for the split. **Unblocks.** B5.
- **Exit test.** Each role's tron fixture plans "no changes" against live tron.

### Phase B5: tron runs through the recipe (M)

- **Goal.** tron's `platform.yaml` is generated, not hand-written.
- **Deliverables.** tron's facts file; stage 3 of [platform-recipe.md](platform-recipe.md#proving-it).
- **Depends on.** B4, B2.
- **Exit test.** `render(tron facts)` plans "no changes" against tron, and the hand-written file is retired.

### Track C: tron (parallel)

- **C1, helper upgrade (S).** `tink helper upgrade` to A0's release. Keep the old status shape, because moving to the status volume means re-enrolling and a new token from `vps` [tron: STATUS item 7]. *Exit:* `plan` reports the new version, and copies continue.
- **C2, dns on a timer (S to M).** A daemon worker that runs `join apply dns --from-server`, with a field in the status document. First run `join apply dns --from-server` by hand against tron's real CoreDNS, which has never been done. *Exit:* a publish added to a stack is answered within one interval with no hand step.
- **C3, caddy and sso applied (M).** Section 3, item 2: a Caddy include and `join apply caddy` on the ingress writer, then Authelia's rules. *Exit:* tron's canary routes and rules are no longer in `platform.yaml`. Then the ACL rendering, which needs the derived-rules marker in `network-acl` (M). The mesh records stay hand-applied on the control-plane host.

### Track D: cross-cutting (parallel)

- **D1, minimal watching (M).** The heartbeat and ntfy half of tron's watching design. Running `tink helper status --check` from the helper or from cron counts as the first probe. *Exit:* stopping the VPS helper, or tron, alerts the phone within the stated window. **This is needed before either site is called reliable.**
- **D2, branch and PR hygiene (S).** Close #16, delete the leftover branches, and merge or close the two design notes.
- **D3, narrow the helper credential (M to L).** Start on the VPS, where every volume can live in restricted projects, before tron.

### The smallest sequences asked for

- **Rust on the VPS under tink, backed up, with a graceful stop:** A0, then A1, then A2, then A3, then A4. Five steps, about one M and four S, plus decision 1. A5 replaces A3's hand-written unit with the tink feature and makes image updates safe.
- **The platform stack as a shipped recipe on the VPS:** A0, then A1, then B1, then B3. B2 is strongly advised before B3 but not strictly required. This gives a shipped recipe of one role. Adding ingress for a public web app on the VPS is one B4 step plus decision 4.

## 5. Decisions the owner must make

1. **Where do the Rust copies go?**
   - Options: (a) the existing `vps` remote (public, already a target for tron); (b) tron, reached over the mesh, which first needs a VPS gateway; (c) a same-host pool only, with the 3-2-1 warning accepted.
   - Recommendation: (a) now, (b) later if the third machine is retired.
   - Blocks: A4.
2. **Migrate the legacy world or start fresh?**
   - Options: copy the Podman volumes (about 555 MB **[session]**) into the new Incus volumes by hand once; or wait for the next forced wipe.
   - Recommendation: migrate only if a wipe is not close. It is a one-off, so it is not a tink feature.
   - Blocks: A2's cut-over.
3. **Is RCON (28016) public?**
   - Options: public as in rustpod; not forwarded at all (RCON through `incus exec` and the Perl client); only over the mesh.
   - Recommendation: not forwarded. The graceful stop already uses RCON from inside the instance.
   - Blocks: A2.
4. **Does the VPS platform stack include ingress and sign-on now?**
   - Options: helper only (B1 as written); add ingress with a `public` leg for future web apps (ntfy, the watching heartbeat).
   - Recommendation: helper only, until D1 or a web app needs ingress.
   - Blocks: the scope of B1 and B4.
5. **What happens to the third machine and `tink deploy`?**
   - Options: keep it frozen and keep deploy; move it onto the recipe (B4/B5 plus one more fixture); retire it once tron's copies and the control plane move to rust.wyomarus.com.
   - Recommendation: freeze now, and decide after B4.
   - Blocks: deleting deploy and `tink ingress`.
6. **Copied template or shipped recipe?**
   - The docs say both (section 1).
   - Recommendation: shipped recipe with eject, as [platform-recipe.md](platform-recipe.md) argues. Then edit what-tink-is.md and the glossary.
   - Blocks: B3.
7. **Is the recipe a load-time expander, or a separate `tink platform plan` path?**
   - Recommendation: the expander, with `render` as the eject (section 3, item 5).
   - Blocks: B3.
8. **`quit`, or `server.save` then stop, for Rust?**
   - Decide after one measurement.
   - Blocks: A5's `stop:` field shape, and whether autorestart stays on.

## 6. Risks and unknowns

| Risk | Rests on | What to do |
|---|---|---|
| The 1-second stop window and the absent stop setting are Incus 7.5.1 behaviour | one day of measurement on one host [graceful-stop.md] | re-measure at every Incus upgrade, like the authorization scriptlet. A future `oci` stop setting could make the hook unnecessary |
| The host-unit ordering (`Requires=` with `After=`) was measured with `systemctl stop incus-startup.service`, not a real shutdown | one run | A3's exit test is a real reboot |
| A UDP proxy with `nat` | never measured; #103 measured TCP | measure in A2 before the cut-over |
| The authorization scriptlet's input changes between Incus versions | #93; README says so | already a documented re-test; a VPS with no deploy does not use it |
| The VPS firewall redirects were removed live; not tested across a reboot | [session] | check after A1's reboot that outbound HTTPS still works from a container |
| The helper has never been through a host reboot | helper.md | A1's exit test |
| The VPS has a public IP and no host firewall; the API listens on the bridge address only | [session], helper.md | keep the listener off the wildcard. Every container on `incusbr0`, the Rust server included, can reach the API port. Consider the NIC egress rule from helper.md, whose effect on traffic to the host is a **[hypothesis]** |
| The Rust image tag `latest-oxide` floats, and how the game updates inside the image is unknown **[to confirm]** | rustpod README | pin by digest; find out whether a new game build means a new image or an in-place update at start |
| Recipe scope creep into a templating language | [platform-recipe.md](platform-recipe.md) traps | a field only when two real sites need it; golden fixtures per site |
| Authelia merging a static and a generated file | **[to confirm]** in the offer-grant design | lab it before C3 |
| `join --from-server` reads stored config; a role stamp in a profile is invisible | [join.md](join.md#from-the-server) | the reason to park profile defaults (section 3, item 6) |
| The tron helper is on `v0.1.4`, and moving it to the status volume needs a new token from `vps` | [tron: STATUS] | C1 upgrades in place and defers the move |
| Single host for everything on tron, and a single VPS for Rust | [tron: STATUS] | accepted; D1 at least makes outages visible |
| Several "decided" claims were decided in the author's own working sessions and are recorded only in docs | design notes | where one blocks a phase, it is listed in section 5 |

## 7. Parking lot

| Idea | Why not now |
|---|---|
| Verify helper and ledger (scheduled restore checks) | a person runs `verify`, and `plan` warns when one is overdue; two sites do not need a second credential yet |
| `kind: network-forward` | proxy devices work and are checked; the advantage it has does not apply |
| Profile-carried defaults; expanded config in plan, join and export | the recipe emits the boilerplate; the change is large and breaks a `join` rule |
| `kind: access`, OIDC attributes on offers, per-name auth strength, `accept:` | no app on either site needs them yet; Home Assistant on tron will be the first |
| Listener and bypass check | valuable for Home Assistant's discovery leg; after C3 |
| Ownership phases 2-3 (report, prune) | needed when the recipe first drops or renames a helper |
| Authoritative apply, layers 2-3 | ownership phase 1 covers the immediate need |
| Project-level `user.tink.site.*` stamps | only for daemon-side drift checks of the recipe, which come after B5 |
| Mesh gateway and control plane as recipe roles | the control plane is hand-run on another machine; move it only with decision 5 |
| Declaring people through LLDAP's bootstrap | needs secrets in files; the directory UI works |
| Restic engine | Incus-native copies cover both sites |
| Cross-stack references | `--from-server` stamps cover the case that mattered |
| `tink mongo snapshot` | a stub with no design. Under the "no deprecation for undeployed features" rule in CLAUDE.md, remove the command |
| A name for the remote-access feature | nothing to name until the mesh is a recipe role |
| `tink.exe` live testing | no site depends on it |
