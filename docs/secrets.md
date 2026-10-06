# Secrets

Tink keeps secrets in an age-encrypted file that is safe to commit, and lets a stack refer to them as
`${secret:NAME}` instead of containing them. This is **phase 1**: references in an instance's
`environment.*` values. The reasoning, the alternatives, and what is deliberately not here yet are in
[`secrets-design.md`](secrets-design.md).

## Quick start

```
tink secret keygen --add                  # makes an identity (the private key), adds its public key to secrets.yaml
tink secret set immich-db-password        # prompts (no echo), or reads stdin;  --generate makes a random one
```

```yaml
kind: instance
name: immich-db
config:
  environment.POSTGRES_PASSWORD: ${secret:immich-db-password}
```

`tink plan` and `tink plan apply` expand the reference and send the value to Incus. `secrets.yaml` sits beside
the stack file (or give `--secrets FILE`); the identity is `~/.config/tink/identity.txt` (or `$TINK_AGE_IDENTITY`,
or `--identity FILE`).

Commit `secrets.yaml`. **Never** commit the identity. Back the identity up somewhere other than that machine and the
repo: without it the secrets cannot be read.

## The store

`secrets.yaml` holds the public keys that may read it (`recipients`) and one encrypted value per secret. Each value
is encrypted on its own, to every recipient, so changing one secret changes one line. Setting a value needs only the
public keys; reading one needs an identity. The file contains no plaintext.

A value can be read with the plain `age` tool, with no tink involved:

```
base64 -d <<< "<the value from secrets.yaml>" | age -d -i ~/.config/tink/identity.txt | tail -n +2
```

(The first decrypted line, `tink:1:<name>`, binds a value to its name so that two lines swapped by a bad merge are
an error instead of two secrets silently exchanged; `tail -n +2` drops it.)

| Command | |
|---|---|
| `tink secret keygen [--add]` | make an identity and print its public key; `--add` also adds it to the store. Never overwrites. |
| `tink secret set NAME [--generate] [--length N] [--charset alnum\|hex]` | set from stdin or a no-echo prompt, never from a flag (flags land in shell history and `ps`). Values must be at least 6 characters. A trailing newline is dropped; multi-line values (a certificate) work from `< file`. |
| `tink secret list` | names only |
| `tink secret reveal NAME` | print a value: deliberate, for recovery |
| `tink secret rm NAME` | remove (it stays in git history; rotate it if it was ever used) |
| `tink secret recipients [add KEY \| rm KEY]` | list, add or remove a public key; re-encrypts every value |

### A new host

A host that applies the stack needs an identity whose public key is a recipient. No private key ever has to move:

1. On the host: `tink secret keygen` and note the public key it prints.
2. On the machine where you edit the repo: `tink secret recipients add age1...`, commit, push.
3. On the host: pull.

Adding a recipient re-encrypts every value, so **every line of `secrets.yaml` changes** (age ciphertext is randomized):
do it on a branch with nothing else in flight, or expect a conflict if two branches both touch the store.

## Where a reference is allowed

Only in the **value of an instance's `environment.*` config**. Anywhere else, `${secret:` is an error at load time, with the
reason. That is deliberate: it keeps a secret out of places that print or expose it (command lines in `exec`/`incus`
resources and verify checks, instance names, profile and project config, `user.*` keys the guest can read, `kind: file`
content, which is pushed with mode 0644 and after the instance has started).

A `$` directly before the opener makes it literal: `$${secret:not-a-secret}` is the text `${secret:not-a-secret}`.

## What `plan` and `apply` do

- **They never print the value**, or the value it replaces. A changed secret shows
  `config.environment.POSTGRES_PASSWORD: (value hidden) changed`. This is true of any `environment.*` key whose *name* looks
  sensitive (`PASSWORD`, `SECRET`, `TOKEN`, `KEY`, ...) even if no reference is involved, because a diff must not echo a
  password someone typed in plainly.
- **A resource whose secret cannot be used is BLOCKED as a whole**, never partly applied (Postgres initializes once with
  whatever environment it first sees). The message depends on why:

  | Situation | Message tells you to |
  |---|---|
  | no `secrets.yaml` where tink looked | check the directory, or make a store |
  | the store exists but the secret is not in it | `tink secret set NAME` |
  | the secret **is** set but this machine cannot decrypt it | fix the identity. It says plainly **not** to run `tink secret set`, which would overwrite a good secret |

- **A stack with no references never touches the store or an identity**, so `plan` works on a machine that is not trusted
  with the keys as long as the stack needs none.
- **A rotated secret does not reach a running process until the instance restarts.** Incus stores the new environment, but
  the process keeps the one it started with, and the next `plan` will say "no changes". `apply` says so when it happens;
  `restart: true` on the instance makes `apply` restart it. And some software reads a setting only once: Postgres reads
  `POSTGRES_PASSWORD` only when it first initializes its data directory, so changing it later does not change the database.
- Everything tink prints, including errors from Incus, passes through a **redactor** that replaces any secret it has
  decrypted with `***`. It is a net under the places tink already avoids printing a secret, **best effort, not a guarantee**:
  it cannot catch a value tink never decrypted, or one something has transformed beyond base64, hex, URL and JSON forms.

## Putting an existing secret under tink: Immich

The Immich stack has a database password in a placeholder-rendered file and in plain `environment.*` keys. To move it:

1. **Upgrade tink on every host that applies the stack first.** An older tink does not know references and would send the
   literal text `${secret:...}` to Incus as the password.
2. Read the *live* value into the store rather than generating a new one, because Postgres is already initialized with it:

   ```
   sudo incus config get immich-db environment.POSTGRES_PASSWORD | tink secret set immich-db-password
   ```

   `set` strips the trailing newline. **Do not use `--generate` for a database that is already running**: it would replace the
   password in the stack but not in the database.
3. Change the stack to `${secret:immich-db-password}` for both containers.
4. `tink plan` should now say **no changes**: the expanded value equals the live one. Nothing restarts.

## What this does not protect against

- **Root on the host, and anyone who can talk to the Incus socket.** The value has to reach the container, and Incus keeps it
  in plain text: `incus config get` shows it to anyone with Incus access, and root can read a container's environment from
  `/proc`. An ordinary unprivileged user on the host cannot. Tink protects git, its own output, shell history and `ps`, and
  ordinary users. It does not hide a secret from the machine it runs on.
- **Instance snapshots and exports** carry the value (in the instance config), as they carry anything in it.
- **Every recipient can read every secret in a store.** One store is one trust domain: a VPS that should not see Tron's
  secrets should have its own stack and store, not a recipient key in this one. Removing a recipient re-encrypts everything
  but does not take back what they could already decrypt from the repository's history; rotate the secrets too.
- **Anyone who can commit can replace a value with one of their own**, since the recipients are public keys. Review changes to
  `secrets.yaml` as you would any change that decides what a service's password is.
- **`tink run --env NAME=value`** puts the value in your shell history and the process list. `--dry-run` hides it in its own
  output, but `run` is not the way to pass a secret; use a stack.
- **`tink deploy` passes the image registry token on a command line** (`incus remote add --token`); that is a separate,
  existing leak, tracked on its own.

## Not in phase 1

Delivering a secret as a **file** in the guest (for software with `_FILE` variables, such as Immich's `DB_PASSWORD_FILE`
and Postgres's `POSTGRES_PASSWORD_FILE`), which needs `mode`/`uid`/`gid` on `kind: file` and a push before first start;
`kind: secret` declarations with `generate:` policies; secrets for backup targets, restic and verify checks. See
[`secrets-design.md`](secrets-design.md).
