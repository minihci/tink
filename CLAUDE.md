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
`deploy`, `run`, `plan` / `plan apply`, `export`, `ingress`, `daemon`, `backup`, `secret`, `remote`, `helper`. It talks to Incus through
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
  only make sense on the host itself (anything reading a storage-pool path, `daemon run`'s ingress half) refuse a remote in
  `cmd/tink/main.go`.
- **The resolver (`internal/resolve`) is the core.** A stack is YAML documents of kinds project, profile, storage-volume,
  instance, file, exec and `stack` (a name only, not a resource). `graph.go` computes dependency levels (resources within a level
  apply concurrently; ties are broken by kind then name so a plan reads the same on every run). Dependencies come from
  name-matching (an instance's project, profiles and volume-backed devices) plus explicit `DependsOn`; deliberately not a
  Terraform-style expression language. `Plan` is pure reads; `apply*.go` converges. Design and rationale are in
  `docs/resolver-architecture.md`.
- **Plan also judges drift and policy**, not just existence: image drift by fingerprint (`volatile.base_image`, not `image.id`,
  see `imagedrift.go`) driven by an instance's `on_image_change`, and each storage volume's declared backup policy
  (`policy.go`, `volumebackup.go`).
- **Volume backup is split across five packages.** `backupmeta` (what is recorded on a volume, the policy and schedule
  arithmetic; depends on nothing else here) is used by `volbackup` (copy/restore/verify), `backuprun` (which copies are due),
  `resolve` (writes the policy onto the volume at `plan apply`) and `daemon` (schedules from the volumes themselves). See
  `docs/volume-backup.md`.
- **The helper is an Incus instance, not a service.** `tink helper install` creates an instance running `tink daemon run`.
  `internal/jobs` is its work queue, a directory where `READY` is written last so a half-pushed job is never read. The helper
  publishes a status document on its own instance config (`user.tink.helper.status`), read back by `internal/helper`. See
  `docs/helper.md` and `docs/daemon-jobs.md`.
- **Secrets never reach a place that prints them.** `${secret:NAME}` is accepted only in an instance's `environment.*` values
  (`resolve/secretrefs.go`); anywhere else it is a load-time error. Values are age-encrypted in a committable `secrets.yaml`
  (`internal/secrets`), and output goes through a redactor. When adding a place that renders config, check it can't leak an
  expanded secret (`maskconfig_test.go`, `cmd/tink/redact_test.go`). See `docs/secrets.md`.
- **`tink deploy` is template-driven.** `internal/bootstrap` renders `${VAR}` placeholders in `configs/` from `deploy.env`.
  `configs/*.env`, `configs/secrets/` and `configs/authelia/users_database.yml` are host-owned and gitignored; never commit them.

## Conventions

- **Metadata keys.** Everything tink reads or writes on an object's config lives under `user.tink.*`.
  `cmd/tink/keynamespace_test.go` scans non-test Go source for string literals starting `"user.` outside that namespace, with
  an allowlist for the three legacy `user.ingress.*` keys.
- **Docs travel with code.** Update the docs a change touches in the same PR, and flip "not yet run / not yet built" claims in
  the README and `docs/` when they stop being true. Package doc comments carry much of the design rationale; read them before
  changing a package.
- **No deprecation period for undeployed features.** If nothing has been deployed with a feature, remove it outright.
- **Releases.** A `v*` tag builds the binaries (`release.yml`) and the helper image (`helper-image.yml`). The version is
  compiled in from the tag, and `tink helper install` with no flag picks the image published for the version it reports.
