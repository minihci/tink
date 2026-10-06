# Secrets: design

**Status: design, not implemented.** Nothing here exists yet except the problems it solves.

Claims are marked **[verified]** (tried live on a real host while writing this), **[docs]** (read in upstream
documentation), **[code]** (read in this repo), or **[hypothesis]** (believed, to be checked before relying on it).

## Why now

Tink has no story for secrets, and the real stack it runs already needs one. Concrete cases from using it:

| # | Need | Today |
|---|---|---|
| R1 | Immich's Postgres password, shared by two containers | `sed`-rendered from a placeholder into a file on the host, then stored as plain `environment.DB_PASSWORD` in Incus config |
| R2 | Credentials for a remote Incus server (client cert and trust token) for backup copies | no mechanism |
| R3 | restic repository password and S3/B2 keys for the off-site engine | no mechanism |
| R4 | A `backup.verify.check` that needs credentials (e.g. to open a database) | no mechanism |
| R5 | The TrueNAS API key behind an Incus `truenas` pool | lives in Incus pool config; appeared in plain text in Incus error output and in process arguments **[verified]** |
| R6 | Keeping secrets out of the repo while the stack YAML stays in it | placeholder + `sed` step, by hand |

### Leaks in tink itself today

- `plan` prints **both the old and the new value** of any changed config key: `config.%s: %q -> %q`
  (`internal/resolve/plan.go`). A changed `environment.DB_PASSWORD` would be printed in clear. **[code]**
- `kind: file` always pushes with mode `0644` (`internal/resolve/apply.go`), so a secret written that way would be
  world-readable inside the container. **[code]**
- `tink run --env K=V` writes straight into `environment.*` (`internal/run/flags.go`) and so lands in shell
  history and the host process list. **[code]**
- Errors from Incus or its drivers can echo values (R5 is a live example). Tink wraps and prints them. **[verified]**

## Threat model, stated plainly

**Protected against:** the secret appearing in git, in `plan`/`apply`/`run` output and logs, in shell history, in
`ps`, and to ordinary (non-root, non-`incus-admin`) users on the host.

**Not protected against: root on the host, or anyone with the Incus socket.** Checked on a real Incus host:

| How a secret reaches the container | Who can read it afterwards |
|---|---|
| `environment.X` config key | anyone with Incus API access, via `incus config get` / `show` **[verified]**; root on the host via `/proc/<pid>/environ`; not an ordinary host user **[verified]** |
| a file pushed into the container | a file in the container's root filesystem: readable by root on the host; **not** an ordinary host user (mode `0400` root, and the directory is root-only) **[verified]** |

Two further facts that shape the design **[verified]**:

- Pushing a file works on a **stopped** container, with the requested mode honored. So *create, push, start* is
  possible, and a secret file can exist before the app first starts.
- In an OCI container `/run` is an ordinary directory on the root filesystem; only `/dev/shm` is tmpfs. A pushed
  file therefore **survives restarts and is included in instance snapshots**. There is no free "tmpfs for secrets".

So: delivery to a container is at-rest plaintext on the host in both modes, protected by host permissions. The
store (below) is what keeps secrets out of git. Tink should say this in its docs rather than imply more.

## Design

### 1. A secret store of age-encrypted values, committed next to the stack

`secrets.yaml` beside `tink.yaml`: a map of secret name to an [age](https://github.com/FiloSottile/age)-encrypted
value, **each value encrypted on its own**. Names are visible, values are not. Per-value encryption gives clean git
diffs and merges (one changed secret is one changed line), unlike one encrypted blob.

```yaml
# secrets.yaml -- safe to commit
immich-db-password: age1:YWdlLWVuY3J5cHRpb24ub3JnL3YxCi0+IFgyNTUx...
macpro-incus-client: age1:YWdlLWVuY3J5cHRpb24ub3JnL3YxCi0+IFgyNTUx...
```

- **Recipients** (public keys) are listed in the file, so several machines can decrypt: the person's Mac to edit,
  and each host that applies the stack. The matching **identity** (private key) lives outside the repo: `$TINK_AGE_IDENTITY`,
  else `~/.config/tink/identity.txt` (mode 0600). It is the one thing that must be backed up elsewhere; lose it and
  the secrets are gone, though they can be regenerated and rotated.
- **Library, not a subprocess: `filippo.io/age`.** It is pure Go and pulls in about 11 modules (mostly `golang.org/x`);
  an encrypt/decrypt round trip with armor is about 20 lines **[verified]**. The alternative, **sops**, needs 340 modules
  for just its decrypt entry point and grows the binary from 19 MB to 60 MB **[verified]**; it is the wrong shape for a
  single small binary. age ciphertext also stays readable by the plain `age` CLI, so there is an escape hatch with no tink
  involved.
- Rejected for the first version: Vault / 1Password / Bitwarden CLIs (a service or an account to depend on), and
  host-local plaintext files (no repo story, nothing to restore from). A pluggable resolver can add `op://`-style
  backends later without changing how secrets are *referenced*.

### 2. Declaring a secret: `kind: secret`

```yaml
kind: secret
name: immich-db-password
generate: {length: 32, charset: alnum}   # optional: how `tink secret init` creates it
```

A `kind: secret` says "this stack needs a secret called this". `plan` reports it as **set**, **unset**, or
**unset but generatable**. A resource that references an unset secret is **BLOCKED**, with the command that fixes it.

**Apply never writes the store.** Generating on first apply would mean `apply`, running on the host, modifies a
git-tracked file in a checkout that is not the one you edit. Instead creation is an explicit step run where the repo is
edited:

```
tink secret init                    # create every missing secret that declares `generate:`
tink secret set NAME                # read the value from stdin (never from a flag: history and ps)
tink secret list                    # names, and set / unset; never values
tink secret rotate NAME             # explicit; a new generated value
tink secret rm NAME
tink secret reveal NAME             # deliberate, for recovery; prints the value and says so
tink secret rekey                   # re-encrypt every value after the recipients change
```

### 3. Referencing a secret: `${secret:NAME}`

Expanded at plan/apply time into specific fields, never at load, so the YAML stays inert and reviewable:

| Allowed in | Not allowed in (load-time error, with the reason) |
|---|---|
| instance `config` values (e.g. `environment.*`) | names, `image`, device `source`s |
| `kind: file` `content` | `command` / `check` argv of `exec` and `incus` resources, and verify checks: argv is visible in the guest's and the host's process lists and in logs |

This is deliberately narrow and grep-able. Anything outside the list is rejected rather than quietly leaking.

### 4. Delivery: environment or file

**Environment** is the simplest: `environment.DB_PASSWORD: ${secret:immich-db-password}`. Visible through the Incus
API (see the threat model), and what most apps without file support need.

**File** keeps the value out of `incus config show` and out of the environment of every process in the container:

```yaml
kind: instance
name: immich-server
secrets:
  - {name: immich-db-password, path: /run/secrets/db_password, mode: "0400"}
config:
  environment.DB_PASSWORD_FILE: /run/secrets/db_password
```

Tink pushes the file after create and **before first start** (verified possible above), mode `0400`, owner root unless
`uid`/`gid` is given. This works wherever the app has the `_FILE` convention: Immich documents `DB_PASSWORD_FILE` and the
other database variables **[docs]**, and the official Postgres image documents `POSTGRES_PASSWORD_FILE` **[docs]**. `kind: file`
gains `mode`, `uid` and `gid` (it hard-codes 0644 today), which also makes it usable for non-secret files that should not
be world-readable.

**Recommended default:** files where the app supports `_FILE`, environment where it does not.

**Rejected for the first version: a host-side tmpfs bind-mounted into the container**, which would keep secrets out of
snapshots. Autostarted instances come up at boot before anything runs tink, so nothing would be there to re-create the
tmpfs contents. **[hypothesis]** Worth revisiting if snapshots containing secrets turn out to matter.

### 5. How `plan` and `apply` treat secrets

- **Stateless drift still works.** `plan` decrypts, reads the live value (the config key, or the file's content), and
  compares. It reports `config.environment.DB_PASSWORD: (secret) differs`, **never either value**.
- **No identity available?** `plan` still runs; secret-bearing keys are reported `unverified (no identity)` and apply
  refuses to touch them. `plan` stays usable on a machine that is not trusted with the keys.
- **A redactor over all output.** Every decrypted value (and its base64 and URL-encoded forms) is registered, and anything
  tink prints (`plan`, `apply`, `run`, wrapped errors) passes through it, replacing matches with `***`. This is the
  defense for leaks tink does not control, such as an Incus driver echoing a credential in an error (R5).
- **Rotation** is "change the value, apply": the diff is shown as above, config or file is updated, and the instance restart
  follows the existing `restart:` rules. Tink does not rotate on its own.
- **Existing diffs are fixed regardless of the store.** `diffConfig` stops printing values for any key whose desired value
  came from a secret, and prints `(changed)` for keys named like `*PASSWORD*`, `*SECRET*`, `*TOKEN*`, `*KEY*` as defense in
  depth. (The name heuristic is a policy choice; see open questions.)

### 6. How each need maps

| # | Mechanism |
|---|---|
| R1 | `kind: secret` with `generate`; delivered as files to Immich (`DB_PASSWORD_FILE`) and Postgres (`POSTGRES_PASSWORD_FILE`), or as env. Removes the `sed` step. |
| R2 | A secret holding the client cert+key (and token); a `backup-target` names it (`credentials: {secret: ...}`); tink materialises it as a 0600 file under its own config dir only for the duration of use. Detail belongs to the copy-engine slice. |
| R3 | The off-site job instance is created by tink, so it uses the same instance-secrets path: `RESTIC_PASSWORD_FILE` plus S3/B2 keys as files or env. |
| R4 | `backup.verify.check` gains `secrets:` (and env) with the same semantics, delivered to the throwaway instance; never argv. |
| R5 | Not tink's to store: it is Incus pool config. Tink's contribution is the redactor, and docs advising a narrowly scoped key. |
| R6 | `${secret:...}` in committed YAML replaces the placeholder; the store is the committed, encrypted counterpart. |

## Phasing

Each step is useful on its own.

1. **Store and references.** `filippo.io/age`, `secrets.yaml`, the `tink secret` commands, `kind: secret`, `${secret:}` in
   config values and `kind: file` content, environment delivery, the redactor, and the `diffConfig` fix. Migrates the Immich
   password off the `sed` step.
2. **File delivery.** `secrets:` on instances, `mode`/`uid`/`gid` on `kind: file`, create-push-start ordering, drift by reading
   the file.
3. **Hooks for the backup work.** Target credentials (R2), the restic job instance (R3), and verify-check secrets (R4).

## Open questions

- **Identity distribution.** Each applying host needs the identity file; where does it come from on a fresh VPS, and should an
  identity be passphrase-protected (age supports it, but then apply cannot run unattended)?
- **One store per stack, or shared?** Several stacks (Tron, VPSes) may share a secret or must not. A store per stack directory is
  simplest; sharing then means copying, which is a reason to keep secrets few and generated.
- **The name heuristic.** Masking by key name (`*PASSWORD*`...) catches mistakes (a plain value in a secret-looking key) but is a
  guess; the alternative is only masking what came from `${secret:}`.
- **`tink run --env`.** Add `--secret-env NAME=secret`, or document `run` as not for secrets?
- **Reading a live env secret to compare it** requires the same Incus access tink already has, but means decrypted values sit in
  tink's memory during `plan`. Compare by hash instead? That needs a stored hash, which is state tink avoids.
- **Backups of instances and exports contain the delivered secret** in both modes (config for env, rootfs for files). Volumes, which
  the backup feature backs up, do not, unless an app writes it there.
