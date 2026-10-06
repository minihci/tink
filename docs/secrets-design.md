# Secrets: design

**Status: phase 1 is implemented** (see [`secrets.md`](secrets.md) for how to use it). This document is the
reasoning: why, what was decided, what was rejected, and what is left. It was reviewed adversarially against the code
by a separate agent before phase 1 was built; the [review outcomes](#what-the-review-changed) are recorded below.

Claims are marked **[verified]** (tried live; the commands are in the [appendix](#appendix-how-the-claims-were-verified)),
**[docs]** (read in upstream documentation), **[code]** (read in this repo), or **[hypothesis]** (believed, to be checked).

## Why

Tink had no story for secrets, and the real stack it runs already needed one:

| # | Need | Before this work |
|---|---|---|
| R1 | Immich's Postgres password, shared by two containers | `sed`-rendered from a placeholder into a file on the host, then plain `environment.DB_PASSWORD` in Incus config |
| R2 | Credentials for a remote Incus server (client cert and trust token) for backup copies | no mechanism |
| R3 | restic repository password and S3/B2 keys for the off-site backup engine | no mechanism |
| R4 | A `backup.verify.check` that needs credentials | no mechanism |
| R5 | The TrueNAS API key behind an Incus `truenas` pool | lives in Incus pool config; appeared in plain text in Incus error output and in process arguments **[verified]** |
| R6 | Keeping secrets out of the repo while the stack YAML stays in it | placeholder plus a `sed` step, by hand |

### Leaks in tink itself, found while designing

- `plan` printed **both the old and new value** of any changed config key, and `apply` printed the same lines. **[code]**
  *(fixed: phase 1)*
- The image-drift report printed the live and image values of undeclared `environment.*` keys, and `tink run --dry-run` printed
  the whole config it would set. **[code]** *(fixed: phase 1)*
- Stack YAML decoding ignored fields it did not know, so a misspelled or misindented key, or a field from a newer tink, was dropped
  silently. **[code]** *(fixed: phase 1; also seen for real when a stack using a newer field loaded on an older binary and did nothing)*
- `kind: file` always pushes with mode `0644`, after the instance has started. **[code]** *(why file content cannot carry a secret yet)*
- `tink run --env K=V` writes the value into `environment.*`, and so passes it on a command line. **[code]**
  *(documented as not for secrets)*
- `tink deploy` passes the image-registry token to `incus remote add --token` on a command line. **[code]**
  *(separate, existing; tracked on its own)*
- Errors from Incus or its drivers can echo values (R5 is a live example). **[verified]** *(the redactor is a net for this)*

## Threat model, stated plainly

**Protected against:** the secret appearing in git, in `plan`/`apply`/`run` output and logs, in shell history, in `ps`, and to
ordinary (non-root, non-`incus-admin`) users on the host.

**Not protected against: root on the host, or anyone with the Incus socket.** The value has to reach the container and Incus
holds it **[verified]**:

| How it reaches the container | Who can read it afterwards |
|---|---|
| `environment.X` config key | anyone with Incus API access, via `incus config get`/`show`; root on the host via `/proc/<pid>/environ`; not an ordinary host user |
| a file pushed into the container | a file in the container's root filesystem: readable by root, not by an ordinary host user |

Two more facts that shape the design **[verified]**: pushing a file works on a **stopped** container with the mode honored (so
*create, push, start* is possible), and in an OCI container `/run` is an ordinary directory on the root filesystem (only `/dev/shm`
is tmpfs), so a pushed file **survives restarts and is included in instance snapshots**. There is no free "tmpfs for secrets".

So delivery is at-rest plaintext on the host in both modes, protected by host permissions. The store is what keeps secrets out of
git. Tink says this in its docs rather than implying more.

**Within the store, there is no least privilege.** Every recipient can decrypt every secret in a store; one store is one trust
domain. And because recipients are public keys, anyone who can commit can write a value of their own. Both are stated in
`secrets.md`; neither is solved by cryptography here.

## What was built (phase 1)

**A store** of age-encrypted values, committed beside the stack: `secrets.yaml` with `recipients:` (public keys) and `secrets:`
(name to base64 age ciphertext). Each value is encrypted separately to every recipient (clean diffs; independently readable).
The decrypted payload begins with a `tink:1:<name>` line that is verified on read, so a swap of two lines is an error rather than a
silent exchange of two secrets. The identity (private key) is `$TINK_AGE_IDENTITY` or `~/.config/tink/identity.txt`; tink refuses a
file group- or other-readable, and never overwrites one. No passphrase, so `apply` runs unattended. A new host generates its own
identity and its public key is added from the editing machine, so no private key ever travels.

**Library: `filippo.io/age`.** Adding it to this repo added two modules (`age`, `hpke`) because the rest were already
dependencies; in a fresh module it pulls in about 11 **[verified]**. The alternative, **sops**, needs 340 modules for its decrypt
entry point alone and grows a binary from 19 MB to 60 MB **[verified]**. The store is also plain age: the real `age` CLI reads a
value (`base64 -d | age -d -i ID | tail -n +2`) **[verified]**.

**References:** `${secret:NAME}`, accepted **only in an instance's `environment.*` values**. Validation walks every string-ish field of
every resource and rejects a reference anywhere else, so the list of safe places is an allowlist and a field added later is covered by
default. A `$` before the opener makes it literal.

**Expansion** happens once, after loading and before the graph is built, into fresh copies (the input is never mutated, no maps are
shared), and records which config keys held a secret so no diff prints them. A stack with no references never opens the store or an
identity.

**Unresolved secrets block the whole resource**, never part of it, with different messages for *no store here*, *not set*, and *set but
cannot be decrypted here*. The last must not suggest `tink secret set`, which would overwrite a good secret. A guard blocks any resource
that still holds an unexpanded reference, so the literal text `${secret:...}` can never be pushed as a password.

**Output:** sensitive-looking `environment.*` keys (`PASSWORD`, `SECRET`, `TOKEN`, `KEY`, ...) print `(value hidden)` on both sides of
a change whether or not a reference is involved; and everything tink prints, including the final error, passes through a redactor of
every decrypted value. The redactor is **best effort**, a net under places tink already avoids printing a secret.

**Strict YAML:** unknown fields are an error.

## What the review changed

An adversarial review of the first version of this document against the code found these; each is how it was resolved.

| Finding | Resolution |
|---|---|
| Secrets in `kind: file` content in phase 1, but the 0644 mode fix and push-before-start ordering are phase 2 | **Env only in phase 1.** Reference in file content is a load error. |
| Masking only "the desired side" misses the old value, `apply`'s `updated (%v)`, the image-drift report, and `run --dry-run` | Mask at the source, **both sides**, plus the drift report and the dry run; a name heuristic as well as provenance. |
| The "allowed fields" list was too wide (`user.*`, `oci.entrypoint`, `cloud-init.*`, profile/project config) and not an enforced allowlist | **Allowlist of exactly one place**, enforced by reflection over every field. |
| Expansion placement and failure semantics undefined; `applyOne` plans then creates with the same resource; `Levels` shares maps | Expand once, up front, into copies; carry provenance and problems on the resource; the plan guards block unresolved or unexpanded. **Whole-resource block** (the doc had contradicted itself). Added the **"set but cannot decrypt"** state. |
| The redactor was overclaimed; R5's value is never registered; encodings incomplete; main's `os.Exit` skips the flush; guest exec output goes into errors | Documented as **best effort**; added hex and JSON forms; stdout, stderr and the final error all wrapped and flushed (tested on each path). R5 is stated as out of reach. |
| Least privilege: every recipient reads everything; age gives no authenticity, so ciphertext is not bound to the name | **Name bound inside the payload.** Trust domain and committer-can-substitute stated plainly. |
| Rotation: an env change on a running instance is written but nothing restarts, and the next plan reads converged; Postgres reads its password once; `init` on a deployed R1 would cause an outage | `apply` now says "not restarted"; docs call out Postgres; the Immich migration reads the *live* value into the store and says not to use `--generate` on a running database. |
| Defer `kind: secret`: collides in the global name map with instances, needs new cases in many switches, and cross-resource checks per-resource `Validate` cannot do | **Deferred.** An unset or misspelled reference gives the same BLOCKED outcome without it; `set --generate` covers generation. |
| Silent YAML field drops | `KnownFields(true)`. |
| A missing `secrets.yaml` opens empty, so a wrong directory reports everything "unset" | `Store.Exists()`; "no secret store at PATH (wrong directory?)" is its own message. |
| Existing argv leak in `tink deploy` | Out of scope; spun off as its own task. |
| Several [verified] claims had no evidence in the repo | The [appendix](#appendix-how-the-claims-were-verified) records the commands. |

Considered and **not** taken: dropping `reveal` (kept: it is how a value is recovered, and it is plainly named) and the extra
encodings (kept: they are cheap); a `--file` flag for multi-line values (kept simple: `set NAME < file` works).

## Later phases

2. **File delivery.** `secrets:` on instances, pushed after create and **before first start**, mode `0400`; `mode`/`uid`/`gid` on
   `kind: file`; drift by reading the file. Right for software with `_FILE` variables: Immich documents `DB_PASSWORD_FILE` and the other
   database variables **[docs]**, and the Postgres image documents `POSTGRES_PASSWORD_FILE` **[docs]**. Rejected for now: a host-side tmpfs
   bind-mounted in (it would keep secrets out of snapshots, but autostarted instances come up before anything runs tink to re-create it)
   **[hypothesis]**.
3. **Backup hooks.** Target credentials (R2), the restic job instance (R3), and verify-check secrets (R4), all delivered through the same
   instance-secret path; never argv.
4. **`kind: secret`** with `generate:` policies and an unused/missing report, if the reasons for deferring it go away.

## Open questions

- Identity on a fresh VPS: the manual round trip (generate there, add the key, pull) is documented; is there a better bootstrap?
- A store per stack directory is simplest; sharing a secret across stacks means copying it.
- Rekeying rewrites every line of the store, so two branches that touch it conflict. Acceptable, or worth a smarter format?
- Whether `tink run` should grow a way to take a secret at all.

## Appendix: how the claims were verified

**Delivery and visibility** (a throwaway Incus project on a real host; an OCI Alpine container with `oci.entrypoint=sleep`):

```
incus config set c environment.APP_PASSWORD=<value>; incus config get c environment.APP_PASSWORD     # readable via the API
incus file push ./secret c/etc/app-secret --mode 0400 --uid 0 --gid 0     # while the container is STOPPED: succeeds
incus start c; incus exec c -- ls -ln /etc/app-secret                      # -r-------- 0 0, content present
incus exec c -- mount | grep -E ' /run | /tmp | /dev/shm '                 # only /dev/shm is tmpfs; /run is not
incus file push ./secret c/run/secrets/app; incus restart c; ls /run/secrets/app   # still there
incus snapshot create c s1; find <pool>/containers-snapshots -name app-secret       # in the snapshot, root-only
sudo cat /proc/<pid>/environ | tr '\0' '\n' | grep APP_PASSWORD           # root: yes; an ordinary user: permission denied
```

**Library cost:** a scratch module importing `filippo.io/age` (round trip with `armor`) reported 11 modules in `go list -m all`; one
importing `github.com/getsops/sops/v3/decrypt` reported 340, and built to 60 MB against 19 MB for tink. In this repo, `go get` added
two modules.

**Escape hatch:** `TestPlainAgeCLICanReadAValue` runs the real `age` binary over a stored value (skipped if `age` is not installed).

**End to end on a real host:** a stack with `environment.APP_PASSWORD: ${secret:app-password}` was planned and applied; the container's
environment matched the stored secret (compared by fingerprint, never printed); a second plan reported no changes; rotating the secret
printed `(value hidden) changed` and the not-restarted note, PID 1 kept the old value while the instance config held the new one; and
a scan of everything printed found no occurrence of either value. The four blocked cases (no store, unset, cannot decrypt, no identity)
each produced their own message.

**Immich `_FILE` variables:** the Immich environment-variables documentation lists `DB_PASSWORD_FILE` and the other database variables
as supporting files **[docs]**; Postgres's `POSTGRES_PASSWORD_FILE` is referenced there and documented by the official image **[docs]**.
