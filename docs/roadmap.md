# Roadmap: tink's capabilities, and what to build next

**Status: a draft, 2026-10-17, at `main` after #103.** Nothing here is decided. This roadmap is about tink's capabilities. Which sites and workloads tink has been run on, and what each one showed, is recorded in the author's private notes, and only the lessons are written here. Claims are marked **[code]** (read in the tree, and `go build`, `go vet` and `go test ./...` pass), **[pr]** (the merged pull request), **[docs]** (what a doc in this repository says, not re-checked), **[lab]** (measured on a lab host, with the version written down), **[notes]** (the author's private notes) and **[inferred]** (reasoning; check it). Sizes: **S** (a small PR), **M** (a few PRs, or one with a live lab), **L** (a design plus several PRs and labs). No dates.

## In one page

1. **Tink is the tool. Workloads are how it is tested.** The author runs different workloads on tink to see what works, what does not, and what has to be built. The goal is a tink that runs today's workloads and tomorrow's. A game server exercises different parts of tink than home automation does. This plan is ordered by what tink must be able to do.
2. **Gaps found by running workloads decide what gets built (sections 2 and 3).** Run a workload, log each gap with evidence, fix it, run it again. A capability becomes generic (a recipe field, a new kind, a new verb) only when a second kind of workload hits the same gap, or the author says why one is enough. The author is the only user today, so nothing is built for users who do not exist yet.
3. The binary is in better shape than the docs suggest. Resolver, backups, secrets, `--remote`, the daemon helper and `join` are built and have been run live. Most of what feels "in flux" is design that has not been built, plus about a dozen docs whose status lines are out of date.
4. **Nothing merged since 2026-10-07 has been released.** `v0.1.4` is the newest tag, with about 90 commits on top of it: `join`, `kind: publish`, `kind: helper`, the status volume, `--api-via network`, `--check-project`. A release build installs the helper image of its own version, so no deployment can run the new helper work without `--binary` until there is a release **[code, git tags]**.
5. **Three site shapes have been tried:** a LAN host behind NAT with the full platform spine; a public edge on one address, built before the recipe with the old ingress daemon and `tink deploy`; and a non-web, stateful, UDP workload on a clean public host. The public-site fixture for `join` (#103) was synthetic, and the second shape is the real one it imitates.
6. **Anything stateful needs a stop that saves.** A game server found it; databases probably share it **[inferred]**. The first form is a hand-written host unit, already measured. The tink feature (`stop:` plus one verb) follows, and its value is in tink's own stops (`on_image_change: rebuild`, `restart: true`), which kill the app without a save today.
7. **The platform recipe is the right direction, built one role at a time from the roles the site shapes share,** with golden fixtures taken from live hosts. Not from the full spine in one go, and not from a one-role first step either.
8. Two pairs of mechanisms overlap and should be merged before the recipe depends on them. **Ingress:** `tink ingress reconcile` and `join`'s Caddy rendering. **"The platform":** `tink deploy` and the recipe. A deployment still runs both old mechanisms, so freezing them has a cost.
9. Some designs have no workload behind them. Park the verify helper with its ledger, `kind: network-forward`, profile-carried defaults and `access` documents.
10. **Off-site copies are not encrypted.** Incus 7.5.1 has no at-rest encryption for volumes, copies or exports, so a copy lands as plaintext on the target. The designed way out is the restic job engine (K10); its open questions are credential delivery, who runs the job, retention and a network path for a remote repository.
11. Ownership stamps should be written from birth on a fresh host. Minimal watching is the quiet precondition of "reliable", and nothing is built. The docs disagree about two decisions that matter (copied template or shipped recipe; whether a restricted certificate is a boundary), and both should be settled in the docs first.

## 1. Where things stand: tink's capabilities

### Built and exercised live

| Capability | Evidence |
|---|---|
| `tink plan` / `plan apply` (graph, levels, BLOCKED, image drift, extras) | `internal/resolve` **[code]**; many real stacks, including a full platform stack whose second plan is quiet **[docs, notes]** |
| `tink run`, `tink export` | README; [export.md](export.md) "Verified live" **[docs]** |
| `tink deploy` | built a public-edge deployment on Incus 7.4 **[docs: README, notes]**. Not used on the other site shapes |
| `tink ingress reconcile` and the host `tink-daemon` unit | live on that deployment **[docs: README]** |
| Secrets (`${secret:}` in `environment.*` only, age store, `--generate`) | #9; a multi-container web stack; a game server's RCON password **[docs, notes]** |
| Volume backup: snapshot policy, copies to pool and remote targets, restore, verify, 3-2-1 check | #10-#13, #18, #19, #22, #23; runs to a NAS and to a remote host in [volume-backup-design.md](volume-backup-design.md) **[docs]** |
| `backup verify --check-project` | #96; run on Incus 7.5.1, 2026-10-17 **[pr: #99]** |
| Daemon helper: install from the published image, scheduled copies, restart after kill, drain and upgrade | #21, #26-#33, #56, #57 **[pr]**; lab host **[docs: helper.md]**. Its job history on the LAN site is `backup-run` only **[notes]** |
| Helper status on its own volume; host API over the network (`--api-via`) | #94, #95; run on a clean host, 2026-10-17 **[pr: #99]** |
| `kind: helper` (adopt and report) | #92; it adopts a deployed helper that is still on `v0.1.4` **[notes]** |
| `--remote`, `tink remote` | #15, #17; from a laptop **[docs]** |
| `kind: network`, `kind: network-acl` | #74, #87; each run live once **[docs]** |
| Files placed before first start, and under a disk mount | #78 (11 of 11 in the lab), #85 **[docs]** |
| `kind: exec` with `check_output` | #79; a throwaway lab **[docs]** |
| `tink identity groups` | #80; run once against a lab directory **[docs]** |
| `tink join` (print) and `join apply dns` | #91, #92; the LAN site's dns records are generated by it, run by hand **[notes]** |
| Publish stamps, `join --from-server` | #97; on a clean host, output identical to the file-based join and to the golden files **[pr: #99]** |
| NAT proxy warnings (static address, overlapping listeners) | #103; read-only plans on a clean host **[pr]**. A `nat` proxy stack was also applied there: a clean re-plan, and the app saw the client's real address (TCP only) **[lab]** |
| OCI NIC `name:` warning | #77; checked against a lab host **[docs]** |
| Authorization scriptlet under deploy's routing | #93; Incus 7.5.1 only **[pr, docs: README]**. The older deployment runs the earlier 7.4 form **[notes]** |
| Releases: binaries and helper image | `v0.1.0`-`v0.1.4`, all dated 2026-10-07 **[code: tags]** |

### Built, tested only against fakes or fixtures

| Capability | Evidence |
|---|---|
| `join apply dns` against a real dns helper from `--from-server` | "Not run" **[docs: join.md]** |
| `join` for a public-only site (`public` leg, optional dns and mesh helpers) | #103; fixtures in `internal/join/testdata/public/` **[code]** |
| Install after a remove that kept the volumes | the bug was found live; the fix (#98) is unit-tested **[pr]** |
| File update under a mount while the instance is stopped | #90; fakes only **[docs: first-start-files-design.md]** |
| `allow_disruption: true`; a bridge update; an ACL on a NIC; a `reject` default | **[docs: network.md, network-acl.md]** |
| `on_image_change: rebuild` | `internal/resolve/rebuild.go` **[code]**; no live run recorded **[to confirm]** |
| The helper's ingress half (`--ingress`, through the file API) | **[code]**; no live run recorded |
| Helper survives a host reboot | "not yet tried" **[docs: helper.md]** |
| `tink.exe` on Windows | built in CI, never run **[docs: remote.md]** |

### Designed, not built

| Design | Where | Note |
|---|---|---|
| Graceful stop: a `stop:` field, a verb, a host unit | PR #104, `docs/graceful-stop.md` | the measurements are solid; nothing is coded |
| Platform stack as a shipped recipe (`kind: site`, `tink platform plan`) | [platform-recipe.md](platform-recipe.md), [platform-recipe-accounting.md](platform-recipe-accounting.md) (#100) | stage 1 (the accounting) is done; stages 2-5 are not |
| Ownership stamps and scoped prune | `docs/ownership-stamps-design.md` on an unmerged branch | only volumes carry `user.tink.stack` today **[code: backupmeta.StackKey]** |
| Daemon loop running `join apply dns --from-server` | [join.md](join.md), [helper.md](helper.md) | `internal/daemon` does not import `internal/join` **[code]** |
| Applying the caddy, sso, acl and mesh renderings | [join.md](join.md#what-it-does-not-do) | needs a generated-include split in the proxy and sign-on configs, a derived-rules marker in `network-acl`, and a decision on who writes them |
| Listener and bypass check on discovery legs | [offer-grant-join-design.md](offer-grant-join-design.md) D4 | `netstat` through exec; *unverifiable* on images with no shell |
| Narrow helper credential (restricted certificate, generated scriptlet) | [what-tink-is.md](what-tink-is.md) decision 2(a) | measured: 10 rules, no instance rights **[notes]**. Volumes in the `default` project cannot be restricted |
| Scheduled restore checks (a verify helper and a ledger) | `docs/scheduled-verify-design.md` on an unmerged branch | |
| Authoritative apply, layers 2 and 3 | [authoritative-apply-design.md](authoritative-apply-design.md) | layer 1 (extras) is built |
| `kind: network-forward` | [public-exposure.md](public-exposure.md), [network-resources-design.md](network-resources-design.md) | unmeasured |
| Secrets in files (phase 2) | [secrets-design.md](secrets-design.md#later-phases) | blocks sign-on OIDC keys and a directory service's password file in a recipe |
| Watching and alerts (a heartbeat, a push channel) | the author's private notes | nothing built or applied |
| Retiring `tink daemon install` | an unmerged branch, far behind main | |
| Restic job engine (backup step 4): client-side encrypted, deduplicated off-site copies | [volume-backup-design.md](volume-backup-design.md) | prototyped (restore fidelity, a repeat backup adding 0 B, `restic check` passing); nothing in the code refers to it **[docs]**. Needed for encrypted off-site copies: K10 |

### Proposed or idea only

OIDC attributes on offers; `kind: access` (one instance using another's offer); per-name authentication strength; `accept:` for a known bypass; project-level `user.tink.site.*` stamps; role contracts as data; profiles carrying defaults (needs `plan`, `join` and `export` to read expanded config); a helper version policy (`on_version_change`); a role stamp that finds any helper; `kind: platform` and `tink platform eject`; the mesh gateway and control plane as helpers; a name for the remote-access feature; declaring people through the directory's bootstrap; notifications; cross-stack references; `tink mongo snapshot` (a stub command that returns "not yet designed" **[code]**).

### Docs whose status line is out of date, or that contradict each other

| Doc | What it says | What is true |
|---|---|---|
| [what-tink-is.md](what-tink-is.md) (dated 2026-10-13) | "nothing yet stamps the declarations onto the server" | stamps are built (#97) and run live (#99) |
| same, "The platform stack (proposed)" | "A template you copy and own (decided)" | [platform-recipe.md](platform-recipe.md) leans the other way. Not reconciled |
| [glossary.md](glossary.md) | platform stack "starts as a template you copy and own" | same conflict |
| [offer-grant-join-design.md](offer-grant-join-design.md) | header: "Nothing in it is built"; open question 8: "tink has none [no ACL kind]"; third step: "Not yet run against a real server" | three build steps are recorded below that header; `network-acl` is #87; #99 ran it live |
| [helper.md](helper.md) | "A restricted certificate is not a boundary on current Incus" | [what-tink-is.md](what-tink-is.md) and a lab measurement say it is a boundary when the project is also `restricted=true`. The two docs disagree |
| [helper.md](helper.md) | the design is PR #16, "an open pull request" | #16 is still open, far behind main. The doc it links to is not on main |
| [daemon-jobs.md](daemon-jobs.md) "Not here yet" | jobs can only be reached where the jobs directory is | `tink helper jobs|log|cancel` work remotely (#56) **[code: cmd/tink/helperjobs.go]** |
| [platform-recipe.md](platform-recipe.md) fact 5 and its open questions | a `public` leg "`tink join` does not model yet" | #103 added it the same day |
| [design-principles.md](design-principles.md) pattern 6 | an older note on opinions as code is the argument | that note proposes "opinions ship as stacks". The platform recipe supersedes it in part; nothing says which parts |
| README | one deployment is "the reference deployment" | the author's working sites are now more than that one, and the README says nothing about the others |

## 2. How tink is evaluated: the loop

The method the author already practises:

1. **Pick a workload and run it from its real install instructions,** the way a newcomer would: `docker run` or compose for a single container, upstream docs for a larger stack. Use a throwaway project.
2. **Log every place tink helped, was silent, or was wrong,** each with an ID, the area, the finding and the evidence. The newcomer trials (`docs/explore-*.md`) and `docs/docker-gap-analysis.md` are this for single-container installs. A private gap log (IDs G1 to G19) is this for a larger migration. One-feature labs are the same thing at a smaller scale.
3. **Fix the gap in tink** on a branch, then a PR. The first eight landed as PRs #70 to #75 and others.
4. **Run the workload again.** That is the exit test for the fix.
5. **Record what the run exercised,** so the next trial can aim at what is still untouched. The author keeps a coverage matrix of workload archetypes against the parts of tink (live, lab only, untouched) in the private notes.

Where the log's open gaps land in this plan. These are the log's own words as read on 2026-10-17, and some may have been fixed since **[inferred]**:

| Gap | Area | Where the plan puts it |
|---|---|---|
| G8 | `${secret:}` only in `environment.*`, no secret-to-file | K5 stage 2 (secrets in files, phase 2) |
| G10 | No readiness, no stop timeout, no dependent restarts | K2 (a stop that saves). Readiness and dependent restarts are not planned |
| G11 | Ingress is HTTP-only and reads the NIC address | K4 (the join apply path replaces the discovery, section 4, item 2) |
| G12 | A backup target's `location` is an unverified claim | Not planned. Small, and a candidate for `plan` to check |
| G13 | No ownership marker on instances | K6 |
| G14 | No networking opinions | Partly #103 (the NAT proxy checks); the rest is not planned |
| G15 | No way to import data into a volume | Not planned. A migration will hit it |
| G16 | No way to create an instance without starting it | Not planned. Same hit |

**What the trials show about parts of tink that nothing has exercised yet:** UDP through a `nat` proxy; a database-primary app that needs a quiesce before a snapshot ([volume-backup-design.md](volume-backup-design.md) names a future `quiesce` hook); an app that signs in through OIDC; more than one Incus host as a cluster; IPv6; a GPU; a VM-type instance (the code handles `virtual-machine`, but there is no live run **[code]**). Only ever touched in a lab: home automation, `on_image_change: rebuild`, the helper's ingress half, the host reboot gate. The most exercised: plan and apply, backups to a second host, and the sign-on chain through the platform spine.

## 3. The gate: what gets built

The aim is a tink that runs the workloads the author has now and the ones tried next. The gate keeps that from turning into building for imagined users.

1. **A gap found by running a workload is the trigger.** The workload can be one that has run, or a named planned trial. "It might be useful" goes to the parking lot.
2. **Fix the gap with the smallest thing that closes it,** and use the workload's rerun as the exit test. A capability with no workload to rerun is a design note, not a deliverable.
3. **Going generic takes a second archetype.** A new kind, a verb the user must learn, or a recipe field is built for one workload only as the smallest thing that works (a printed unit, a hand-written stack, a doc). It becomes a feature when a second kind of workload hits the same gap, or when the author says why one is enough.
4. **Empty cells are work too.** Where the docs say tink supports something and no workload has run it (UDP proxies, `on_image_change: rebuild`, a host reboot with the helper), a trial is part of the roadmap, ahead of building more.
5. **Workload files stay with the workload.** A game server's stack, its RCON script and a home automation config are fixtures that tink's docs point to. Tink takes only what generalises.
6. **Evidence is measured on a host, with the version written down.** A design resting on one measurement says so (section 7).

How the capabilities in section 5 stand against it:

| Capability | Workload that needs it now | Second archetype | Verdict |
|---|---|---|---|
| Release | all | n/a | Build |
| Prove the helper on a clean host | a game server | any site that moves | Build (it is a test, not a feature) |
| UDP through a `nat` proxy | a game server | none | Measure only. Add a check if it fails |
| Stop that saves: the unit | a game server | none | Build as a hand-written unit |
| Stop that saves: `stop:` and a verb | a game server | a database-primary app (a next trial) | Build after the unit has run a real reboot and an image update, and the database trial confirms the gap |
| Apply the join renderings | a LAN site | a public-edge site | Build for the LAN site first |
| Platform recipe | a LAN site, a clean public host | a public-edge site | Build, role by role, with a fixture per site shape that has the role |
| Ownership stamps, phase 1 | a fresh host | LAN and edge sites adopt later | Build phase 1 only |
| Minimal watching | all | n/a | Build the minimum |
| Narrow helper credential | a LAN site | a clean public host | Build in parallel, not on the critical path |
| Encrypted off-site copies (the restic job engine) | volumes with sensitive content that leave the host | any off-site target on a provider the author does not control | Build, after credential delivery and the job's rights are settled (K10) |
| Verify helper, `network-forward`, profile defaults, `access` | none | none | Parking lot |

## 4. Designs that should change shape

1. **Release before more design (S).** All the helper work since 2026-10-07 is unreleased, and a deployed helper is on `v0.1.4`. Cut a release (`v0.2.0`). Add a release step to the definition of done for any feature the helper runs. *Evidence:* `git tag`; helper.md "With no flag, a release build installs `ghcr:minihci/tink-helper:<its own version>`".

2. **Merge the two ingress mechanisms: keep the writer, replace the discovery.** `internal/ingress` already has what applying `join`'s Caddy rendering needs: writes through the file API, writes before removals, one reload, and a failed read treated as an error **[code: ingress.go, helper.md]**. What is wrong with it is the discovery (`user.ingress.*`, by instance name) and the template (a site block per domain, no sign-on) **[code: render.go]**. Recommendation: `join apply caddy` reuses the ingress writer for the `handle` fragments, aimed at an include directory in the platform Caddyfile. Then `tink ingress` is frozen, and deleted once the deployment that runs it moves or is retired. *Evidence:* [platform-recipe-accounting.md](platform-recipe-accounting.md) part 2, item 2, already says "pick one". The older deployment runs the old one: `user.ingress.*` on its app instances and routes from a volume **[notes]**.

3. **Freeze `tink deploy`; do not use it on new hosts.** It is a second definition of "the platform" (the proxy, the sign-on service, the Incus UI). It deletes and relaunches three instances on every run, replaces the server config whole, and shells out **[docs: README, accounting item 1]**. All of that is the opposite of plan and apply. Keep it alive for the deployment that was built with it, and add nothing. Delete it when the recipe covers that deployment, or when it is retired. Merge or redo `retire-daemon-install` at the same time (S).

4. **Graceful stop: build it smaller, and in a different order.** The design (PR #104) proposes a field, two verbs (`tink stop` and `tink graceful-stop --all`), use in apply, and a host-unit installer. Recommended shape:
   - **First, with no tink code:** a hand-written host unit, `Requires=` and `After=incus-startup.service`, whose `ExecStop` runs the app's quit command with `incus exec` and waits. This is exactly what was measured. It protects the world at the next reboot.
   - **Then one verb** (`tink stop [INSTANCE...|--all]`) and the `stop:` field. **Use them in tink's own stops first.** `on_image_change: rebuild` runs pull, stop, rebuild, start, and `restart: true` restarts the instance **[code: rebuild.go, apply.go]**. Today both kill an app that needs a save (for the restart, **[inferred]** from the stop measurement: a restart stops the instance the same way). That matters more than the host hook, because tink does it every time.
   - **Measure `server.save` followed by a plain stop** before adopting `quit` **[inferred]**. `quit` leaves the instance stopped only with `boot.autorestart: "false"`, so a crashed server would not come back. A save followed by a stop would let autorestart stay on. Unmeasured.
   - Print the unit rather than install it, as `daemon install` does. Then there is no `tink host` command to design.

5. **The platform recipe: build it role by role from the roles the site shapes share, and take the fixtures from the live hosts.** The preconditions in [platform-recipe.md](platform-recipe.md#what-has-to-be-true-first) (split generated content, first-apply steps, ownership, a helper version policy, watching, role contracts, pins) are what a full spine of five role helpers needs. A first step with only the daemon helper is too thin: an expander shaped by one trivial role will have the wrong shape for the second. The site shapes share more: **ingress and sign-on** (the LAN and the edge shape), the mesh control plane, and the daemon helper (all three, eventually). Recommendation:
   - The recipe is an **expander at load time**: a `kind: site` document expands into ordinary resources that the normal `plan` converges. `tink platform render` prints the expansion, and that printout is the eject. Do not build a second plan path.
   - **Stage 0, before any expander:** write each site's platform stack by hand as plain YAML. For a deployment built before the recipe, start from `tink export` **[inferred]** and bring it to plan-and-apply shape. These are the golden fixtures.
   - **Then add roles one at a time,** starting with the ones two site shapes share (ingress, sign-on). A role earns its place when its fixtures on two sites both plan "no changes" against the live host.
   - Keep the hand-written platform stack of the full-spine site as the oracle until the recipe reproduces it.

   *Evidence:* the accounting found about fifteen facts and no site literals in tink's code. The traps section already warns about growing a templating language.

6. **Park profile-carried defaults and the move to expanded config.** A recipe written in Go can emit the repeated helper boilerplate itself. Moving `plan`, `join` and `export` to expanded config breaks a rule `join` relies on ([join.md](join.md#from-the-server)), and profile edits need a new disruption guard. That is large and buys nothing the recipe cannot do. App stacks still repeat the boilerplate, which is a smaller pain. *Evidence:* [platform-recipe.md](platform-recipe.md#what-a-profile-can-hold-as-a-default), where a device does not merge.

7. **Cut the verify helper for now.** The scheduled-verify design adds a second helper, a ledger, eight rule groups and a role marker for helpers. Its own alternative (a person runs `backup verify --check-project`, and `plan` warns when a check is overdue) is what exists now. That suffices for a handful of volumes. Revisit once watching exists and checks are being forgotten. *Evidence:* `docs/scheduled-verify-design.md` §1 "ALTERNATIVE"; `plan` already warns when a check is stale **[docs: volume-backup.md]**.

8. **Cut `kind: network-forward` from the roadmap.** Proxy devices with `nat` are measured, tink passes them through, and #103 checks them. The forward's one advantage is that it does not need a device-edit right. That matters only if the daemon helper edits instances, and it is designed never to. *Evidence:* [public-exposure.md](public-exposure.md) cautions.

9. **Ownership stamps: split phase 1 from the rest, and do it before a fresh host fills up.** Stamping on create is cheap on a fresh site and adds no adoption churn there. Report and prune (phases 2-3) wait until the recipe needs to drop or rename a helper. *Evidence:* the design's own phases, and its first-apply churn section.

10. **Do not let the credential work block the daemon loop.** The daemon helper already holds a certificate with the reach of root and makes copies with it. Letting it also run `join apply dns --from-server` adds two file reads and one write, plus list calls **[docs: join.md]**. Its reach does not grow. Narrowing the certificate (decision 2(a)) is a separate, parallel security track, and it is complicated where volumes live in `default`.

11. **Settle the boundary statement.** [helper.md](helper.md) says a restricted certificate is no boundary. [what-tink-is.md](what-tink-is.md) and [design-principles.md](design-principles.md) pattern 8 say it is one inside a restricted project. Correct helper.md (S, docs only).

12. **Close PR #16** and move whatever is still true in `helper-design.md` into [helper.md](helper.md). Today the user page points at an unmerged design that is two weeks behind what was built.

13. **Keep an internet-facing app off the helper's bridge.** If the daemon helper's API listens on the same bridge as an internet-facing process that loads third-party code, that process can reach the API port. The join design already puts apps on bridges of their own behind an ACL ([offer-grant-join-design.md](offer-grant-join-design.md) D9). Do the same for the platform project and the app project. This is a use of capabilities tink already has, and it belongs in a stack and in the recipe's defaults, not in new code.

## 5. Roadmap: by capability

Capabilities come first. **K0** and the first workload on a clean host form the critical path to "a stateful app runs on a clean host under tink". **K5**, the recipe, is the largest piece and runs along the side of the others until it has a second role.

```
K0 release ──> K3 helper on a clean host ──> [a stateful workload, offsite copies] ──> K2a stop unit ──> K2b stop: in tink
      │                  K1 UDP via nat (measure, before the workload's cut-over)
      ├──> K4 join apply path ──> K9 retire the old mechanisms
      └──> K5 recipe: stage 0 fixtures ──> expander + first shared roles ──> more roles ──> a full-spine site through it
K6 ownership phase 1 (before K5's expander) · K7 watching · K8 credential narrowing (all parallel)
K8 + secrets in files ──> K10 encrypted off-site copies (restic job engine)
```

### K0: release and hygiene (S)

- **Needed by.** every deployment that runs a helper, and every later helper feature.
- **Deliverables.** First fix the stale docs in section 1 (docs only, S). Then tag `v0.2.0` and check that the image pulls without a login [helper.md]. Close PR #16, delete the leftover branches, and merge or close the two design notes.
- **Depends on.** Nothing. **Unblocks.** K3, K4.
- **Exit test.** `tink version` reports `v0.2.0`, and `tink helper install` with no flag works on a scratch server.

### K1: UDP through a `nat` proxy (S, a measurement)

- **Needed by.** a game-server workload. **Gate.** One workload, so a measurement and a note, not a feature.
- **Deliverables.** Measure a UDP `nat` proxy on a clean host (the rule, the client address, a real client joining). Record it in [public-exposure.md](public-exposure.md). Add a plan check only if the measurement finds a trap.
- **Depends on.** Nothing. **Unblocks.** The workload's cut-over.
- **Exit test.** A client outside the host connects over UDP and the server sees its real address.

### K2: a stop that saves (S, then M)

- **Needed by.** a game server now; databases probably **[inferred, unmeasured for them]**.
- **K2a (S): the unit.** A hand-written host unit, `Requires=` and `After=incus-startup.service`, whose `ExecStop` runs the app's quit command through `incus exec` and waits. This is what `docs/graceful-stop.md` (PR #104) measured. Its source is kept with the workload. *Exit:* a real `systemctl reboot`, not only `systemctl stop incus-startup.service` as measured: the log shows the save before the instance stops, and the server is back after boot.
- **K2b (M): `stop:` in tink.** The `stop:` field and its `user.tink.stop` stamp, one verb (`tink stop [INSTANCE...|--all]`), and use in tink's own stops: `on_image_change: rebuild` and `restart: true` **[code: rebuild.go, apply.go]**, which kill the app without a save today. Print the unit rather than install it, as `daemon install` does. Measure `server.save` against `quit` first. *Gate:* start only after K2a has run through a reboot and an image update, and the database trial confirms the need in a second archetype. *Exit:* an image change applied with `rebuild` leaves a fresh save from before the stop.
- **Depends on.** the workload's stack (K2a), K2a and PR #104 (K2b). **Unblocks.** Image updates with `on_image_change: rebuild` for anything stateful.

### K3: the helper on a clean host (S)

- **Needed by.** a clean public host; it is also the first host reboot with a helper, which `helper.md` says was never tried.
- **Deliverables.** A `tink` project with a healthy helper installed from a stack (`kind: helper`), `core.https_address` on the bridge address with a port nothing else holds **[lab]**, and the restore-check project from [volume-backup.md](volume-backup.md#running-the-check-in-a-project-of-its-own).
- **Depends on.** K0. **Unblocks.** Offsite copies, and K5's stage 0 on that host.
- **Exit test.** `tink helper status --check` exits 0, a second `plan` says no changes, and the same holds after a deliberate host reboot.

### K4: the join apply path (M each)

- **Needed by.** a LAN site (the dns half is run by hand today **[notes]**); a public-edge site, if it moves to offers.
- **Deliverables, in order.**
  - A daemon worker that runs `join apply dns --from-server`, with a field in the status document. First run it by hand against a real dns helper, which has never been done.
  - `join apply caddy` on the ingress writer (section 4, item 2), aimed at an include directory in the platform Caddyfile. Then the sign-on service's rules; confirm that it merges two files, which [offer-grant-join-design.md](offer-grant-join-design.md) marks **[to confirm]**.
  - The ACL rendering, which needs the derived-rules marker in `network-acl` (M). The mesh records stay hand-applied on the control-plane host.
- **Depends on.** K0. **Unblocks.** K5's ingress and sign-on roles, K9.
- **Exit tests.** A publish added to a stack is answered within one interval with no hand step; the canary routes and rules are no longer in the platform stack.

### K5: the platform recipe (L)

- **Needed by.** every site shape. **Gate.** Three shapes, but built role by role (section 4, item 5).
- **Stage 0 (S to M per site).** Each site's platform stack as plain YAML that plans "no changes" against the live host. A full-spine site has this already. For a deployment built before the recipe, start from `tink export`. For a clean host it is K3's stack plus its bridges.
- **Stage 1 (M).** The `kind: site` expander and `tink platform render`, with pins shipped with the recipe, a `plan` note when the helper runs older tink than the recipe needs (a report only), and the first shared roles: ingress and sign-on.
- **Stage 2 (L in total, M per role).** Dns, identity, the mesh control plane, the monitor. For each: its contract written as data (stamps, files, ports), its default image pin, and its restore check. Secrets in files (phase 2 of secrets) when identity or OIDC needs them.
- **Stage 3 (M).** The full-spine site's platform stack is generated, not hand-written ([platform-recipe.md](platform-recipe.md#proving-it) stage 3).
- **Depends on.** K0, K3, K4 (for the split), K6. **Unblocks.** K9.
- **Exit tests.** Each role's fixtures plan "no changes" against the live hosts that have it; the recipe's output for the full-spine site plans "no changes" against it; deleting a site's hand-written file loses nothing.

### K6: ownership stamps, phase 1 (M)

- **Needed by.** a fresh host (stamp from birth), and the recipe before it renames or drops a helper.
- **Deliverables.** Phase 1 of the ownership design: stamp on create, `--adopt`, and the validate, export, extras and guard changes. Merge the design note first. Prune and report (phases 2 and 3) wait.
- **Depends on.** Nothing. **Unblocks.** K5's stage 1 upgrades, prune later. It is not on the critical path to a workload running.
- **Exit test.** The design's own: 30 real stack files plan the same apart from one line, `--adopt` restarts nothing, and a fresh host's objects carry stamps from birth.

### K7: minimal watching (M)

- **Needed by.** every site, before it is called reliable.
- **Deliverables.** A heartbeat out and a phone alert. Running `tink helper status --check` from the helper or from cron counts as the first probe.
- **Exit test.** Stopping a helper, or a host, alerts the phone within the stated window.

### K8: narrow the helper credential (M to L)

- **Needed by.** a LAN site, where the helper's reach is widest. **Gate.** Do not let it block K4: the helper already holds root-equivalent reach, so the dns loop adds none (section 4, item 10).
- **Deliverables.** Start on a clean host, where every volume can live in restricted projects, then on a site where some policy volumes are in `default` and cannot be restricted.

### K9: retire the old mechanisms (S to M)

- **Needed by.** nothing urgent. It follows the decision about the deployment that still runs them.
- **Deliverables.** Delete `tink ingress` and `tink deploy` once that deployment moves or is retired. Merge or redo `retire-daemon-install`. Remove the `tink mongo snapshot` stub (section 8).
- **Depends on.** K4, K5, and that decision.

### K10: encrypted off-site copies (L)

- **Needed by.** any volume with sensitive content that is copied to a provider the author does not control. Today every off-site copy is plaintext at rest.
- **What Incus can and cannot do (measured on Incus 7.5.1).** The server's configuration metadata has no encryption, LUKS or key setting for pools, volumes, instances or backups. `incus storage volume copy` has no encryption option, and `incus storage volume export` offers only compression and the driver-optimized format. A copy is encrypted in transit by TLS and then **stored as plaintext on the target's pool**. Encrypting the target's disk (LUKS under the pool, or an encrypted ZFS dataset) protects only a powered-off or reprovisioned disk, because the key has to be on the running host.
- **The path:** the *ephemeral restic job* already designed in [volume-backup-design.md](volume-backup-design.md#the-ephemeral-job-engine). Per job tink takes a snapshot, makes a copy-on-write clone, mounts it read-only into a short-lived container from a digest-pinned restic image, runs the command through exec, and always deletes the instance and the clone. restic encrypts and deduplicates on the way out, so the target holds only ciphertext and can be anything restic can write to (an S3-compatible store, SFTP, or a REST server in append-only mode, which also stops a compromised source from deleting history). The design keeps tink out of the business of being a backup engine. It was prototyped with the real image and verified for restore fidelity, a repeat backup adding nothing, and `restic check` passing.
- **Open questions, to settle before building:**
  1. **Credential delivery.** The repository password and any storage keys must reach the job container. Secrets are only usable in `environment.*` today (the secrets design's later phases cover files). A lost repository password means lost backups, so its own backup is part of the design.
  2. **Who runs the job.** The daemon helper's certificate deliberately cannot create instances, so a job needs either its own restricted project and certificate or a narrow, project-scoped right for the helper. This is the same question as K8.
  3. **Retention.** `retain:` is an age; `restic forget` is count or calendar based, so the engine needs its own `keep:` block.
  4. **A remote repository needs a network path in the job container.** The prototype used a local repository volume and did not cover this.
  5. **Orphan cleanup** if tink dies mid-job (a marker on the instance and clone, and a reaper), **and which project** the jobs and clones live in.
- **Deliverables.** `engine: restic` on `kind: backup-target`; the job runner; `plan` warnings that name a copy to a plaintext target as such; a restore and a `check` through the same mechanism; docs in [volume-backup.md](volume-backup.md) saying plainly that `engine: incus` copies are not encrypted at rest.
- **Depends on.** The secrets work (files) and K8's answer on the job's rights. **Unblocks.** Off-site targets that are not Incus servers, and off-site copies of sensitive volumes on a third-party provider.
- **Exit test.** A volume is backed up off-site by restic, the target holds no plaintext (a listing of the repository shows only encrypted data), a restore into a scratch volume has the same checksums, and `restic check` passes.

**Smallest sequences:**

- **A stateful app on a clean host, backed up, with a graceful stop:** K0, K3, the workload's stack with K1 before its cut-over, K2a, offsite copies. Five steps, about one M and four S. K2b replaces the hand-written unit and makes image updates safe.
- **The platform as a recipe, on a site that is not the full spine:** K0, K3, K5 stage 0 for a clean host and for a pre-recipe deployment, then K5 stage 1 with ingress and sign-on, where the pre-recipe deployment is the second fixture and the full-spine site the oracle.

## 6. Decisions the author must make

Decisions about particular hosts are kept in the private notes. These are the ones about tink itself.

1. **Copied template or shipped recipe?**
   - The docs say both (section 1).
   - Recommendation: shipped recipe with eject, as [platform-recipe.md](platform-recipe.md) argues. Then edit what-tink-is.md and the glossary.
   - Blocks: K5 stage 1.
2. **Is the recipe a load-time expander, or a separate `tink platform plan` path?**
   - Recommendation: the expander, with `render` as the eject (section 4, item 5).
   - Blocks: K5 stage 1.
3. **`quit`, or `server.save` then stop, for a game server?**
   - Decide after one measurement.
   - Blocks: K2b's `stop:` field shape, and whether autorestart stays on.
4. **Who runs the restic job?**
   - Options: a separate job project and certificate (a second credential, kept apart from the daemon helper); or a narrow, project-scoped instance and volume-clone right added to the helper's certificate.
   - Recommendation: the separate project and certificate. It keeps the helper's rule that it never creates instances, and a compromised helper cannot reach job credentials.
   - Blocks: K10, and part of K8.

## 7. Risks and unknowns

| Risk | Rests on | What to do |
|---|---|---|
| The 1-second stop window and the absent stop setting are Incus 7.5.1 behaviour | one day of measurement on one host (`docs/graceful-stop.md`) | re-measure at every Incus upgrade, like the authorization scriptlet. A future `oci` stop setting could make the hook unnecessary |
| The host-unit ordering (`Requires=` with `After=`) was measured with `systemctl stop incus-startup.service`, not a real shutdown | one run | K2a's exit test is a real reboot |
| A UDP proxy with `nat` | never measured; #103 measured TCP | K1 |
| The authorization scriptlet's input changes between Incus versions | #93; README says so | already a documented re-test; a host that does not use deploy is not affected |
| A host with a public address and no host firewall | field notes | keep the API listener off the wildcard address. Every container on the default bridge can reach the API port. Consider the NIC egress rule from helper.md, whose effect on traffic to the host is a **[hypothesis]** |
| The helper has never been through a host reboot | helper.md | K3's exit test |
| A game-server image tag floats, and how the game updates inside the image is unknown **[to confirm]** | the legacy project's README | pin by digest; find out whether a new game build means a new image or an in-place update at start |
| Recipe scope creep into a templating language | [platform-recipe.md](platform-recipe.md) traps | a field only when two site shapes need it; golden fixtures per site |
| The sign-on service merging a static and a generated file | **[to confirm]** in the offer-grant design | lab it before K4's Caddy and sign-on step |
| `join --from-server` reads stored config; a role stamp in a profile is invisible | [join.md](join.md#from-the-server) | the reason to park profile defaults (section 4, item 6) |
| A deployed helper is on `v0.1.4`, and moving it to the status volume needs a new token | the author's notes | the helper upgrade defers the move |
| Single host for everything on a site | the author's notes | accepted; K7 at least makes outages visible |
| Several "decided" claims were decided in the author's own working sessions and are recorded only in docs | design notes | where one blocks a phase, it is listed in section 6 |
| Copies made with `engine: incus` are stored unencrypted on the target (Incus 7.5.1 has no at-rest encryption for volumes, copies or exports) | measured on a lab host | say so in the docs now (K10's deliverables); treat any third-party target as able to read the data until K10 exists |
| The plan knows of three site shapes because they were found by looking | the author's notes | ask whether other shapes exist before section 5 is settled |

## 8. Parking lot

| Idea | Why not now |
|---|---|
| Verify helper and ledger (scheduled restore checks) | a person runs `verify`, and `plan` warns when one is overdue; no workload needs a second credential yet |
| `kind: network-forward` | proxy devices work and are checked; the advantage it has does not apply |
| Profile-carried defaults; expanded config in plan, join and export | the recipe emits the boilerplate; the change is large and breaks a `join` rule |
| `kind: access`, OIDC attributes on offers, per-name auth strength, `accept:` | no workload needs them yet; the first app that does will show their shape |
| Listener and bypass check | valuable for an app with a discovery leg; after K4 |
| Ownership phases 2-3 (report, prune) | needed when the recipe first drops or renames a helper |
| Authoritative apply, layers 2-3 | ownership phase 1 covers the immediate need |
| Project-level `user.tink.site.*` stamps | only for daemon-side drift checks of the recipe, which come after K5's stage 3 |
| Mesh gateway and control plane as recipe roles | the control plane is hand-run elsewhere; move it only with the decision about that deployment |
| Declaring people through the directory's bootstrap | needs secrets in files; the directory UI works |
| Cross-stack references | `--from-server` stamps cover the case that mattered |
| `tink mongo snapshot` | a stub with no design. Under the "no deprecation for undeployed features" rule in CLAUDE.md, remove the command |
| A name for the remote-access feature | nothing to name until the mesh is a recipe role |
| `tink.exe` live testing | no site depends on it |
| Anything that no workload needs yet | The gate (section 3). A design note is enough |
