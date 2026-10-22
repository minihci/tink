# Roadmap and architecture

**Status: a proposal for discussion, 2026-10-19.** It follows [capability-review.md](capability-review.md), which holds the evidence: a rubric, a scorecard per capability, and the code checks behind the claims here. Sizes are lines of non-test Go as of this date. Claims are marked **[code]** (read in this repository) or **[proposed]**. Nothing in it is built.

## What tink is

tink turns the docker-run and compose examples people self-host from into a complete, wired-up service on their own Incus host. You describe an app in a stack file: its image, volumes, ports and what it offers. tink supplies what those examples leave out (backups with off-site copies, a reverse proxy, DNS names, single sign-on, a directory of people and groups, remote access) by running a small reference platform of pinned upstream services as ordinary Incus containers and generating their configuration from the facts the apps declare. `tink plan` shows what would change and `tink plan apply` converges it. tink stores no state of its own; Incus is the database. It is built for one person running a homelab, with a VPS for presence and off-site copies. It is not Kubernetes and not a general infrastructure tool, and it never edits what an app does inside its own container.

The platform is optional. The converger must be useful with no helper installed.

## The architecture of the core

tink is one binary of about 24,500 lines (plus about 28,000 of tests) with seven direct dependencies. No file is over 810 lines. The size is not in any one place; it is in breadth: a dozen top-level commands, four jobs done by two mechanisms (listed below), and compatibility code for shapes that were once deployed. The fix is boundaries and a rule for adding things, with deletion as the smaller part.

### Four rings, with dependencies pointing inward only

```
  ring 3  platform recipe     a Go function: a few site facts -> resources.   optional
  ring 2  capabilities        backup, publish (the derive stage), the helper,
                              identity and mesh adapters. Each: declaration, actor, checks
  ring 1  the converger       load, expand, graph, plan, apply, export, run.  the product
  ring 0  adapters            incusapi, incusconf, remote, secrets.           no internal imports
```

| Ring | What lives there | Lines today |
|---|---|---|
| 0 adapters | `incusapi` 547, `incusconf` 47, `remote` 461, `secrets` 771 | about 1,800 |
| 1 converger | `resolve` 7,599, `run` 1,378, and the commands for plan, apply, export, run, secret, remote in `cmd/tink` | about 9,000 plus about 2,000 in `cmd` |
| 2 capabilities | backup (`backupmeta`, `volbackup`, `backuprun`) 2,800; the helper (`helper`, `daemon`, `jobs`) 3,800; `join` 1,145; `identity` and `mesh` 570 | about 8,300 |
| 3 recipe | not built | 0 |
| to retire | `bootstrap` (`tink deploy`) 1,097, `ingress` 640, `backup` 18, and `cmd/tink/join.go` 332 | about 2,100 |

**The rule that makes the rings real:** the converger knows about *kinds*, never about capabilities. Until #128 it did: `resolve` imported `backupmeta`, `helper` and `run`. Backup has moved behind a seam (`backupext`); `run` is the converger's own instance engine, in ring 1 in the table above, so that import is within the ring; `resolve` still imports `helper` for `kind: helper` **[code]**. The boundary to draw is that a capability reaches the converger through a small, fixed set of extension points, and the converger imports none of them. `internal/resolve/imports_test.go` lists what it may import, so a new import of a capability fails the build.

### Three extension points, and no more

A homelabber can hold three things in their head. Every capability must fit them, and a feature that needs a fourth is a sign it belongs somewhere else.

1. **A kind** (`project`, `instance`, `storage-volume`, ...): decode, validate, plan, apply, dependencies. A union struct of about 55 fields serves all kinds today; the per-kind field table in `validate.go` is what a new kind has to keep in step **[code]**.
2. **A pass**: a function over the loaded resources that derives something into them (`applyStackProject`, `derivePublishStamps`, `inheritProject` are passes now). Passes run in one fixed order, in one place.
3. **A check**: a function that returns findings, with a severity, beside the resource they concern. A blocked finding stops that resource and its dependents; the rest proceed.

The capability pattern from the design notes maps onto them: the **declaration** is a kind or a block on one; the **actor** is a pass (derive) or the helper (do, on a timer); the **checks** are checks.

### The one pipeline

```
 stack files + site facts
        |  load, validate (strict)
        v
   secrets, image names          <- one function, used by plan AND apply
        |  passes, in order: defaults, project, publish stamps, derive (join), inherit
        v
   expanded resources + derived files + findings
        |  graph into levels
        v
   plan (reads only)  ->  apply (level by level, derived files last)  ->  read back
```

Today this is assembled twice in `cmd/tink` and the order of the passes matters, so consolidating it is the first piece of structural work, not a cosmetic one **[code]**. The join is the derive pass; it is not a command. See capability-review.md, findings 4, 5 and 7.

### Patterns that earn their place, and ones to limit

Keep and use more:
- **Incus is the database.** No state file, no second source of truth. Metadata lives under `user.tink.*` on the objects it describes.
- **Pure functions with golden files** for anything that renders (`join`, the backup policy arithmetic). They are the specification and the regression test at once.
- **Fail closed, and say why.** Any error means nothing is applied; a refused action names its reason.
- **Injected seams** (`Files`, `applyFunc`, the jobs `FS`) so the risky parts can be tested without a server.
- **Declared state reports, derived state heals.** Applied consistently, this decides what a helper may do unattended.
- **One owned prefix** for the one kind of key tink may remove.

Limit or retire:
- A process-global for the chosen remote (`incusapi`), which blocks two servers in one process.
- Plan changes as free text (`[]string`) that apply parses.
- A flat namespace for resource names while other places key by project and name.
- Two mechanisms for the same job: route generation (`ingress`, `join`), convergence (`deploy`, `plan apply`), redaction (the secrets redactor and registry-credential scrubbing), and ingress appliers (host path and file API).

### What "maintainable by one homelabber" means, concretely **[proposed]**

- **One page of architecture** (this one) that is true, plus one user page per built feature. No design note stays in the tree once its feature is built; the reasoning moves to the package comment. No design note for an unbuilt feature merges to the main branch; it lives in an issue.
- **A demand gate, beside the shape gate.** A capability is built when a workload someone runs finds a gap in tink, and the workload's rerun is the exit test; with no workload to rerun it is a design note in an issue. It becomes a general feature (a new kind, a verb the user must learn, a recipe field) when a second kind of workload hits the same gap, or when the author says why one is enough. Where the docs claim something that no workload has run (a UDP proxy, `on_image_change: rebuild`, a host reboot with the helper), a trial comes before more building. The author is the only user today, so nothing is built for users who do not exist.
- **A capability gate.** A new capability ships only with: an engine-neutral declaration on the app or the site; a default engine behind a role; a plan-time check; the three extension points above; a golden test. Anything else waits.
- **A freeze list** until the converger is hardened: no new roles or helpers, no `kind: access`, no recipe, no network-forward kind, no restic engine. The consumer drives the order, not the design notes.
- **A size budget per ring,** reviewed each release: the converger stays the largest and most tested; a capability that grows past a few thousand lines is split or simplified before it grows further. The helper subsystem (3,800 lines, the largest piece outside the converger) is the next one to look at after backup verification lands.
- **Few dependencies.** Seven direct today; adding one is a decision with a reason written down.
- **A short list of deletions that is actually carried out.** No deprecation period and no migration code, deployed or not: a removal is a release note that says what to change, and a stack that needs changing is fixed by hand, by someone (or an agent) who can read the code. Code written only to carry old shapes forward is the compounding cost this project cannot afford.

## Redundancy to remove

| What | Why | Size | Condition |
|---|---|---|---|
| `tink deploy` and `internal/bootstrap` | a second, destructive convergence engine, shelling out; superseded by a stack and, later, the recipe | 1,097 | the platform stack plans clean against a live platform |
| `tink ingress` and `internal/ingress` | the first offer implementation; the join's proxy rendering replaces it, and its known gaps (an unvalidated domain and port, a non-atomic write) go with it | 640 | Phase 0, with no migration code; a stack that uses the keys is fixed by hand |
| `tink mongo snapshot` and `internal/backup` | a stub that says "not yet designed" | 18 | none; delete now |
| `tink join` as a command | the join is a pass, not a verb | 332 | after the derive stage reports in `plan` |
| status document on the instance, and the host-path ingress apply | compatibility for helpers installed before the volume layout | part of 1,300 | a dated cut-over of any running helper |
| `tink daemon install` and the host unit | superseded by the helper | part of `bootstrap` | with `deploy` |

That is about 2,100 lines, 8 to 9 percent, plus their tests. Deletion alone does not tame the project. The larger effect comes from the rules above and from one pipeline instead of two.

## The roadmap

Each step is done when its check passes. Steps in a phase can run in parallel; phases mostly follow each other.

### Phase 0: stop the growth (S)
- **Cut a release first.** Nothing merged since the last tag has been released, and a release build installs the helper image of its own version, so a running helper cannot take the newer work until there is one. Tag it from the current `main`, which still has self-registration and `tink deploy`: it is the last release a host built with them can upgrade to unchanged.
- Fix the docs that contradict each other or whose status line is out of date: the helper page's statement that a restricted certificate is no boundary (it is one inside a restricted project), the platform-stack "template to copy" against "shipped recipe", the offer-grant-join header, and the pages that point at an unmerged design. Close the stale pull request for the helper design and delete the merged or abandoned branches.
- Commit the capability review and this roadmap; agree the freeze list and the capability gate.
- Move design-only notes (`platform-recipe`, `platform-recipe-accounting`, `graceful-stop`, the unbuilt parts of `offer-grant-join-design` and `what-tink-is`, and the unmerged designs for ownership stamps and scheduled restore checks) out of the main tree into issues; keep one status table.
- Delete the `mongo` stub.
- Delete self-registration outright (`tink ingress`, `internal/ingress`, the `user.tink.ingress.*` and legacy `user.ingress.*` keys, the daemon's ingress loop and its `--no-ingress`, `--ingress-via-api`, `--routes-dir` and `--ingress-instance` flags, the helper's legacy-key count in its status). Only `cmd/tink/main.go` and `internal/daemon/run.go` import it **[code]**. No migration code: a stack that still uses the keys is fixed by hand, by an agent following [removals/ingress-self-registration.md](removals/ingress-self-registration.md), and until the derive stage lands the proxy's routes are written by hand. Two sharp edges. A running helper started with `--no-ingress` will not start after an upgrade that removes the flag, so it is reinstalled with the release note's instruction. And `tink deploy` installs `tink daemon run` as the host's reconcile daemon, which would then have nothing to reconcile, so that step of deploy goes with it (`internal/bootstrap/reconciler_daemon.go`); the rest of deploy stays until the platform stack replaces it. A host that was built with deploy keeps running the build it has until its owner upgrades it, so nothing breaks when this lands: its generated routes keep being served. The removal page describes that shape too. The self-registration writer (a file written through the instance API, removals after writes, one reload, a failed read treated as an error) is a proven pattern; Phase 3 takes it from git history, at the commit before this removal, rather than keeping dead code.
- *Done when* the docs tree describes only what is built, and the README table and `capability-review.md` agree.

### Phase 1: make the core trustworthy (M)
- `planInstance` compares run state and profile membership; an instance left half-created is resumed or reported, not "no changes" (F3).
- One shared instance-config update with a bounded retry on a failed precondition (F4).
- `plan` says how many undeclared items it leaves in place, for every kind, not only instances (F5). Built for profiles, storage volumes, networks and projects ([plan-extras.md](plan-extras.md)); not for network ACLs, which are converged rule by rule.
- `context.Context` and a timeout through the resolver and guest exec (F7).
- *Done when* each of those has a test that fails without it, and `go test -race ./...` is clean.

### Phase 2: one pipeline, with defaults (M)
- Consolidate preparation into a single function used by `plan` and `plan apply`; the pass order lives in one place. The flags both commands share (`--socket`, `--offline`, `--secrets`, `--identity`) are declared once, as persistent flags on `plan`, so `apply` and the future `plan explain` inherit them. *Done (#125):* `internal/prepare` runs load, secrets, connect, image qualification and `Levels` in that order for both commands, and apply uses the connection it made instead of opening a second one; a pass is added in `prepare` and nowhere else.
- **Ownership stamps, phase 1:** stamp what tink creates (instances, profiles, networks and the rest, as volumes are today) from the moment it is created, with an explicit adopt for what already exists. It makes "what is tink's to remove" answerable, which "not in the stack" cannot. Reporting and pruning stay out until something needs them. *Done (#127):* every kind tink creates is stamped in its creation request; `plan` says in one line how many declared objects have no stamp and `plan apply --adopt` (or `ownership: adopt-on-apply`) stamps them without restarting anything; see [ownership.md](ownership.md). Orphans, takeover as BLOCKED and prune are #133 phases 2 and 3, not built.
- Add the stack-level `defaults:` pass (restart flags, NIC name, limits, the backup block), local to the converger. *Done (#126):* `defaults:` on `kind: stack` fills instance `config` and device keys (by device type) and a volume's `backup` block into the resources that do not set them, second in `internal/prepare` ([stack-defaults.md](stack-defaults.md)). Role stamps are not a default: a role differs on every helper, and deriving it from a name would need a template, which the resolver does not have. The five-helper fixture is 150 lines written out and 123 with defaults; each helper still states its image, role, address and state volume.
- Move capability knowledge out of `resolve` behind the three extension points, starting with backup. Backup is done (#128; `backupext`, see [resolver-architecture.md](resolver-architecture.md), "Update, 2026-10-20: the converger imports no capability"). What remains is `kind: helper`, which needs a kind extension point (decode, validate, plan and apply per kind) before `helper` can leave the converger. *Deferred on purpose:* that registry is designed with its second consumer in Phase 3 (the derive stage adds passes and checks), or after the helper review in Phase 4, whichever comes first, so it is not built around a subsystem that review may shrink. `internal/resolve/imports_test.go` names `helper` as the one exception until then.
- *Done when* `plan` output is identical for every existing stack file with the old and new pipeline, and the boilerplate a stack repeated (boot flags, NIC name, limits, the backup block) is written once in `defaults:` and resolves to the same resources.

### Phase 3: the derive stage (L)
- **Findings first.** Run the join's checks inside `plan`, read-only, with a stack-wins merge over the server's view (finding 7, steps B).
- **Layer the requirements.** Names (dns, proxy) need only an ingress helper and a zone; sign-on, mesh and ACL switch on with their helper; `allow:` is optional and its absence means open to the LAN. Add a site-level TLS choice (wildcard or per name).
- **Derived files as planned changes** under the operator's own credential (a helper applying them on a timer is optional, and comes later with a narrow identity), dns first, with the dns applier's ownership, refusal and read-back rules as resolver behaviour. The proxy, sign-on and ACL renderings follow as their engines get an include point. *Started (#177):* the ACL renderings are derived by `plan` for a `kind: network-acl` that says `derive_ingress: true` ([network-acl.md](network-acl.md#rules-derived-from-the-publishes)); a stack that says `network: own` gets its bridge, ACL and the apps' addresses ([network.md](network.md#a-bridge-of-the-stacks-own)); the proxy's routes and the sign-on helper's rules are written into a `kind: file` with `derive:` ([join.md](join.md#derived-files)); the dns records (`tink join apply dns`) and the mesh records are not yet, and `tink join` goes when they are and `plan explain` exists.
- **Remove** `tink join`. Add the expanded-stack view, not named join (name to be decided; see below).
- *Done when* a published name in a stack shows its dns, proxy, sign-on and ACL changes in `plan`, `plan apply` writes and reads them back, and no hand-written copy of any of them remains in the platform stack.

### Phase 4: backup and delivery (M, can start with Phase 1)
- **No scheduled restore checks, for now.** The first draft of this phase had the helper enqueue them. The authorization lab measured that the helper's real work needs no right over any instance and a restore check creates a throwaway one, so scheduling it means a second identity in a project of its own, a record of results, and its own rules: a design of its own, and the one the private review parks. Until watching exists, a person runs `tink backup verify --check-project` and `plan` warns when a check is overdue. What changes now is small: a volume that declares copies and no verification cadence is nagged, instead of being quiet (today "no cadence declared, no nag"), and the warning reaches a person through the heartbeat below.
- The schedule's time zone moves into the policy or the site facts.
- Say where the stack files and the secrets identity belong, and have `plan` mention it when secrets are in use.
- Watching stage 1 only: a heartbeat to something outside the site and one push, so a failure reaches a person. Probes derived from publishes follow Phase 3.
- **Review the helper subsystem** (`helper`, `daemon`, `jobs`: 3,800 lines, the largest piece outside the converger) once the heartbeat has landed: what it must do, what the directory queue and the install path cost, and what can be removed or simplified. The output is a short written decision, before Phase 5 builds on it. It also settles the name, and the direction is to call it the daemon (`tink daemon install`, `kind: daemon`), which is already what the command and package are called, leaving "helper" for the role containers. Hosts that run the old name are fixed by a page an agent can follow, in the style of [removals/ingress-self-registration.md](removals/ingress-self-registration.md), not by migration code. The glossary already says the bare "helper" is ambiguous (a role container such as the dns helper, or the one that runs `tink daemon run`), and the other capabilities are named for what they provide.
- *Done when* a deliberately failed copy, an overdue restore check and a stopped helper each produce a push, and the review is written.

### Phase 5: helper credential cut-over (M, a parallel track)
This runs beside Phases 3 and 4, not after them. The helper already holds root-equivalent reach and makes copies with it; letting it also write one derived file adds two reads and a write and does not widen that reach, so narrowing the credential must not block the derive stage.

- Move the scriptlet generator into tink, generated from the stack; one narrow identity per job; a restricted project for anything that creates instances.
- Cut a running helper over (status on a volume, API over the network, a restricted certificate), reversible from the host.
- *Done when* the helper holds no right to edit an instance and `authorization.client.tls-restricted` routes to the generated script.

### Phase 6: the platform recipe (L, only as a consumer needs it)
- Role contracts as data, the recipe as Go code, `deploy` folded in, mesh policy and the identity helper as derived artifacts. The recipe is a load-time expander: a site document expands into ordinary resources that the normal plan converges, and the printout of that expansion (`tink plan explain`) is also how a user ejects. One role at a time, starting with the ones two sites share (the proxy and sign-on), each with a fixture taken from a live site that plans "no changes" against it; the hand-written platform stack stays the oracle until the recipe reproduces it.
- *Done when* the platform stack plans clean against a live platform, built from about fifteen site facts.

### In parallel, outside the roadmap
The workloads are how tink is tested, and they run beside the phases, not behind them. The first real consumer, a home-automation migration, needs the converger, backup with restore checks, and the helper, and can proceed on hand-written platform pieces while Phases 1 to 3 land. A second, a stateful game server on a clean public host, exercises what the first never did and needs three small things that are not features: a measurement of a UDP port through a `nat` proxy; the helper installed on a clean host and put through a host reboot, which has never been done; and a hand-written host unit that asks the app to save before Incus stops it, with no tink code. The graceful-stop verb and field wait for the gate: that unit has been through a real reboot and an image update, and a second kind of workload (a database-primary app) shows the same gap. Two more trials would fill empty cells: an app that signs in through the platform's OIDC, and `on_image_change: rebuild` on real data.

## Running the project on GitHub **[proposed]**

As of 2026-10-19 the repository has Issues on and nothing else: no issue, no milestone, no template, only GitHub's default labels. The main branch is not protected, there is no ruleset, secret scanning and its push protection are off, Dependabot's security updates are off, and the workflow actions are pinned to major tags **[code, `gh api`]**. For one maintainer and agents, the aim is the fewest surfaces that do the job, in this order of value for effort:

1. **Secret scanning with push protection, Dependabot (security updates, plus version updates for Go modules and the actions), and private vulnerability reporting.** All three are settings or one small file, they cost nothing to run, and they address risks a public repository carries however small it is: a committed token, a stale dependency, and a bug like the ingress injection that should be reported in private.
2. **A ruleset on `main`:** require the CI workflow to pass and a pull request, forbid force-pushes. Agents open the changes, so the gate that stops a broken change is worth more than any process document. A single maintainer can still merge their own pull request.
3. **Issues as the home for design notes,** with a `proposal` form (the capability gate as a checklist, the claim tags as required fields) and labels by ring (`ring:adapters`, `ring:converger`, `ring:capability`, `ring:recipe`) and by kind (`bug`, `proposal`, `removal`). Not Discussions: `gh` reads and writes issues directly, an agent can find and link them, and a second surface to keep in step is what the project is trying to remove.
4. **Milestones for the phases,** so state lives in one place and the roadmap file keeps only the reasoning.
5. **Release notes by label** (`.github/release.yml`) once releases resume, so a `removal` label is how "what to change by hand" reaches the people who need it.
6. **Turn the wiki off,** since it is a second, unread place for docs to drift.

Not worth it for this project: Projects boards (milestones carry the state), CODEOWNERS (one maintainer), and Discussions.

**Applied 2026-10-19,** on the author's go-ahead: items 1, 2, 4 and 6 and the labels of item 3 are settings, made with `gh`; the issue forms, Dependabot's configuration and the release-notes grouping (items 3 and 5) are files, in pull request #110, which the new ruleset holds until CI passes. The ruleset requires a pull request and the `build` check, and forbids force-pushes and deleting `main`; it asks for no approving review, so the maintainer can merge their own pull request. Milestones exist for Phases 0 to 6. The first 33 issues were created on 2026-10-19 (#114 to #146): the work items of Phases 0 to 2, the design notes as proposals, and the open gaps. Later phases get their issues when they start. To undo any of it, an admin edits the ruleset or the repository settings.

## Decided and open

Decided on 2026-10-19:
- Self-registration is deleted in Phase 0, with no migration code.
- Design notes live in issues until the feature is built.
- The helper subsystem is reviewed after Phase 4.
- The freeze list and the capability gate.
- Where a removal needs a stack changed, the repair is a page written for an agent, not code.

Open:
1. **The name of the expanded-stack view,** leaning to a subcommand of plan: `tink plan explain`. It prints what the facts expanded to, and for each derived value where it came from (a default, a site fact, a publish). It sits beside `tink plan` and `tink plan apply`, takes the same files and flags, and is read-only like plan. Other candidates were `tink expand`, `tink show` and `tink render`.
