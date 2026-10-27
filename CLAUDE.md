# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

CI (`.github/workflows/ci.yml`) runs exactly these, in order; run them before pushing:

```
gofmt -l .                 # must print nothing
go build ./...
go vet ./...
staticcheck ./...          # go install honnef.co/go/tools/cmd/staticcheck@latest
go test -race ./...
```

```
go build -o tink ./cmd/tink                       # the binary (/tink is gitignored)
go test ./internal/resolve -run TestName -race    # one test; -run takes a regexp
```

Tests need no Incus server: they use injected fakes. Anything that talks to a real daemon is verified by hand against a
disposable host, and the README's per-command "Status" notes record what has actually been run live.

## What tink is

A single Go binary (`cobra`, module `github.com/minihci/tink`) that makes the Mini HCI platform's Incus conventions executable:
`deploy`, `run`, `import`, `plan` / `plan apply`, `export`, `daemon`, `backup`, `secret`, `remote`, `helper`. It talks to Incus through
Incus's own Go client (`github.com/lxc/incus/v7`), not by shelling out. The exceptions are registries, `incus launch` and `incus file push` in
`internal/bootstrap`, which still shell out, and a `kind: incus` resource, which runs the command its author wrote.

## Architecture

`cmd/tink` is a thin cobra layer (argument parsing and guards only). Each capability is a plain importable package under
`internal/`, so a future UI or API server can reuse it. The README's architecture block lists the packages; the parts that take
several files to see are below.

- **One binary, no client/server split.** `tink daemon run` is a subcommand of the same binary. tink holds no state of its own:
  Incus owns everything, and tink diffs live Incus state directly. Don't add a state file or a separate daemon binary (the
  `internal/daemon` package doc says why).
- **The server is chosen once per process.** `--remote NAME` / `$TINK_REMOTE` is applied in `main.go`'s root command and stored
  in process-wide state in `internal/incusapi` (`UseRemote`); every `incusapi.Connect` then goes to that remote. Commands that
  only make sense on the host itself (anything reading a storage-pool path) refuse a remote in
  `cmd/tink/main.go`.
- **`internal/prepare` is the one place the passes run in order.** `plan` and `plan apply` call `prepare.Prepare` (load, defaults, secrets, connect, derive, qualify images, `resolve.Levels`), and apply reuses its connection and levels (`resolve.ApplyPrepared`). `internal/derive` holds the derive stage's passes (today a bridge, its ACL and the apps' static addresses for a stack with `network: own`, then the ingress rules of an ACL with `derive_ingress`, then the region of every `kind: file` with `derive:` (the proxy's routes, the sign-on helper's rules, the dns helper's records; the mesh view is a whole file), from the publishes and the platform on the server); it sits above `resolve` and `join` and `resolve` imports none of it. `plan explain` shows what each pass made by giving `prepare` a `Trace` (`internal/explain`): it compares the resources before and after each pass, so a pass needs nothing added to be explained. A new pass goes into `prepare`, not into either command; the flags both share (`--socket`, `--offline`, `--secrets`, `--identity`) are persistent flags of `plan` (`cmd/tink/stackflags.go`).
- **The resolver (`internal/resolve`) is the core.** A stack is YAML documents of kinds project, profile, storage-volume,
  instance, file, exec, and two that are not resources to converge: `stack` (a name only) and `publish` (a declaration that
  `tink join` reads; `publish.go`). Both are kept out of the graph, which is why a publish may share a name with its instance. `Levels` stamps each publish onto
  its instance (same stack and project) as `user.tink.publish.<name>` (`publishstamp.go`): derived into the instance's desired config, so the ordinary plan converges it. So is each `kind: file` with `derive:` as `user.tink.derive.<file name>` on its instance, and each network ACL with `derive_ingress` as `user.tink.derive.ingress` on the ACL (`derivestamp.go`, no
  guard in either: the daemon never adopts and never empties). Those prefixes, and `user.tink.site`, are what tink OWNS: a live key under one that the stack no longer derives is a planned removal (`run.StaleOwnedKeys`), and nothing else is ever removed. `helper` (`helperkind.go`)
  declares the daemon helper: adopted and only reported on when present, installed (through `internal/helper`) when absent, never upgraded or removed. `graph.go` computes dependency levels (resources within a level
  apply concurrently; ties are broken by kind then name so a plan reads the same on every run). Dependencies come from
  name-matching (an instance's project, profiles and volume-backed devices) plus explicit `DependsOn`; deliberately not a
  Terraform-style expression language. `Plan` is pure reads; `apply*.go` converges. Design and rationale are in
  `docs/resolver-architecture.md`.
- **`tink join` is a pure function of the stack files it is given, or of the server with `--from-server`** (`internal/join`; `server.go` reads the instances, profiles, networks and publish stamps, read-only, and fails on a stamp it cannot read): it writes nothing, and prints
  the dns records, mesh records, proxy routes, sign-on rules and ACL rules a site's `kind: publish` documents imply. Any error means
  nothing is printed (a partial rendering looks like a complete one). `tink join apply dns` (`internal/join/apply.go`) is the one thing
  that writes: the dns helper's generated `/hosts`, through an injected `Files` interface, with refusals instead of guesses (errors,
  zero publishes, a clash with the zone file, a file tink did not write, a failed read) and a read-back. `apply dns --from-server` is what the daemon helper will run. `join.ReconcileDerived` (`derived.go`) keeps the stamped derived files the same way, from the stamps and with `plan`'s guards (`resolve.CheckDerived`). Its golden outputs in `internal/join/testdata/golden` are written
  by hand, not generated. It reads **one site** at a time: `site: NAME` on `kind: stack` is stamped as `user.tink.site` on the stack's role helpers and published instances (`resolve/site.go`, an owned key like the publish stamps), `join.FromServer`/`PlatformFromServer` take the site and leave other sites' instances unread, `join.ForSite` cuts files the same way, and the derive passes use the planned stack's site. No site is the unnamed site, which is how a server behaved before sites. See `docs/join.md#sites`.
- **Plan also judges drift and policy**, not just existence: image drift by fingerprint (`volatile.base_image`, not `image.id`,
  see `imagedrift.go`) driven by an instance's `on_image_change`, and each storage volume's declared backup policy
  (`policy.go`, `volumebackup.go`).
- **Volume backup is split across six packages.** `backupmeta` (what is recorded on a volume, the policy and schedule
  arithmetic; depends on nothing else here but `backupext`) is used by `volbackup` (copy/restore/verify), `backuprun` (which copies
  are due) and `daemon` (schedules from the volumes themselves). `resolve` does not import it: it asks `backupext.Get()`, the
  interface `backupmeta` registers in its `init` (the declaration types live in `backupext` too), to write the policy onto the
  volume at `plan apply`. `resolve/imports_test.go` fails if the converger imports a package that is not on its list; do not add a
  capability to it. See `docs/volume-backup.md`.
- **Ownership is one stamp, and it is not part of a diff.** `user.tink.stack` (`internal/ownership`) is written in the creation request of every kind tink
  creates for a named stack (project, profile, storage volume, instance, network, network ACL, image properties), and never by an ordinary apply on an object that
  exists. It is its own dimension of a plan (`PlannedResource.Ownership`, read from the live object by `observed` and classified in `planKind`), so it is never a
  change line, an action or an exit status; `plan` ends with one line for declared objects that exist with no stamp. `plan apply --adopt` (or `ownership:
  adopt-on-apply` on `kind: stack`) is a pass of its own, `resolve/adopt.go`, with a narrow stamp-only write that must not read `Restart` or change an instance's
  state (a test fails if it does). The key is refused in `config:` of every kind and never exported. Phase 1 of #133 only; orphans and prune are not built. See
  `docs/ownership.md`.
- **The helper is an Incus instance, not a service.** `tink helper install` creates an instance running `tink daemon run`.
  `internal/jobs` is its work queue, a directory where `READY` is written last so a half-pushed job is never read. The helper
  publishes a status document as a config key (`user.tink.helper.status`) on a custom volume that its instance points at
  (`user.tink.helper.status-volume`, `POOL/VOLUME`; a volume has no devices, so the helper needs no edit right on any instance), read back by
  `internal/helper` (helpers installed before that keep it on their instance). See
  `docs/helper.md` and `docs/daemon-jobs.md`.
- **Secrets never reach a place that prints them.** `${secret:NAME}` is accepted only in an instance's `environment.*` values
  (`resolve/secretrefs.go`); anywhere else it is a load-time error. Values are age-encrypted in a committable `secrets.yaml`
  (`internal/secrets`), and output goes through a redactor. When adding a place that renders config, check it can't leak an
  expanded secret (`maskconfig_test.go`, `cmd/tink/redact_test.go`). See `docs/secrets.md`.
- **`tink deploy` is template-driven.** `internal/bootstrap` renders `${VAR}` placeholders in `configs/` from `deploy.env`.
  `configs/*.env`, `configs/secrets/` and `configs/authelia/users_database.yml` are host-owned and gitignored; never commit them.

## Conventions

- **Metadata keys.** Everything tink reads or writes on an object's config lives under `user.tink.*`.
  `cmd/tink/keynamespace_test.go` scans non-test Go source for string literals starting `"user.` outside that namespace.
- **Docs travel with code.** Update the docs a change touches in the same PR, and flip "not yet run / not yet built" claims in
  the README and `docs/` when they stop being true. Package doc comments carry much of the design rationale; read them before
  changing a package.
- **This repository is public.** Never put a real hostname, domain, address, account, or a description of where something
  runs or what is weak about a particular site in code, tests, fixtures, golden files, docs, commit messages, or PR and issue
  text. Use documentation values (`example.org`; `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`) and generic words ("a
  homelab host", "a public VPS"). A measurement taken on a real host cites a lab ID (for example "L-2026-10-17") and states
  the software version; the host's name and the raw output stay in the author's private notes. Read PR and issue text before
  posting it, because it is public too.
- **No deprecation period for undeployed features.** If nothing has been deployed with a feature, remove it outright.
- **Before 1.0, fix hosts with a page, not code.** When a rename or removal touches something deployed (config keys, CLI
  names, an image name), write an agent-followable page that says how to find the affected instances, fix them and test the
  result, in the style of `docs/removals/ingress-self-registration.md`, and make the plain change in code. Do not write
  migration or compatibility code; fixing the few places that need it costs less than carrying lines that run once.
- **Releases.** A `v*` tag builds the binaries (`release.yml`) and the helper image (`helper-image.yml`). The version is
  compiled in from the tag, and `tink helper install` with no flag picks the image published for the version it reports.

## Working in GitHub

Issues are the plan and the log; a PR is the change. Anything bigger than one change is planned in issues before the first PR.

**Parent and sub-issues** (#254 with #255 to #258 is the model).
- The parent's body is the stable part: what was looked at, what is already right, the gaps, a "Not gaps (decided)" list so they are not found
  again, and "Done when". Change it only when the scope changes, and say why in a comment. It closes when its sub-issues do.
- A sub-issue says what, the evidence, and a "Done when" that a test or a command can answer. Link it with
  `gh api -X POST repos/minihci/tink/issues/PARENT/sub_issues -F sub_issue_id=ID` (ID is the issue's numeric `id`, not its number); order with
  "blocked by".
- A side thought goes into a `[parked]` proposal issue, not into the change in hand.
- Show the owner the titles and bodies before filing: filing is public.

**Check the premise first.** An issue claims something about the code or a dependency. Read the code (a dependency's source too) before changing
anything, and record what was found with file and line. If the premise is wrong, say so and close or reshape the issue (#256: the Incus client
already names a missing extension).

**Comments are the log.** A later reader, or another machine, learns what happened from the issue. Post as it happens, one comment per event,
dated and starting with its kind:
- *Plan*: the approach, the files, the live check, what is out of scope. Before work; shown to the owner first.
- *Finding*: what was expected, what was found, tagged [code], [docs], [lab] (with the software version) or [proposed], and what it changes.
- *Measurement*: method, software version, a table, what was not measured. Raw output stays in the private notes; the comment cites a lab ID.
- *Decision*: what was chosen, what was dropped and why, who decided.
- *Correction*: an earlier comment or the issue was wrong. A new short comment, not an edit; shown to the owner first.
- *Closing*: what changed (the PR), what was left on purpose, when to reopen. Close "completed" when the work was done, measuring included, and
  "not planned" when it was decided not to be a gap.
A PR description says what the change is and links the comments. It is not the only place a finding is written.

**Pull requests.**
- One branch from `main` per sub-issue; stack only when one needs the other, and then merge bottom-up with merge commits.
- The title ends with the issue number. The body says `Closes #SUB` and `Part of #PARENT`, what changes, the tests, what was checked (the CI
  commands), and, plainly, what was run live, what only on fakes, and what was not run.
- The milestone and the `area:` label are the issue's. Docs go in the same PR.
- Show the owner the description before opening. Never merge: merging waits for the owner, per PR.
- After a merge: sync `main`, delete the branch, update #218.

**Labels and milestones.** A milestone is an arc toward 1.0; each issue has one `area:` label (`area:core`, `area:daemon`, `area:service`), and
`proposal`, `parked`, `removal` or `bug` where they apply. Move an issue out of a milestone before closing it. An issue found mid-work starts with
"Found while: #N" and is listed on #218 under Open detours.

**#218 (where we are)** is the status page: read it first, update it last, without asking.

**Public text.** Every issue, comment, PR body and commit message is public: the rule under Conventions applies, and the check script in the
private notes (`hooks/check-text.sh`, installed by `hooks/install.sh`) runs on each before it is posted.

**Git identity.** If none is configured, set `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME` and `GIT_COMMITTER_EMAIL` for the commit to
`Morgan` and `33681253+m16d@users.noreply.github.com`. Never `git config`, and never the account email (it would sit in public history).
