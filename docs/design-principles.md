# Design principles: the patterns that shaped tink

**Status: a synthesis for discussion, 2026-10-17. It describes patterns already in the repository; it is not a decision record.** It was written by reading the code and the design notes, and each pattern below says where it is built and where it is written down. Claims are marked **[code]** (read in this repository), **[docs]**, **[lab]** (seen on a real host) or **[proposed]** (not built). The words it uses are in [the glossary](glossary.md).

It exists so that a new design can be tested against the old ones. For any proposal, ask each pattern's question (the last line of each section) before building.

## 1. Incus is the control plane, and tink holds no state

**The pattern.** tink has no state file, no daemon API of its own and no client/server split; it diffs live Incus state. What it must remember it writes on the object it concerns, under `user.tink.*`, so the state stays with the thing it describes.

**Evidence.** Built: `CLAUDE.md` forbids a state file or a separate daemon binary; the volume's backup policy and verification stamps live on the volume (`internal/backupmeta`); ownership is `user.tink.stack` on a volume; the helper's status document lives on a volume it points at (`internal/helper`); publish stamps live on the instance (`internal/resolve/publishstamp.go`); `cmd/tink/keynamespace_test.go` fails the build if tink writes a `user.` key outside its namespace. Written: [what-tink-is.md](what-tink-is.md#the-loop), the `internal/daemon` package comment (why one binary), the platform recipe proposal (#131) (which stamps, and where). Measured: `user.*` is accepted by the server config, projects, networks, ACLs, profiles, instances, volumes and pools, though Incus's own metadata lists it for only some **[lab]**.

**The question to ask.** Does this add state tink must keep somewhere else? If a copy is needed, is it derived (written only by `plan apply`, in an owned prefix, removed when stale) and is something else the source of truth?

## 2. Plan, then apply; and the default is to leave things alone

**The pattern.** You declare a stack, `plan` compares it with what Incus reports (pure reads), and `plan apply` converges, level by level with ties broken the same way every run. A plan says what would change, what is blocked and why, and what looks wrong. It never acts on something that could interrupt running work without being told to, and it never deletes: what a stack no longer declares is reported as an extra.

**Evidence.** Built: `internal/resolve` (`plan.go` reads, `apply_run.go` converges, `graph.go` orders); dependencies come from name matching plus an explicit `depends_on`, deliberately not an expression language (`dependson.go`); a `BLOCKED` result for what tink refuses to touch; the disruption guard on networks (`allow_disruption`, `network.go`); extras (`extras.go`, [plan-extras.md](plan-extras.md)); image drift and `on_image_change: report | ignore | rebuild` (`imagedrift.go`, [image-updates.md](image-updates.md)); idempotent `kind: exec` guarded by a `check`; files placed before an instance's first start ([first-start-files-design.md](first-start-files-design.md)); apply output that says what it did *not* do (a config change that needs a restart). Written: [resolver-architecture.md](resolver-architecture.md).

**Known gap.** The loop creates and updates but never deletes, which hardened into a stronger rule than intended; ownership stamps are the proposed fix (#133, not built, not merged).

**The question to ask.** Can `plan` show this before it happens? What does it do when the live state disagrees: report, block, or act, and who chose?

## 3. Incus's own vocabulary is the intermediate form

**The pattern.** "Resolving" was never one problem but two: *translation* of a foreign vocabulary, and *convergence* of a description already in Incus's words. Translators sit at the edge (`tink run` turns a `docker run` line into Incus primitives; a Compose or Kubernetes translator would too); convergence happens once, in Incus's vocabulary. An Incus object or key keeps Incus's exact word, so `config:` and `devices:` blocks paste in unchanged. A concept Incus lacks gets tink's own word, checked first against Incus, Compose and Kubernetes.

**Evidence.** Written: [resolver-architecture.md](resolver-architecture.md#two-problems-not-one), [the glossary's rules for choosing words](glossary.md#choosing-words) and its table of collisions (ingress, project, profile, publish, volume), `internal/run/DESIGN.md` (the flag mapping and its non-goals), [docker-to-tink.md](docker-to-tink.md). Built: `internal/run`, `tink export` (the reverse direction, [export.md](export.md)).

**The question to ask.** Is this in Incus's words? If it needs a word of its own, was it checked for collisions, and is it a deliberate step away from the rule?

## 4. A capability is a declaration, an actor and checks; code talks to roles

**The pattern.** The pieces tink has share one shape: something declared, something that acts on it, and checks that say when it is wrong. A **role** is a job more than one technology can do (ingress, identity, single sign-on, dns, mesh); an **engine** is the technology that does it today. tink's code talks to the role, and an engine sits behind it. A **helper** is a container Incus keeps running for one role, one container per role. Helpers split: the role helpers **serve** (they handle users' traffic, are stock engines, and hold no Incus credential), and the daemon helper **tends** them (tink itself, the only one with a certificate). Its rule is that **derived state heals and declared state reports**.

**Evidence.** Written: [what-tink-is.md](what-tink-is.md#one-pattern-a-capability) (the capability table, roles and engines, helpers), [helper.md](helper.md#what-the-helper-is-for) ("what the helper is for"). Built: the daemon helper (`internal/helper`, `internal/daemon`, `internal/jobs`), `kind: helper`, role and leg stamps read by `join`, the backup scheduler and the ingress reconcile. Not built: the role helpers as a shipped stack.

**Known gap.** A role's contract (the stamps it carries, the files and ports it exposes, the snippets it must define) is in comments and Go constants, not data (the accounting in #131).

**The question to ask.** Which role is this, and what is its contract? Is it derived state (heal) or declared state (report)?

## 5. Offers, grants, and the join

**The pattern.** An app states what it can provide (an **offer**, as keys on its instance, saying nothing about who may use it). The site states who may use it (a **grant**, today a `kind: publish`). `tink join` is a **pure function** over those and the helpers' own facts, and renders the five pieces a published name touches: dns records, mesh records, proxy routes, sign-on rules and ACL rules. Its applier **refuses instead of guessing**: an error, no publishes, a clash with the zone file, or a file tink did not write each stop it with nothing written.

**Evidence.** Built: `internal/join` (`join.go`, `offers.go`, `render.go`, `apply.go`), hand-written golden files in `internal/join/testdata/golden`, `kind: publish` and its stamps (`internal/resolve/publish.go`, `publishstamp.go`), `tink join --from-server` (`internal/join/server.go`), `tink join apply dns`. Written: [join.md](join.md), the design in #130. Run live **[lab]**: the stamps and the join against a real Incus 7.5.1 server on 2026-10-17, all five renderings matching the golden files.

**Known gap.** Only the dns records can be applied; the routes and sign-on rules are still hand-written inside the engines' config files, and `tink ingress reconcile` is a second, older mechanism for routes.

**The question to ask.** Is this a fact about the app or a decision about who may use it? Is the rendering a pure function, and what does the applier refuse?

## 6. Opinion as code

**The pattern.** An opinion has four parts: a **default** stated once with its reason; a **check** that notices departure; a **reasoned opt-out** (a field that holds the reason, not a flag that silences); and **machinery that makes following it cheap**. Opinions warn, they do not block, except where proceeding loses data. Where there is a real alternative it names it and says when to deviate.

**Evidence.** Built, in full for backups: `backup:` on a volume, 3-2-1 and `location`, the policy written on the volume, the scheduler, `verify` (`internal/backupmeta`, `internal/volbackup`, `internal/backuprun`, `internal/resolve/policy.go` and `volumebackup.go`); the opt-out is `backup: none: <reason>`. Written: [volume-backup.md](volume-backup.md). The four parts above come from an earlier opinion-as-code design note that was never merged; the pattern stands, but two of its proposals have been superseded by #131: opinions shipping as embedded stack fragments (the recipe is a load-time expander instead), and `deploy` becoming an alias of such a stack (it is folded into the recipe, once the recipe plans clean against a live platform). Its reverse-proxy row is gone with self-registration. Partly: storage and identity are still opinions held by prose or convention only.

**The question to ask.** What is the default and why, what notices a departure, and does the opt-out carry its reason?

## 7. Facts, recipe and policy are different things

**The pattern (the newest, proposed).** tink owns the containers and the wiring between them (a *recipe*: a pure function from site facts to resources). The user owns a short file of site *facts*. *Policy* stays where it already is: apps state offers, the site publishes them to groups, people live in the identity helper. No copy of the stack exists, so nothing falls behind.

**Evidence.** Proposed in #131 and tested against a real platform stack literal by literal. Precursors that are built: `deploy.env` (a facts file, in the wrong form, `configs/deploy.env.example`), the offer/grant split (pattern 5), and `tink join`'s pure-function shape.

**The question to ask.** Is each input a fact, a recipe choice or a policy, and who maintains it? If the user must edit it on every upgrade, it is in the wrong place.

## 8. Least privilege, and refuse rather than guess

**The pattern.** What always runs should hold the least it can. tink avoids primitives that reach root (a proxy device to the Incus socket, an edit right on the helper's own instance), runs restore checks in a restricted project with no network, encrypts secrets and never prints them, and when it cannot be sure it refuses and says how to fix it.

**Evidence.** Built: `--api-via network` and the status volume (`internal/helper`); `backup verify --check-project` (`internal/volbackup`); the authorization scriptlet and its routing (`configs/daemon/authorization.star`, `internal/bootstrap/authorization_star_test.go`); secrets as `${secret:NAME}` only in `environment.*`, age-encrypted, redacted from output (`internal/secrets`, `internal/resolve/secretrefs.go`, `maskconfig_test.go`, `cmd/tink/redact_test.go`); a `tink run` that publishes a port with no network refuses ([oci-network.md](oci-network.md)); a missing project says how to create it. Written: [helper.md](helper.md), [secrets.md](secrets.md), [secrets-design.md](secrets-design.md). Measured **[lab]**: a certificate restricted to a project is not a boundary unless the routing keeps it on Incus's own check, and a project-restricted certificate reads that project's stamps with plain `can_view`, while the server config needs `can_view_sensitive`.

**The question to ask.** What is the most powerful thing this can now do? Is there a narrower way, and what does it do when it is unsure?

## 9. Adoption and reversibility

**The pattern.** tink works on an Incus that has never heard of it, manages only what it is told to, and leaves the rest alone. You can come in from a hand-built host and go back out: `tink export` writes live objects as a stack (and checks the result with `plan`), `tink run` is a quick start you distill, and an existing helper is adopted in place and changed by nothing. A stack is proved read-only first: `plan` against a live server until it says no changes.

**Evidence.** Built: `internal/resolve/export.go` ([export.md](export.md)), `internal/run`, `kind: helper` adoption (`helperkind.go`), `--remote` for planning a server from elsewhere ([remote.md](remote.md)). Written: [docker-to-tink.md](docker-to-tink.md), the exploration notes ([explore-uptime-kuma.md](explore-uptime-kuma.md)).

**The question to ask.** Can this be adopted from what exists, and can it be left again? Does `plan` say "no changes" against a host that already matches?

## 10. Evidence discipline

**The pattern.** Claims are tagged by how they were checked. Alternatives were tried for real before building (Terraform and `incus-apply` are written up as tried). Features are checked live on a disposable host, and the docs say what was and was not run; they are updated in the same change as the code, and a "not yet run" claim is flipped when it stops being true.

**Evidence.** Written: the tags in the headers of [what-tink-is.md](what-tink-is.md) and every design note; [resolver-architecture.md](resolver-architecture.md#terraform-tried-for-real); the per-command "Status" notes in the README; `CLAUDE.md` ("docs travel with code"); the live-validation notes recorded in [helper.md](helper.md), [join.md](join.md) and [volume-backup.md](volume-backup.md).

**The question to ask.** How do we know? Was it seen on a real host, or read, or hoped, and does the doc say which?

## 11. Testability and shape

**The pattern.** Capabilities are plain importable packages under a thin CLI, so a UI or API could reuse them. Logic is pure where it can be and tested with injected fakes (no Incus server needed), with hand-written golden files for renderings. The server a command acts on is chosen once per process and there is no ambient default, so a forgotten remote cannot send a stack to the wrong place.

**Evidence.** Built: `cmd/tink` (arguments only) over `internal/*`; `internal/incusapi`; `CLAUDE.md` ("Tests need no Incus server"); `--remote` and `$TINK_REMOTE` applied once in `main.go` and refused by commands that only make sense on the host ([remote.md](remote.md)).

**The question to ask.** Can this be tested without a real server, and what stops it acting on the wrong one?

## How to use this page

For a new design, write one line per pattern: *holds*, *strains* (and where), or *not applicable*. The strains are the design work. For the platform recipe, the strains found so far are: its first-apply steps do not converge (pattern 2); upgrades and renames need ownership before "never delete" can bend (2); its knobs must carry reasons like an opt-out (6); a facts document is the first input not in Incus's vocabulary (3); facts fixed at birth are a category of declared state that must be refused, not reported (4); and the project stamp and the facts file are two places that can disagree (1). They are argued in #131.
