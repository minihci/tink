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
`deploy`, `run`, `plan` / `plan apply`, `export`, `daemon`, `backup`, `secret`, `remote`, `helper`. It talks to Incus through
Incus's own Go client (`github.com/lxc/incus/v7`), not by shelling out. The exceptions are registries and `incus launch` in
`internal/bootstrap`, which still shell out.

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
- **`internal/prepare` is the one place the passes run in order.** `plan` and `plan apply` call `prepare.Prepare` (load, secrets, connect, qualify images, `resolve.Levels`), and apply reuses its connection and levels (`resolve.ApplyPrepared`). A new pass goes into `prepare`, not into either command; the flags both share (`--socket`, `--offline`, `--secrets`, `--identity`) are persistent flags of `plan` (`cmd/tink/stackflags.go`).
- **The resolver (`internal/resolve`) is the core.** A stack is YAML documents of kinds project, profile, storage-volume,
  instance, file, exec, and two that are not resources to converge: `stack` (a name only) and `publish` (a declaration that
  `tink join` reads; `publish.go`). Both are kept out of the graph, which is why a publish may share a name with its instance. `Levels` stamps each publish onto
  its instance (same stack and project) as `user.tink.publish.<name>` (`publishstamp.go`): derived into the instance's desired config, so the ordinary plan converges it. That prefix is the one
  config prefix tink OWNS: a live key under it that the stack no longer derives is a planned removal (`run.StaleOwnedKeys`), and nothing else is ever removed. `helper` (`helperkind.go`)
  declares the daemon helper: adopted and only reported on when present, installed (through `internal/helper`) when absent, never upgraded or removed. `graph.go` computes dependency levels (resources within a level
  apply concurrently; ties are broken by kind then name so a plan reads the same on every run). Dependencies come from
  name-matching (an instance's project, profiles and volume-backed devices) plus explicit `DependsOn`; deliberately not a
  Terraform-style expression language. `Plan` is pure reads; `apply*.go` converges. Design and rationale are in
  `docs/resolver-architecture.md`.
- **`tink join` is a pure function of the stack files it is given, or of the server with `--from-server`** (`internal/join`; `server.go` reads the instances, profiles, networks and publish stamps, read-only, and fails on a stamp it cannot read): it writes nothing, and prints
  the dns records, mesh records, proxy routes, sign-on rules and ACL rules a site's `kind: publish` documents imply. Any error means
  nothing is printed (a partial rendering looks like a complete one). `tink join apply dns` (`internal/join/apply.go`) is the one thing
  that writes: the dns helper's generated `/hosts`, through an injected `Files` interface, with refusals instead of guesses (errors,
  zero publishes, a clash with the zone file, a file tink did not write, a failed read) and a read-back. `apply dns --from-server` is what the daemon helper will run. Its golden outputs in `internal/join/testdata/golden` are written
  by hand, not generated. See `docs/join.md`.
- **Plan also judges drift and policy**, not just existence: image drift by fingerprint (`volatile.base_image`, not `image.id`,
  see `imagedrift.go`) driven by an instance's `on_image_change`, and each storage volume's declared backup policy
  (`policy.go`, `volumebackup.go`).
- **Volume backup is split across five packages.** `backupmeta` (what is recorded on a volume, the policy and schedule
  arithmetic; depends on nothing else here) is used by `volbackup` (copy/restore/verify), `backuprun` (which copies are due),
  `resolve` (writes the policy onto the volume at `plan apply`) and `daemon` (schedules from the volumes themselves). See
  `docs/volume-backup.md`.
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
- **Releases.** A `v*` tag builds the binaries (`release.yml`) and the helper image (`helper-image.yml`). The version is
  compiled in from the tag, and `tink helper install` with no flag picks the image published for the version it reports.
