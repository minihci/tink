# Capability review: the rubric, and the first scorecard

**Status: a working review, 2026-10-18; checked against the code and the README capability table on 2026-10-20, after ingress self-registration was removed (#149).** One page per round: a rubric every capability is judged by, and a scorecard for each capability reviewed so far. Claims are marked **[code]** (read in this repository, not run) or **[docs]** (taken from another page here). Nothing in it was run against a server for this review; where a row says a capability was run live, that is what the other page says, and the README table agrees with it.

tink is an orchestrator and a reference platform for one person running a homelab, with a VPS for presence and off-site copies. The platform is optional: the converger must be useful with no helper installed. Capabilities are reviewed in the order a homelabber would reach for them:

1. run the app (the converger itself)
2. reach it by name, with TLS (ingress and dns)
3. back it up, locally and off-site
4. secrets
5. reach it from outside (a VPS and the mesh)
6. sign-on, then people and groups
7. the rest of the lifecycle (updates, graceful stop)

## The rubric

| # | Row | The question |
|---|---|---|
| 1 | Neutral fact | Does the app declare something it knows about itself (an offer), rather than engine configuration or site policy? |
| 2 | Stateless | Is it derived from what Incus reports, with no state file of tink's own? |
| 3 | Plan-time check | Does `plan` say when the wiring is wrong, before anything is applied? |
| 4 | Heal or report | Declared state reports and derived state heals. Does it keep to that? |
| 5 | Validated inputs | Is every value that reaches rendered configuration checked first? |
| 6 | Applies, run live | Does it converge by itself, and has it been run against a real server? |
| 7 | Recipe-ready | Can a pure function from site facts emit it as resources? |
| 8 | Stands alone | Does it work without the rest of the platform? |

Rows 1 and 7 decide whether a capability fits the platform model. Row 8 decides whether the platform stays optional.

## 2. Reach it by name, with TLS: ingress and join

The capability is the **ingress role**: a Caddy instance, the ingress helper, that serves the site's names. That role stays. What decides what it serves is **join**: an app states an offer, the site publishes it under a name, and the join renders the dns records, proxy routes, sign-on rules and ACL rules that agree with it. The earlier mechanism, **self-registration** (an instance registered itself with a few config keys and a reconciler rendered one Caddy site block per instance), was removed in #149 ([how to replace it](removals/ingress-self-registration.md)); the first version of this review compared the two, and that comparison is dropped. Public routes are written by hand until the join's proxy half exists.

| Row | join |
|---|---|
| 1 Neutral fact | yes: `user.tink.offer.<name>.{port,proto,listen}` names no engine; the site names it in a `kind: publish` **[code]** |
| 2 Stateless | yes: publishes are stamped onto the instance as `user.tink.publish.<name>`, and `--from-server` reads them back, failing closed on a stamp it cannot read **[code]** |
| 3 Plan-time check | mostly: the join's findings are thorough, but `plan` runs only the check that a publish's instance is in the same stack **[code]** |
| 4 Heal or report | partly: dns is applied by hand with `join apply dns`; nothing runs it on a timer **[code]** |
| 5 Validated inputs | yes: the label, the zone, group names and addresses are checked before rendering **[code]** |
| 6 Applies, run live | only `join apply dns` writes; the proxy, sign-on and ACL renderings are print-only, and `internal/join` is not called from the daemon **[code]** |
| 7 Recipe-ready | yes: a publish is a resource **[code]** |
| 8 Stands alone | no: it needs an ingress helper with a role, a zone, a `serves`, `mesh` or `public` leg, a sign-on helper, a managed network with exactly one ACL, and a static address on the app's NIC **[code]** |

### What the scorecard shows

1. **join is not layered the way the homelabber order is.** Name and TLS come first, sign-on is sixth. But `Build` is an error with no sign-on helper, and `allow:` must not be empty (`resolve/publish.go`). Every published name is behind sign-on, and LAN-only names are listed as not modelled **[docs: join.md]**. The design note says exposure and authorization are different layers (D3, the design in #130); the built join fuses them. Until a name can be published without sign-on, there is no built path for a name that needs only this step: routes for those are written by hand, since self-registration was removed.
2. **join assumes a wildcard certificate.** Its routes are `handle` fragments inside a wildcard site that imports the snippets `sso` and `no_portal_cookie`, which the platform's Caddyfile defines by hand; no Caddyfile in `configs/` does. The public-VPS testdata uses a Caddy build with a DNS provider module, so DNS-01 is required. A certificate per name over HTTP-01, which the removed self-registration used, needs no provider token. Which of the two a site uses is a site fact the join cannot state.
3. **The proxy half of join is the only missing piece for this step.** The diff, the write through the instance file API and the reload were in the removed ingress code; the roadmap's Phase 3 takes them from git history, at the commit before #149, rather than keeping dead code.

### What the user states

The join pattern settled which facts a user has to put in a stack, and where each one lives. This is the interface; everything else is derived. As built, a published web app needs **[code]**:

| Who states it | Fact | Where |
|---|---|---|
| the app | what it offers: a port and a protocol, and the NIC if it has several | `user.tink.offer.<name>.*` on the instance |
| the app | a static address on a managed network, and exactly one ACL attached to that network | the instance's NIC, the network |
| the site | a name, which offer it shows, and who may use it | a `kind: publish` beside the instance |
| the site | the zone, each helper's role, and why each of the ingress helper's NICs is there (LAN, mesh, public) | `user.tink.dns.zone`, `user.tink.role`, `user.tink.leg.<nic>` on the helpers |

The last row is the part a shipped recipe would remove: it is the same for every site of one shape, and only the zone and the address range differ. What remains for a user is the first three rows.

### Direction

Agreed 2026-10-19: the join is a stage of `plan` and `plan apply`, not a command. The end user declares offers and publishes and runs `plan` and `plan apply`; the dns records, proxy routes, sign-on rules, ACL rules and mesh records follow from them, are shown as planned changes, and are written and read back by apply.

1. **Make the join a stage of plan and apply.** After the declarations converge, derive the renderings and plan them as changes to files on the helper instances (a kind the resolver already plans and applies), each shown beside the publish it comes from, with the join's findings beside the resource they concern. See cross-cutting finding 4.
2. **Layer the requirements.** Names (dns and proxy) need only an ingress helper and a zone. Sign-on, the mesh and the ACL are each switched on by their helper existing. `allow:` becomes optional, and its absence is an explicit "open to the LAN" rather than an error. Agreed 2026-10-18.
3. **Add a site-level TLS choice,** wildcard or per name, so a VPS with no DNS provider can publish.
4. **Retire self-registration.** Done: removed outright in #149, with no migration code.
5. **Rename the leftovers.** The command `tink join` goes. A debug or audit view may stay, for what plan cannot show (a target tink does not write, such as a control plane on another host, and a whole-site view across stacks), and is not called join: that word names the pattern, not something a user types.

## 3. Back it up: volume backup

One implementation: a `backup:` block on a `kind: storage-volume`, `kind: backup-target` documents for the places copies go, a scheduler and job executor in the daemon helper, and `tink backup run | restore | verify | forget` by hand. See [volume-backup.md](volume-backup.md).

| Row | volume backup |
|---|---|
| 1 Neutral fact | yes: the volume says what it needs (copies, retention, a verify cadence); the targets are separate site facts, so the same policy works against any target **[code]**. A target's `location` (`same-host`, `other-host`, `offsite`) is the author's claim, and tink cannot check it **[docs]** |
| 2 Stateless | yes: the policy is one key on the volume and every result is a stamp on it (`user.tink.backup.*`); the scheduler works from the volumes, not from the stack **[code]** |
| 3 Plan-time check | yes: `plan` warns about a volume with no declaration, a copy that is overdue or failing, a verification that is stale or missing, and a 3-2-1 shortfall **[code]** |
| 4 Heal or report | copies heal: the scheduler finds the due ones and retries a failure with backoff **[code]**. Verification only reports: `verify.every` is how stale a verification may get before `plan` warns, and nothing in the scheduler enqueues one **[code]** |
| 5 Validated inputs | yes: the policy is parsed strictly, with a protocol number, and a cadence must be daily, weekly or monthly **[code]** |
| 6 Applies, run live | yes: scheduled copies to a pool target and to an Incus remote, run by the helper **[docs]** |
| 7 Recipe-ready | yes: per-volume defaults over a few site targets is the shape the platform recipe proposal (#131) describes **[docs]** |
| 8 Stands alone | partly: Incus-native snapshots and a hand-run `tink backup run` need no helper; scheduled and off-site copies need the daemon helper **[code]** |

### What the scorecard shows

1. **`verify: weekly` reads as a schedule and is a warning threshold.** The keyword is the same one a reader expects to mean "check my backup every week". The documentation says otherwise ("how stale a verification may get before `plan` warns"), but the platform recipe proposal (#131) describes it as "a weekly restore check". The volume page itself says an unverified backup is not a backup. A verification changes no declared state (it restores to a scratch volume and writes a stamp), but it does not fit the helper as it is: the helper's real work needs no right over any instance, and a restore check creates a throwaway one **[lab]**. Scheduling it means a second identity, in a project of its own, and somewhere to record results (see the roadmap, Phase 4).
2. **The scheduled cron expression has no time zone of its own.** Its meaning depends on the helper's `--timezone` flag, which is not part of the policy. Two helpers with different zones would run the same policy at different hours.
3. **Retention is the part least exercised over a real span.** It is unit-tested, but a pruning bug shows only after the declared retention has passed on a live host.
4. **A volume that declares copies but no `verify` is never asked to be verified.** `VerifyWarning` says "no cadence declared, no nag" (`backupmeta/declaration.go`), so leaving the line out is quiet. That is the opposite default from what the page's own claim, that an unverified backup is not a backup, would suggest.
5. **Nothing backs up the other half of a restore.** A volume's data is covered. The stack files and the secrets identity file, which a rebuilt host needs, are not, and no page says where they should be kept.

### Direction

1. Do not schedule restore checks yet. Nag a volume that declares copies and no verification cadence, deliver the `plan` warning through a heartbeat, and revisit once watching exists. A scheduled check is a design of its own (a second identity, a sandbox project, a record of results) and no workload needs it before then.
2. Put the schedule's time zone in the policy or in the site facts, so it travels with the volume.
3. Say, in the volume backup page, where the stack files and the secrets identity belong, and have `plan` mention it when secrets are in use.

## Overview of the rest

A first pass over the remaining steps, from the code. Each is shallower than the three sections above and says so. **[blind]** marks a point taken from an independent read of the source that was given no documentation, and not yet confirmed here.

| Step | What is built | What is not | Largest gap |
|---|---|---|---|
| 1 Run the app | `plan`, `plan apply`, `run`, `export`; dependency levels; image drift and `rebuild` for OCI apps **[code]** | removing what the stack no longer declares, except the one prefix tink owns **[docs]** | `planInstance` does not compare whether the instance is running; the run state is read only for file placement and for rebuild, so a create that fails before the start can plan as "no changes" **[code]**. Also **[blind]**: no `context.Context` or timeout in the resolver or in guest exec, and the plan and apply sequence is assembled in `cmd/tink` |
| 4 Secrets | age-encrypted `secrets.yaml`; `${secret:NAME}` in an instance's `environment.*` only; a redacting writer **[code]** | rotation (there is no re-key command); a delivery that is not an instance config value **[code]** | a value lands in the instance's config in the clear, and what is masked in `plan` and `export` is decided by the key's name (`SensitiveKey`) **[code]** |
| 5 Reach it from outside | a second Incus server as a backup target; `join` renders mesh records **[code]** | the mesh gateway, the control plane and a VPS presence are not created by tink; the mesh records are print-only; the Headscale part is a read-only projection of directory groups into a policy fragment **[code]** | nothing applies the mesh rendering; the credential an applier would run under has been measured (below) but is not yet a product |
| 6 Sign-on, people, groups | a read-only LLDAP reader run through the Incus exec API; `join` renders sign-on rules **[code]** | applying those rules; creating people or groups; the engines' own configuration beyond the shipped Authelia template **[code]** | as in step 5; `kind: access` does not exist |
| 7 Lifecycle | image drift and `rebuild` **[code]** | graceful stop: no code (the design is in #132) **[code]** | an app that must save before it stops loses the write today |

### Cross-cutting findings

1. **Several capabilities stop at "prints what it would write."** `join` renders the proxy routes, the sign-on rules, the ACL rules and the mesh records, and the identity command renders a policy fragment; only `join apply dns` writes. What blocks the rest is not an open question but unfinished work, and the join documentation's open question on the credential an applier runs under is out of date. A lab on a throwaway Incus 7.5.1 server measured the answer **[lab]**: a restricted certificate whose rights come from an authorization scriptlet generated from a table, one narrow identity per job (the daemon's scheduler and executor need ten rules and no instance rights; the dns applier needs file access on one instance), and a restricted project for anything that creates instances. It also found that a helper must hold no edit right on any instance, because that covers its devices. The changes to tink that this called for are merged: the helper reaches the host API over the network, its status is on a custom volume, the shipped scriptlet reads the shape 7.5.1 passes, and `backup verify --check-project` runs the restore check in a project of its own **[code]**. What remains is to move the scriptlet generator (a script outside this repository) into tink, enrol the restricted identities, cut a running helper over, and give each applier its own identity. The daemon helper on a host installed before these changes still holds an unrestricted certificate until that is done.
2. **Declared is not always enforced.** `verify: weekly` is a warning threshold, not a schedule; a publish's `allow` list is rendered into sign-on rules that nothing applies; a target's `location` is a claim. Each is honest in its own page and misleading in a stack file.
3. **The platform is described far more than it is built.** The recipe, `kind: access`, the role helpers and graceful stop are proposals, held in issues (#130, #131, #132) and not in the docs tree. What exists is the converger, backup, `join` for dns, and the read-only identity pieces.
4. **`join` is built as a command, and was designed as a stage of `plan`.** The design note's table says the join is "a computation, reads only: offers, grants and topology in; effective access and findings out", run by "tink, in `plan`", and kept nowhere (the design in #130). What was built is `tink join FILE...` and `tink join apply dns`, each a command of its own, with `plan` knowing only that a publish's instance is in the same stack. The stamps (`user.tink.publish.*`) are the bridge between the two halves: `plan apply` converges the declarations, and `join --from-server` derives from what it left on the server.

   **Direction, agreed 2026-10-19:** make the join a stage of `plan` and `plan apply`: after the declarations converge, derive the renderings from the server's stamps and plan them as changes to files on the helper instances, which is a kind the resolver already plans and applies. The renderings change only when the declarations do, so an operator's `plan apply` is enough to apply them under the operator's own credential, and the narrow identities from the authorization lab are needed only for a helper that applies them unattended. The end user never types `join`. Two things to settle first, and a third to watch:
   - A derived file holds every stack's publishes, so applying one stack rewrites names that belong to others. Plan has to show those changes as changes to files, with their sources.
   - Today any join error prints nothing, because a partial rendering looks complete. In plan, a publish with an error is left out of the derived files and reported as an error finding, and apply still fails.
   - Cross-stack facts (the helpers' roles and legs live in the platform's stack, the publishes in each app's) come from the server, as `--from-server` already reads them, so planning an app stack needs no platform file.
5. **Not repeating values is the same problem as the join.** the accounting in #131 counted every literal in a real platform stack: about fifteen facts, repeated dozens of times, account for every site-specific value; the rest are recipe (conventions and pins), derived from a fact, or generated by the join. It also found that the stack language has no defaults, so each helper repeats the same boilerplate (restart flags, NIC name, memory limit, role stamp, the backup block). Three mechanisms can remove repetition, and each holds a different part **[docs]**:

   | Mechanism | Can hold | Cannot hold |
   |---|---|---|
   | an Incus profile (inherited live) | instance config: restart flags, time zone, role and stack stamps, the root disk | a NIC with its own address (a device does not merge), volume backup policy, file contents, ACLs, secrets; and `plan`, `join` and `export` must first read the expanded config, not the stored one |
   | stack-level defaults in the resolver | the backup block, NIC specifics, `restart: true` on files | nothing in principle; the cost is a second place a value can come from |
   | the recipe (a Go function from site facts to resources) | everything above, plus the offsets, the role graph, the ACLs, the distinguished names derived from the zone | what is not a function of the facts: the zone itself, the LAN addresses, the targets |

   The join and the defaults are one stage seen from two sides: facts in, resources and files out, then diffed against the server. **[proposed, not yet agreed]** One expansion stage before `plan`: the stack's facts and declarations, the role contracts, and the server's stamps go in; expanded resources, derived files and findings come out; `plan` diffs all of it, and `export` and a "show me what my facts expanded to" view read the same result. That view is the natural replacement for `tink join` as a debug view, and is not called join. The accounting note's suggested order is consistent with this: pick one offer path first, make the role contracts data, then the defaults, then the scriptlet generator, then fold `deploy` in.
6. **Outside access and watching are mostly derived artifacts and checks, not machinery.** The remote-access review proposes defaults (a gateway per site, the smallest routes, a default-deny policy kept as code, node keys as secrets, three copies of the control plane's volume) and four checks `plan` could make: a mesh stack with no policy, a gateway whose route covers a bridge that has instances, an auth key in plain config, a gateway without `boot.autorestart` **[docs]**. Policy delivery is already settled: run `policy set` inside the control plane through the exec API, which needs no API key (Headscale's keys have no scope), validated and fail-safe **[lab]**. tink renders a `groups` fragment only; a Headscale server with no policy is allow-all **[code]**. A grant per publish (the door for a group, the bridges for admins, the backup path for the gateway) is one more derived artifact of the join. The watching design (a monitor helper, a heartbeat to something outside the site, a push service, probes through the front door) likewise derives a probe from each publish, and its first stage needs nothing from tink beyond a stack file. What tink lacks today is delivery: a backup copy that fails is visible in `plan` and in the helper's status, and reaches no person.
7. **The single derive stage, checked against the code.** It fits, with prerequisites. What is already there **[code]**:
   - `plan` and `plan apply` already run a series of in-place passes over the loaded resources, in a fixed order: secret expansion, the connection and image qualification in `internal/prepare` (one function for both commands since #125), then, inside `Levels`, the stack's default project, the publish stamps, and project inheritance. The stamping pass (`derivePublishStamps`) is the model for a derive step: it gives the instance a copy of its config that carries the derived keys, so the ordinary instance plan converges them.
   - `PlanOptions` already carries whole-stack analyses into per-resource plans (the backup targets, profiles, ACL names, first-start files, listener overlaps), and `PlannedResource` already has `Blocked`, `Warnings` and `Notes`. A publish with an error maps to a blocked derived item while the others proceed, and `plan apply` already exits non-zero on a blocked item.
   - `join.ApplyDNS` works through a small `Files` interface and returns a report, so the applier is already separate from how the file is reached.

   What stands in the way:
   1. **The pipeline is assembled twice in `cmd/tink`,** once for `plan` and once for `plan apply`, and apply connects to the server a second time inside `ApplyWithOptions`. The order of the passes matters, so a new stage cannot be added in two places. Consolidate the preparation into one function first. *Done (#125):* `internal/prepare.Prepare` is that function; `plan` and `plan apply` both call it and share one connection.
   2. **`join` imports `resolve`,** so `resolve` cannot call `join`. The stage belongs in a package above both, passed into the plan as options the way the helper's policy already is.
   3. **`Build` cannot be given the server's view and the stack's together:** it reports an instance "declared twice". Planning one stack against a server that already holds stamps needs a merge in which the stack wins, outside `Build`.
   4. **`kind: file` is not enough for a derived file.** It compares whole content and reports "content differs". The dns applier adds ownership (a note at the top, `--adopt` for a file tink did not write), a refusal to replace the records with none, a clash check against the zone file, and a read-back. A derived file needs those as plan and apply behaviour, which means a new kind or hook in the resolver, and a new kind touches many places.
   5. **Three of the five renderings have nowhere to go yet.** The proxy routes need an include point in the platform's Caddyfile, the sign-on rules need the engine's configuration split, and the ACL rules clash with `network-acl`, which treats a given direction as the whole list.
   6. **Defaults through profiles need the expanded config,** which `join` does not read (it reads the instance's stored config, so a role stamp in a profile is invisible to it).

   A path that keeps each step useful on its own: **(A)** consolidate the preparation, and add a stack-level `defaults:` pass, which is local to `resolve` and removes the repeated boilerplate without the join; **(B)** run the join's findings as plan-time checks, read-only, with the merge from point 3; **(C)** derived files as planned changes, dns first because its applier and its include point exist; **(D)** remove the `tink join` command and add the expanded-stack view, and let the helper run the same stage unattended where that is wanted.
