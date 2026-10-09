# Opinion as code: what tink is, and what becomes of `deploy`

**Status: design. Nothing here is built.** Written 2026-10-08, after the helper reached the end of its plan and `tink deploy` was looked at again for the first time since it was
ported. Claims are marked **[code]** (read in this repository), **[verified]** (tried live), or **[hypothesis]** (believed, not checked). Where this document says what the
author intends, it says so; it is the reasoning to be argued with, not a decision record.

## The question

Is tink a platform, or an opinion about a platform?

The answer proposed here: **an opinion about a platform, kept as code**. "Opinion-as-code" is worth defining, because it is easy to confuse with the thing next to it.

| | A platform | An opinion written down | Opinion-as-code (tink) |
|---|---|---|---|
| Where it lives | in a control plane that owns hosts | in a document or a runbook | in a tool you run against any Incus |
| What it asks of a host | adoption: the host is now its host | nothing, and so nothing is checked | nothing installed; an optional agent |
| What it does when you disagree | you leave | you ignore the document | it says what it prefers and why, and you opt out with a reason |
| Can it be checked? | by its own state | no | yes: `plan` compares what exists with what the opinion prefers |

The test of the third column is the second row: tink should work on an Incus install that has never heard of it, manage only what it is told to manage, and leave
the rest alone. It already behaves this way on the lab host (below).

## Where tink sits

Between two things people already use:

- **A `docker run` line pulled off a website.** One command, no concepts, readable, nothing to install on the server. This is where `tink run` starts, and what tink
  keeps: one binary, no daemon API of its own, state that lives on Incus objects. **[code]** (`tink run`; README: "One binary, not two")
- **Kubernetes.** Declare, diff, converge; a dependency graph; ownership; an agent next to the data. This is where `tink plan` and `plan apply` and the helper come
  from. What tink refuses is the **control plane**: Incus is the control plane. The helper is not one; it is an Incus instance running the same binary.

What it adds to both is the opinion: sensible defaults for the common homelab and VPS situation, chosen once, stated with their reasons, and checkable.

## The opinions

| Opinion | The default, and why | Where it is encoded today | How it is held to |
|---|---|---|---|
| **Storage** | ZFS preferred, btrfs a fine alternative. The principle is volume management and checksumming (and, as a consequence, cheap snapshots and copy-on-write clones). | Prose only. The "default, reasoning, named alternative, when to deviate" pattern is named in `resolver-architecture.md` with this very choice as its example; the backup code relies on copy-on-write clones. **[code]**: nothing reads a pool's driver. | Nothing. |
| **Backups** | 3-2-1: three copies, two failure domains, one off-site; and a backup is not one until a restore has been shown to work. | `backup:` on each volume; `kind: backup-target` with a declared `location`; the 3-2-1 check; the policy written on the volume; the scheduler and `verify`. **[code]** | `plan` warns on a volume with no `backup:` block, and on one that does not meet 3-2-1. The opt-out is explicit and carries a reason (`backup: none: regenerable -- ...`). |
| **Reverse proxy** | One shared Caddy `ingress` instance is the only public entry point. An app registers itself with three `user.ingress.*` keys; nothing edits the Caddyfile. | `internal/ingress`, `tink ingress reconcile`, the helper's ingress half, `configs/ingress`. **[code]** | By convention: an app that does not register is simply not exposed. |
| **Identity** | Authelia for single sign-on, in front of what you deploy. | `configs/authelia`, wired by `deploy` to Incus's OIDC login and to Incus UI. **[code]** Nothing in `internal/ingress` can ask for an app to be put behind it (no `forward_auth`, no auth key was found). | Not held to anything. The opinion covers the platform's own UI and not yet "the apps you deploy". |
| **App containers** | Bare OCI application containers. No Kubernetes, no Podman or Quadlet. | `kind: instance`, `tink run`. **[code]** | The tool only builds this kind. |
| **Secrets** | Encrypted at rest, never printed. | `tink secret`, `${secret:NAME}`. **[code]** | `plan` and `apply` redact; documented limits. |
| **Updates** | Image drift is reported, and what to do about it is declared per instance. | `on_image_change`. **[code]** | `plan` reports; `apply` converges only when told to. |

Two things stand out. Backups are an opinion **in the full sense**; the others are partly prose, partly conventions in code, and storage and identity are not yet held to anything.

## What an opinion looks like in tink

Backups, the one finished example, have four parts. Each opinion should have the same four, and the rest of this document uses them as the test.

1. **A default, stated once, with its reason.** 3-2-1, and why.
2. **A check that notices departure.** `plan` computes the copies and the failure domains from what the stack declares, and says what is missing.
3. **A reasoned opt-out.** Not a flag that silences it: a field that holds the reason, so the next reader of the YAML learns why this volume is different.
4. **Machinery that makes following it cheap.** The policy on the volume, the scheduler, the helper, `verify`. An opinion that is expensive to follow is a document.

And a rule about force: **opinions warn, they do not block.** The exception is where proceeding loses data (the build already blocks a policy the helper cannot read, because the
alternative is silently not copying). tink is borrowed opinion, not a gate.

The pattern from `resolver-architecture.md` adds a fifth, for choices with a real alternative: **a named alternative and when to deviate** (ZFS, or btrfs when ...).

## What `deploy` is, in this frame

`deploy` is the **reference realisation of two opinions, the edge and the identity, plus Incus UI**, as a one-shot command. It was ported faithfully from a bash script
(`incus-host/scripts/deploy.sh`) on the day the configs moved here, before the resolver existed. The opinions in it are worth keeping. Its form is the problem.

| What it does | How | Against the pattern |
|---|---|---|
| Registers image remotes | shells out to `incus remote add` | tink now resolves registries itself (built-in `docker-oci`, `ghcr`, `images`); probably unnecessary **[code]** |
| Storage volumes, profiles | Incus client, create or overwrite | the resolver already does this, with a diff |
| `incus-ui`, `authelia`, `ingress` | **stopped, deleted and recreated on every run** (documented in its own help text) | not convergent; `on_image_change` exists for the legitimate case |
| Config and secrets | `incus file push` of templates filled from `deploy.env`, plus loose secret files | `kind: file` (content pushed to an instance) and `tink secret` cover most of it; filling per-host values into a template has no stack equivalent that I found; the state is outside Incus |
| Server config | `incus config edit`: **replaces the whole config** | no plan, no diff, no partial ownership; takes the host over |
| Host-local only | shells out; needs a checkout of `configs/`; `refuseUnderRemote` | everything built since works with `--remote` |

Against the four parts: no check (a dry run lists commands, and compares nothing), no opt-out, and its machinery is "run it again". It also embeds a policy whose own header comment is
wrong: `authorization.star` returns True for every trusted TLS client, and its header says a `--restricted` certificate is still bounded by Incus's own restriction regardless.
That was **[verified]** false on a lab VPS (a restricted certificate listed another project's data) and is recorded in `helper-design.md`; the comment has not been corrected.

**Conclusion: right content, wrong form.** The Caddy edge and the Authelia login are the opinion, and they should be expressible as ordinary tink resources that `plan` can diff,
not as a command that owns the host.

## Proposal

### 1. Opinions ship as stacks, and as checks

An opinion is a stack fragment (the resources that realise it) and a `plan` check (the part that notices departure). They are versioned with the binary (embedded), applied with the
normal `tink plan apply`, and can be adopted piecemeal: the edge without the identity, the identity without Incus UI.

Whether there is also a convenience command (`tink platform plan|apply`) or only `tink plan apply -f` on an embedded file is **open**; the second needs no new surface.

### 2. What the resolver needs first

None of this is built, and I did not verify that each item is absent beyond reading the kinds in `internal/resolve/resource.go` **[code]**:

- **A server-config kind, with partial ownership.** It manages the keys it lists and leaves the others, and its `plan` shows the diff. Replacing the whole config, as `deploy` does, is
  what has to stop being the only way.
- **Per-host values for a stack** (the role `deploy.env` plays: domains, addresses, the registry). I found no variable mechanism in stacks; secrets have their own.
- **Ownership on every kind.** Only volumes carry `user.tink.stack` today. "Manage only what you declare" is clearer, and safer to delete against, if everything tink made says so.
- **Adoption.** `tink export` of live resources into stack YAML, so an existing install can be described and `plan` can read "no changes" before anything is applied.
- **Authelia's secrets.** `deploy` needs a one-time script that mints them with Authelia's own CLI; `tink secret --generate` and `kind: exec` may cover it **[hypothesis]**.
- **A way for an app to ask for the identity opinion** (a fourth `user.ingress.*` key, or similar), without which "single sign-on for what you deploy" is not yet an opinion tink holds anyone to.

### 3. The storage opinion becomes a check

The smallest of the missing parts and the clearest case of the pattern: `plan` already knows each volume's pool, and Incus reports the pool's driver. A volume on a pool whose driver does not
checksum or snapshot cheaply (`dir`, ...) would draw a warning that states the reason and the named alternative, with a field to opt out *with* a reason. Which drivers qualify
is a decision to make, not to guess: `zfs` and `btrfs` plainly; the `truenas` driver is ZFS underneath **[verified]** to be a real ZFS dataset on the NAS (`volume-backup-design.md`), but the
volume's filesystem on top of it is ext4 on a zvol, so "checksummed" needs a considered answer there.

### 4. How `deploy` gets from here to gone

Each stage is checkable on its own and none changes a host until the one before has been read.

1. **Adoption**: `tink export`, run against a hand-built host.
2. **The platform as a stack**: the edge, the identity and the UI written as resources, with the server config as a partial-ownership kind.
3. **Prove it read-only**: run `tink plan` of that stack against the reference VPS (`incus.xlii.co`) over `--remote`, and iterate until it reports **no changes**. This is the verification
   `deploy` never had, and it touches nothing.
4. **`deploy` becomes a thin alias** for applying that stack, with a notice; then it is removed. The criterion is stage 3 reaching "no changes", not a date.

## Tron

Tron is the working example of "tink on an Incus it did not build". Its server config is `core.https_address` and one image setting, with no OIDC. Alongside the stacks
tink applied there (Immich, mosquitto and the Matter server, from my notes) are instances tink did not make as far as I know (`caddy`, a Debian system container; `dns`; `haos`; and
others I have not traced). **[verified]** (read on 2026-10-08, and a whole-host `deploy --dry-run` changed nothing).

The telling finding: **no instance on Tron carries any tink marker**, including the ones tink made. Only volumes are stamped (`user.tink.stack`). So the line between "managed" and "not"
is not in Incus at all; it is in the YAML and in someone's memory. For an install that tink was added to later, that is the opposite of the property wanted, and it is the strongest argument for
ownership on every kind.

Converting it over means adopting those instances into stacks, one at a time, until `plan` reads "no changes", which is stage 1 above and why it comes first. Two leads, both
unchecked: `tink helper install --ingress --ingress-instance caddy` might let the helper keep the routes of Tron's existing Caddy if its layout matches `/etc/caddy/routes/generated/`
**[hypothesis]**; and a real `tink deploy` must **not** be run on Tron, because it would replace its server config and add the whole platform.

## The phase 5 branch

`retire-daemon-install` (not yet a pull request) does two things:

- **Removes `tink daemon install` and the unit generator.** Right under any direction here, and should land.
- **Makes `deploy` install the helper and retire the old unit.** This deepens a command this document proposes to replace. It is unit-tested and has not been run on a host. It should wait until
  the direction is decided; if `deploy` is going, the helper's place in the platform belongs in stage 2, as a resource, not as one more imperative step.

## Non-goals

- **A control plane.** No cluster state held by tink; Incus is the source of truth. The helper stays an Incus instance, not a service with its own API.
- **Forcing an opinion.** Everything warns, and everything has a reasoned opt-out. A tink that cannot be disagreed with has become a platform.
- **Being a backup engine, a Docker CLI clone, or a Kubernetes translator.** (Already non-goals in `volume-backup-design.md` and `internal/run/DESIGN.md`.)
- **Multi-host orchestration.** One Incus server at a time, as now.

## Open questions

- Is the Caddy and Authelia platform **the product**, or the **reference deployment** that happens to live in the repository? The answer decides whether stage 2's stacks are shipped
  inside the binary or sit in a repository of their own.
- Should tink ever manage server config, and if so only keys it declares? (This document assumes yes, partially.)
- Where does an opinion live when it spans pieces: is "identity" the Authelia stack, the ingress key that asks for it, the `plan` check, or all three as one unit?
- Which storage drivers count as meeting the storage opinion (above)?
- What does `tink export` do about secrets and about values that differ per host?
