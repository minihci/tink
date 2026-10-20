# Resolving a stack: translators and resolvers

**Status: the convergence half is built** (`internal/resolve`, `tink plan` and
`tink plan apply`). Translators other than `tink run`'s Docker-flag one are not.
This was written on 2026-09-18 as a named idea, before the resolver existed, and is
kept in the order it was reasoned: the sections before "Update, 2026-09-18" are the
idea as it stood, and the updates at the end say how it turned out (the diagram was
wrong; the YAML is the primary interface).

It is a synthesis of a real day's worth of experimentation, not a whiteboard
guess — every claim below was verified live against a real 5-instance stack, not
assumed. See `internal/run/DESIGN.md` for `tink run`'s own design, which this
document builds on directly.

## Where this comes from

Long before `tink run` existed, the original seed idea for this whole
project (a Logseq journal entry, 2026-02-09) was: take real Kubernetes
resource kinds — Deployment, Pod, Service, not a custom schema — convert
them into an intermediate representation, and *resolve* that IR onto
Incus primitives. `tink`'s own capability-zero command was renamed from
`apply` to `deploy` partly because of this idea (`apply`/`resolve` felt
like they should mean the same thing, and didn't want to collide). The
"convert to an IR" half was always the easy part to imagine. The
"resolve it" half was never designed, and the idea sat paused for lack
of a concrete answer.

This document is that answer, worked out empirically rather than
assumed — and a correction to the idea itself: "resolving" was never one
problem. It's two, bundled into one word.

## Two problems, not one

1. **Translation** — mapping a foreign vocabulary's fields (a
   Kubernetes Pod's `containers[].image`/`.env`/`.ports`/`.volumeMounts`,
   or a `docker run` flag) onto the equivalent Incus primitive (an image
   reference, `environment.KEY`, a `proxy` device, a `disk` device).
2. **Convergence** — given a full, multi-object description already
   expressed in Incus's own vocabulary (a project, several profiles, a
   volume, an instance, all referencing each other), safely computing
   what order to create things in and what to change vs. leave alone.

**(1) is already solved and working**: `tink run`'s own
`internal/run/flags.go` *is* a translator in exactly this shape, with
Docker CLI flags as its source vocabulary. A Kubernetes-Pod-spec
translator would be a sibling to that file, not a new kind of thing —
same job, richer source schema.

**(2) had never been solved on this platform** — every stack built so
far (Nightscout, Nextcloud, this platform's own capability-zero
instances) was stood up by a human running commands in the right order
from memory or a prose runbook. That's the gap this document is
actually about.

## Why a profile split alone doesn't solve (2)

The first attempt at "distilling" a validated `tink run`-built stack
into something reusable was a three-tier profile split, worked out and
proven live by rebuilding the same stack from it twice:

- **Platform tier** — one profile, true of every OCI application
  container this platform runs regardless of stack (`boot.autorestart`
  today). Not stack-specific; belongs in `tink` itself eventually.
- **Stack tier** — one profile per component role (`nextcloud-db`,
  `nextcloud-app`, ...), containing only facts durable across *any*
  deployment of that stack. In practice these always end up with zero
  devices: Incus devices merge as whole blocks by name, not
  field-by-field, so any device needing a deployment-specific value (a
  static IP, a named volume) has to be fully restated wherever it's
  overridden, which makes it instance-tier by construction, not a
  profile concern.
- **Instance tier** — genuinely deployment-specific facts: IPs, secrets,
  and non-secret-but-still-environment-specific values (a trusted
  domain, a peer's address).

This is a real improvement over how the actual `nextcloud-incus` profile
files work today (they bundle instance-tier facts into the stack-tier
file itself, punted via "expect another pass through these files at
cutover time"). But it doesn't resolve the underlying tension: the
instance tier is still an *ordered list of `incus` commands* — a
different vocabulary than a `tink run` command line, but the same shape
of problem. Nothing about the split makes "these five instances should
exist right now" less imperative.

## Terraform, tried for real

[`lxc/terraform-provider-incus`](https://registry.terraform.io/providers/lxc/incus/latest/docs)
was built and applied against a full recreation of the 5-instance
Nextcloud stack (redis, db, caddy, app, mcp) in a fresh Incus project.
Result: genuinely declarative, not just declarative-looking.

- One `terraform apply` computed a real dependency graph from resource
  references (`incus_storage_volume.foo.name` used inside an instance's
  device block) — profiles and volumes were created in parallel, actual
  parallel API calls, and instances only started once their own
  dependencies existed. Nobody wrote the order.
- The exact first-boot-config-ordering problem that forced `tink run`'s
  own `incus launch` → `incus init` rearchitecture (Postgres and
  Nextcloud need their config present before the image's one-shot
  install logic runs) needed **zero special handling** here — config is
  part of the same resource's creation payload, not a later PATCH, so
  the provider gets it right by construction.
- `terraform plan` genuinely diffs live state against declared state and
  reports "no changes" when nothing drifted — confirmed live, not
  assumed from documentation.

**One real gap, found and fixed rather than hidden**: the provider's
`file {}` block uploads a file but does not restart the process to
reload it. Caddy — deliberately running with `admin off`, so no
API-based reload — kept serving the image's own default page after the
Caddyfile was pushed. An in-container `exec` sending a kill signal
didn't work either, confirmed live it doesn't even reach the target
process (a real limitation of `incus exec`'s process view for this OCI
container, not a Caddy signal-handling issue). The actual fix: one
`terraform_data` resource with a `local-exec` provisioner, triggered
only by the Caddyfile's own content hash. This is the one place the
config still shells out directly — a single, named, automatically
re-triggered exception inside an otherwise-declarative graph, not the
whole artifact the way a hand-written command doc was.

**Terraform vs. OpenTofu**: HashiCorp moved Terraform's future releases
from MPL 2.0 (real open source) to BUSL 1.1 (source-available, not OSI
open source) in August 2023. The Linux Foundation-governed **OpenTofu**
fork (accepted September 2023) stayed MPL 2.0, is CLI-compatible, and
works with the same `lxc/incus` provider unchanged. Given this
platform's existing self-hosted, no-vendor-lock-in leanings, OpenTofu's
`tofu` binary is the preferred choice over HashiCorp's own — same
capability, zero switching cost, better license.

## `incus-apply`, tried as the lighter-weight alternative

[`abiosoft/incus-apply`](https://github.com/abiosoft/incus-apply) is
purpose-built for Incus rather than a generic multi-cloud translation
layer, and it shows: its YAML is nearly identical to `incus profile
show`/`config show` output, it's a single small binary that shells out
to the `incus` CLI internally (confirmed by reading its source), secrets
load from a `.env` file with no separate state artifact to leak them
into (a real advantage over Terraform's `terraform.tfstate`, which holds
every secret in plaintext), and `apply.after` gives explicit
instance-to-instance ordering — Docker-Compose-`depends_on`-shaped, not
a computed graph (confirmed by reading its source: there is no
cross-resource reference mechanism, and `--project` is a CLI flag, not a
per-resource YAML field).

**Not safe to adopt as of the released `v0.1.1`**, for a concrete,
disqualifying reason: `--project <name>` is honored when checking
whether a resource already exists, but silently dropped on the actual
`create`/`launch` call. Confirmed via `--verbose` output. Every resource
in a 14-resource apply landed in the `default` project instead of the
target one — 5 stopped instances, 6 profiles, and 2 volumes, sitting
right alongside this host's real, running `authelia`/`incus-ui`/
`ingress`. No collision happened this time; nothing prevented one. This
is exactly the kind of bug that matters most here specifically, since
project isolation is this platform's real tenant-separation boundary
(`nextcloud`/`nightscout` projects on `incus.example.org`) — not something
to route around quietly.

Filed upstream as
[abiosoft/incus-apply#68](https://github.com/abiosoft/incus-apply/issues/68),
traced to a likely regression from that project's own PRs #45/#47
("simplify"/"remove redundant" project scoping) — then root-caused and
fixed the same day rather than left as a report: `cloneResource`
(`internal/incus/managed.go`) round-trips a resource through YAML to
deep-copy it, then manually restores the fields tagged `yaml:"-"` that
don't survive that round-trip; `Project` is one such field but was
missing from that restoration list, and `Create()` builds its command
from the clone, not the original resource. Fix, test, and live
re-verification (both a profile and an instance, before/after, on a
real host) submitted as
[abiosoft/incus-apply#69](https://github.com/abiosoft/incus-apply/pull/69).
Not merged as of this writing — check its actual state before assuming
either the bug or the fix's status.

## The proposed architecture

```
   Docker-flag input ──┐
                        ├──> translator ──> resolver-agnostic IR ──> lowering step ──> resolver ──> Incus
 K8s-Pod-spec input ────┘        (per source vocabulary)      (one, shared)    (swappable)
```

- **Translators, plural** — one per source vocabulary. `tink run`'s
  `internal/run/flags.go` already is one, for Docker flags. A
  Kubernetes-Pod-spec translator is the only genuinely new component
  this whole idea still needs.
- **A shared, resolver-agnostic IR** — in the spirit of `tink run`'s own
  internal `Spec` struct, not tied to any one resolver's file format.
  Every translator emits this, regardless of source vocabulary.
- **One lowering step** — turns the IR into whatever the chosen resolver
  actually wants (OpenTofu's `.tf.json`, or `incus-apply`'s YAML). This
  is what keeps the resolver choice swappable rather than baked into
  every translator.
- **The resolver** — does the actual converging. **OpenTofu adopted as
  the primary, opinionated default** (the one actually proven live
  against a real stack); **`incus-apply` named as the documented
  alternative**, worth reconsidering once #69 above is merged and a
  release cut with it — the same "default + reasoning + named
  alternative + when to deviate" pattern already used elsewhere on this
  platform (ZFS vs. btrfs for storage).

## Explicit scope boundary

This only addresses the **host-level** half of the original idea — which
instances, profiles, and devices should exist. It says nothing about
`tink-init` (a real, unfinished PID-1 supervisor for running multiple
processes inside one Incus *system* container, Pod-shaped) — the
**container-level** half of the same original IR idea.

Worth stating plainly, since it's easy to get backwards: every instance
built on this platform so far has been single-process, no init. That
consistency is at least partly a symptom of `tink-init` never having
been finished or deployed — there was no multi-process option to choose
even if wanted — not proof that single-process-no-init is a settled,
permanent preference. Whether `tink-init`'s Pod-shaped pattern is ever
actually wanted remains a genuinely open question, independent of what
necessity has produced so far.

## Status when this was written (the updates below say what was built)

Not built. Named, and now grounded in real, verified evidence rather
than a hypothesis — but still gated on the same standing principle as
everything else on this platform: don't build ahead of a real, live
need. The concrete trigger to actually build the Kubernetes-Pod-spec
translator and the lowering step: a real tenant stack that would
benefit from being *described* once and resolved more than once
(rehearsal + production), the same way the nextcloud-tink-test exercise
that produced this document was itself motivated by a real, repeated
manual-translation cost — not a hypothetical one.

## Update, 2026-09-18: the resolver got built, and the diagram was wrong

The convergence half got built after all, as `internal/resolve` (first
merged as PR #2, from a branch since deleted) — not OpenTofu, not `incus-apply`, a small
tink-native package: a dependency graph inferred by name-matching
(project/profiles/device sources, plus an explicit `depends_on` for
edges structural inference can't reach), stateless (every check queries
Incus directly, no state file), applying level by level with everything
in one level running concurrently. Exposed as `tink plan` /
`tink plan apply`, two verbs rather than one command with a `--dry-run`
flag — `plan` only ever reads, so there's nothing for a flag to opt out
of.

Live-validated against two structurally different real stacks, not one:
the same 5-instance Nextcloud stack this document was originally
written against, and then a Nightscout reconstruction (mongo, app,
caddy) on a separate test host. The second stack is the more
interesting proof: its app tier has no `depends_on` on its database at
all, deliberately, because `cgm-remote-monitor`'s own Mongo client
retries forever and never needs it up first — unlike Nextcloud's app
tier, which genuinely does. `resolve` put Nextcloud's db-dependent app
in its own later level and Nightscout's independent one alongside its
database in the same parallel level, correctly, with no code change
between the two — real evidence the dependency inference generalizes
rather than having been shaped to fit the one stack it was built
against.

This supersedes the "proposed architecture" section's resolver choice
for this platform's own scope: `internal/resolve` is lighter than
OpenTofu (no separate binary, no HCL/`.tf.json` lowering step, in-repo)
and doesn't carry `incus-apply`'s outstanding project-scoping bug —
exactly the "lighter-weight tool" this document went looking for before
building either alternative. OpenTofu and `incus-apply` remain named
alternatives, not the primary path, worth reconsidering only if a real
need outgrows `resolve`'s deliberately narrow scope (no in-place
instance reconfiguration, no computed cross-resource values, no state).

**The diagram above was wrong about one thing, though, not just the
resolver box.** It drew the IR as an internal, invisible hop between
translators and a resolver — implying the normal way to define a stack
would always be through a translator. In practice, both real stacks
above were hand-authored directly as `resolve`'s own YAML, with zero
translation step involved, ever. That YAML isn't a serialization format
for an otherwise-hidden IR; it *is* the IR, made directly authorable,
and it has already proven itself sufficient as a primary interface
across two stacks with genuinely different shapes.

That reframes what a Kubernetes-Pod-spec translator (or `tink run`'s
existing Docker-flag one) actually is: not a competing "real" interface
to `resolve`'s own YAML, and not superior to just writing the YAML —
an onboarding/compatibility layer that produces entries in this same
native format for someone arriving with that vocabulary already in
hand. There's a structural reason it could only ever have been that: a
Pod spec, like a `docker run` command, describes one container-shaped
thing, not a stack's whole graph — it has no native way to say "these
belong to one project" or "this profile is shared across three of
these," the same ceiling `internal/run/DESIGN.md`'s own non-goals
already document for Docker flags. Once a description has to span a
whole graph, not one instance, it has already outgrown what either
per-container vocabulary can express on its own.

Revised diagram:

```
   Docker-flag input ──> translator ──┐
                                       │
 K8s-Pod-spec input ──> translator ──┼──> resolve's own YAML/IR ──> Incus
                                       │    (the primary interface —
   hand-authored YAML ────────────────┘     directly authorable, not hidden)
```

Concretely, this also means a Kubernetes-Pod-spec translator's value
is no longer justified by having been the original seed idea — it's
gated on the same evidence `tink run`'s own Docker-flag translator had
before it was worth building: real Pod-spec-shaped things out there
worth importing (a Helm chart, someone's existing manifests), not a
hypothetical one. Still not built, for exactly that reason.

A primary interface earns a default filename the way a compatibility
layer never would: `tink plan`/`tink plan apply` given no `FILE` now
read `tink.yaml` from the current directory (`internal/resolve.
DefaultFile`), the same role `docker-compose.yml`/`kustomization.yaml`
play for their own tools. Not a thing a translator's output would ever
need — nobody runs `tink run` or a future Pod-spec import with no
arguments and expects it to find something on disk by convention.


## Update, 2026-10-04: an instance's image is no longer create-only

`resolve` treated an instance's `image:` as create-only: bump a tag and
`plan` reported "no changes", `apply` exited 0, and if the same edit also
changed `config:` it was written onto the old image. That is now closed;
[`docs/image-updates.md`](image-updates.md) is the user-facing description.
`plan` reports image drift, a per-instance `on_image_change` policy says
whether `apply` blocks on it (`report`, the default), tolerates it
(`ignore`), or converges it by rebuilding the root filesystem (`rebuild`,
OCI app containers only, digest-pinned images only), and `plan apply` ends
with a summary line and a non-zero exit status whenever it leaves anything
blocked.

Building it corrected two assumptions worth keeping. First, an instance's
`image.id` is not an identity: it is a property of whichever cached image
record first held that content, as typed at that pull, so three instances
made from the same bytes by a tag, a digest and both together all report the
same `image.id`, and an obvious "compare the reference strings" check
reported drift between two names for identical content. What identifies an
OCI image is its fingerprint, which Incus derives from the layer digests and
records as `volatile.base_image`; comparing it needs a registry lookup, so
`plan` now makes one (`--offline` opts out). Second, `incus rebuild` is not
the same as creating the instance again: it is not atomic (it deletes the
root volume and then creates the new one), it downloads the image only
after the instance is stopped, and it leaves the `oci.*` and `environment.*`
keys copied from the *old* image untouched. The runtime-config check and the
pre-pull exist because of those three facts, each of which was confirmed
against the Incus source and a live daemon.

This also makes the scope boundary above stale in one respect: "no in-place
instance reconfiguration" has been untrue since existing instances started
converging in place, and image changes are now handled as described, though
only through an explicit opt-in.

## Update, 2026-10-09: a volume's own settings

A `kind: storage-volume` can carry `config:`, the Incus volume config it is created with (`initial.uid`, `initial.gid`, `initial.mode`, `size`, `block.filesystem`...). It was refused before
("does not use field Config"), which left no way for a stack to say who owns a volume, and `tink run --user` made that matter: it creates volumes owned by the user, and a stack rebuilt from
`tink export` came up with root-owned volumes that the app could not write to (verified live with Navidrome).

- It is created with those keys, and `plan`/`apply` converge the others like an instance's config: only declared keys are compared, nothing else is touched.
- **`initial.*` is creation-only.** Incus reads it when it makes the volume and does nothing afterwards, so on a volume that already exists a difference is a **warning** ("only applies when a volume is
  created: change what it is by hand"), never a change `apply` would claim to make. Setting the key on an existing volume would only record a value that has no effect.
- Keys tink writes itself are refused so that two sources cannot fight over one key: `user.tink.backup.*` and `user.tink.stack` (the copy policy, its stamps, the owning stack), `volatile.*` (Incus's), and
  `snapshots.schedule`/`snapshots.expiry` when the `backup:` block already has a `snapshots` policy.

An instance can also say `ephemeral: true` (deleted by Incus when it stops, whatever the reason; `tink run --rm`). Like every other declared field it is only ever turned on: not declaring it changes nothing, and `plan`
reports `ephemeral: false -> true` for a live instance that should be.


## Update, 2026-10-12: a stack can name its project once

Every resource used to carry its own `project:`, and one that forgot it was created in the server's default project without a word.
On a host where that project holds production, that is the mistake worth making impossible. The stack's own `kind: stack` document
now takes an optional `project:`, which every resource that lives in a project and names none receives:

```yaml
kind: stack
name: smarthome
project: smarthome
---
kind: project
name: smarthome
---
kind: instance            # no project: needed; it is in smarthome
name: mosquitto
```

- **What receives it:** profiles, storage volumes, instances and images. Not a `kind: project` (a project is not inside another), the
  stack itself, a `kind: backup-target` (it describes where copies go) or `kind: incus` (argv for the local CLI).
- **A resource's own `project:` always wins.** A `file` or `exec` resource follows the instance it acts on (that instance's own project,
  or the default it just received); only one whose instance is not in the stack takes the default.
- **It is applied when the stack is loaded**, so every command that reads one sees the final project (plan, apply, backup restore and
  verify), and again in `Levels` for callers that build resources by hand. Running it twice changes nothing.
- **Because things now name the project, it is ordered first**, if the stack declares it, exactly as when each resource said so itself.
- **A stack that does not use it is checked for the mistake it prevents.** If some resources that live in a project name one and others
  do not, `plan` and `plan apply` warn on each that does not: "names no project, so it is created in the server's default project,
  while ... are in project ...". A warning, not an error: putting some resources in the default project on purpose is legitimate, it just
  should not happen by omission. A stack where nothing names a project, or everything does, is not warned about.

Two stacks (two `kind: stack` documents) have no single default; `Levels` already refuses that. See `internal/resolve/stackproject.go`.

## Update, 2026-10-12: `depends_on` must name a resource in the stack

A `depends_on` entry that names nothing declared in the stack (a typo, or a resource since renamed) used to be dropped without a word,
which silently changed the order the author asked for. It is now a load error that names the resource and the entry, and suggests
the nearest resource name when one is within a couple of edits. An entry that names the resource itself is an error too.

Structural references stay lenient on purpose: an instance's profile, a device's volume source, or its project may already exist on
the server without being declared here, and then there is nothing to order. `depends_on` exists only to order the resources of one
apply, so a name that is not in the stack can only be a mistake. The stack's own name (`kind: stack`) is metadata, not a resource, and
cannot be waited for. See `internal/resolve/dependson.go`.


## Update, 2026-10-13: files are placed before an instance's first start

Creating an instance used to be: create it, apply its config, **start it**; and only then the `kind: file` resources that target it pushed their files. A service that needs one of those files at start (Headscale needs `/etc/headscale.yaml`) had a window in which the file was not there: the app exited at once, Incus restarted it in a loop, and tink's own calls that fell in the gaps failed (`reading current content … bad file descriptor`, `Failed to retrieve PID of executing child process`). A second apply converged; a first install should not need one. The design, with the lab evidence, is in [first-start-files-design.md](first-start-files-design.md).

An instance that has been created and never started takes a file push, so **creating an instance now places its eligible files after configuring it and before starting it**:

- **Which files:** those that target a container instance this stack declares, whose `depends_on` names only that instance or other such files (an exec, or anything else, makes a file wait for its own turn, and so do the files that wait for it). A virtual machine keeps the old behaviour: it needs its agent running to take a file. They are placed in declaration order.
- **Nothing else changes.** The file resources still run in their own level. Each is planned immediately before it is applied, finds its content already there, and says `no changes`: no second push and, for `restart: true`, no restart of the instance that has just started with the file. `plan` still says `would create`, plus a note for a file a mount keeps from the first start (next bullet).
- **A mount hides the rootfs.** A file at or below the mount path of a disk device (a volume at `/config`, a host directory) would land underneath the mount and be hidden from the first start. One under a custom volume's mount is written into the volume instead; one under a host directory, or at the mount path itself, is left for after the start, and the apply and the plan say so: `file/ha-yaml: not placed before the first start, because /media is a mounted host directory (device media); it is written right after the start, so the app may read its config first`. See "Files under a mount" in [first-start-files-design.md](first-start-files-design.md); the volume write was run live on 2026-10-13 (a failing write, and a volume attached before, are still only covered by fakes).
- **It never adds a way to fail.** A push that fails (a directory that is not there) is skipped and the instance is started anyway; the file resource then fails in its own turn with the message it has always had.
- **It is said:** `instance/headscale: created`, then `instance/headscale: placed 2 file(s) before its first start: file/headscale-policy, file/headscale-config`, naming only the files that were placed.
- Placing before the start is only for an instance **created** by the apply. An existing instance's files are pushed in their own turn as before, with one addition: the same hazard (a push beneath a mount) exists whenever the instance is **not running**, not only before its first start. A file under a custom volume's mount of an instance with no usable init (any status but `Running`, or `Running` with no init, which is what a crash-looping container looks like) is read from and written into the volume, and one under a host directory is deferred with a warning (`DEFERRED` in a plan, no failure in an apply). See "Later applies: the instance is not running" in [first-start-files-design.md](first-start-files-design.md); that part has only been run against fakes.

This is also the mechanism the secrets design asks for to deliver a secret as a file "after create and before first start" ([secrets-design.md](secrets-design.md)); that feature is not built.


## Update, 2026-10-13: an exec can compare live output with a declared file (`check_output`)

A `kind: exec` declares exactly one way to tell "already done" from "needs to run": `check`, `triggers`, and now `check_output`. It is for an engine with a get/set pair, where the live state can be read back and compared with what the stack declares. The design, and the lab finding behind it (Headscale's policy), is in [exec-output-check-design.md](exec-output-check-design.md).

```yaml
kind: file
name: headscale-policy
instance: headscale
path: /etc/headscale-policy.hujson
source_path: policy.hujson
---
kind: exec
name: headscale-policy-set
instance: headscale
command: [/ko-app/headscale, -c, /etc/headscale.yaml, policy, set, -f, /etc/headscale-policy.hujson]
check_output:
  command: [/ko-app/headscale, -c, /etc/headscale.yaml, policy, get]
  equals: headscale-policy
```

- **Converged** when the check command exits 0 and its output equals the declared content of the file resource `equals` names, ignoring trailing whitespace on both (`policy get` prints one more newline than the file has). Anything else, including a non-zero exit, is "not done": `policy get` before any policy is set exits non-zero, and that is the first set. The output is the command's standard output and error together, as for every exec, so the command should print nothing else on success. Output over 1 MiB is an error.
- **`equals` must name a `kind: file` resource** in the stack, on the same instance, with a declared `content` or `source_path`; anything else is a load error (with a "did you mean" for a near miss). It also **implies the ordering edge**: the exec runs after the file is placed.
- **After the command runs, the check runs again.** If the output still differs, the apply fails and says so, instead of reporting a change that did not take effect. A command that fails (Headscale rejecting a bad policy: exit 1, and it keeps the previous policy) fails the apply with its own message.
- **`plan` says why, never what:** `would run: [...] (the output of [...] differs from file/headscale-policy)` or `(the check [...] exited 1)`. It does not print the content.
- **It heals from drift.** A policy set by hand is noticed by the next `plan` and put back by the next `apply`, which a remembered trigger hash cannot do. That is why a live check stays the preferred guard.
- `check`, `check_output` and `triggers` are mutually exclusive, and `check_output` is exec-only.


## Update, 2026-10-13: `plan` warns when an instance's profiles give it no root disk

Creating an instance sends Incus only its profiles and the ephemeral flag; its own config and devices are applied after it exists (`run.Create`, then `createInstance`). Incus needs a root disk to write the instance record and can take one only from the profiles, so an instance whose profiles carry no `disk` device at `path: /` cannot be created, and a root disk declared on the instance itself comes too late to help. A project made with `features.profiles=true` has an **empty** `default` profile, so an instance that lists no `profiles:` fails the same way. The error names none of this (`Failed getting root disk: No root device could be found`), and the DNS lab met it on 2026-10-13, where the first two applies of a one-container stack failed until a `kind: profile` with a root disk was declared and listed under `profiles:`.

`plan` and `plan apply` now warn on the instance, saying what is missing, why declaring it on the instance does not work, and the fix:

```yaml
kind: profile
name: base
devices:
  root:
    type: disk
    path: /
    pool: default
---
kind: instance
name: dns
profiles: [base]       # without a profile like this, apply fails with "No root device could be found"
```

- **Which profiles:** the instance's `profiles:` list, or the project's `default` when it lists none (Incus's own rule). Each is read from the stack's declaration and from the server, since applying a declared profile only adds the devices it names. One profile with a root disk, under any device name, is enough.
- **Conservative:** a profile that cannot be read, or that exists neither in the stack nor on the server (Incus reports that on its own), means no warning. A later profile that overrides an earlier root device is not modelled, so that case is not warned about either.
- **A warning, not an error**, for containers and virtual machines alike, and, like the [OCI NIC warning](oci-network.md), about the stack: it appears whether or not the instance exists yet.
- **Not covered:** `tink run` creates an instance the same way and has no such check. The instances tink makes for itself do not depend on the profile: `backup verify`'s check instance and the helper set `Spec.RootDiskAtCreate`, which puts their root disk in the creation request (see [volume-backup.md](volume-backup.md)). A stack's instances do not, so the warning stands.

See `internal/resolve/rootdisk.go`.

## Update, 2026-10-17: `plan` warns about two proxy-device mistakes Incus does not report

A `proxy` device with `nat: "true"` is how a host with one public address carries a port to an instance, keeping the client's real address (measured on Incus 7.5.1; the design and the measurements are in [public-exposure.md](public-exposure.md)). A stack expresses it as any other device, and it planned, applied and re-planned correctly. Two mistakes in it were not reported:

- **A NAT proxy that connects to an address the instance does not have as a static address** planned as `would create`; the apply created the instance, retried the start five times and left it stopped, with Incus's own message. A connect address that is not an IP address is refused by Incus when the device is added. `plan` now says so beforehand: for each host-side proxy device with `nat` on, the `connect` host must be an IP address and must be one of the `ipv4.address` / `ipv6.address` values (a CIDR counts) of the NIC devices the instance declares, or gets from a profile the stack declares. A profile the stack does not declare cannot be seen; the warning names it.
- **Two host-side listeners that overlap** (the same protocol, equal addresses or a wildcard of the same family, and intersecting ports, ranges and comma lists included) are accepted by Incus without comment: both instances run and the rule written first answers. `plan` now tells each instance involved which other instance and device it overlaps. The comparison is across the whole stack and ignores projects, because a listener is global to the host; it cannot see other stacks. A proxy bound inside the instance (`bind: container`) is not the host's and is ignored.

Both are **warnings, not errors**, like the [OCI NIC warning](oci-network.md), and are about the stack: they appear whether or not the instance exists yet. A wildcard NAT listener is deliberately not warned about: Incus writes `fib daddr type local` for it, so it takes only traffic addressed to the host's own addresses. See `internal/resolve/proxydev.go`.

## Update, 2026-10-20: `plan` checks that an instance is running, and that it has the profiles the stack names

`planInstance` compared config, devices and the ephemeral flag, and nothing about whether the instance was running or which profiles it had. A create that fails after the instance and its config exist but before a successful start leaves a stopped instance, and the next `plan` said `no changes`. That was read from the code and has not been reproduced against a live server; `plan_runstate_test.go` reproduces it against a fake.

- **A stack declares its instances running.** There is no field to declare one stopped, and `createInstance` always starts what it creates. An instance that exists with status `Stopped` is now planned as an update with the change `state: Stopped -> Running (start)`, and `plan apply` starts it (`updateInstance`, through `run.EnsureRunning`, after the config is applied). Only `Stopped` counts: another status is not something a start fixes (a container whose init has died is listed `Running`). It also means an instance stopped on purpose is started by the next apply; to keep one stopped, take it out of the stack. Like any other change, the start is withheld when the instance is blocked (image drift under `on_image_change: report`).
- **A missing profile blocks.** When the instance's `profiles:` list names a profile the live instance does not have, `plan` blocks it and `plan apply` changes nothing on it, because apply writes config and devices and never an instance's profile list; saying `update` would promise a change that does not happen. The reason names the profile and the fix (`incus profile add`, or recreate the instance). A profile the instance has beyond the stack's is not drift (it stays in `plan --extras`), the order is not compared, and a stack that lists no `profiles:` says nothing about them.

## Update, 2026-10-20: a guest exec has a deadline and a capped output, and `plan apply` stops cleanly on Ctrl-C

Only the registry code took a `context.Context`; the resolver and the guest exec did not. Two consequences, both read from the code and checked against a fake that never answers (`internal/incusapi/exec_test.go`, `internal/resolve/checkoutput_test.go`); neither has been run against a live server.

- **Every guest exec has a deadline.** `incusapi.ExecInGuestContext` gives each attempt `DefaultExecTimeout` (5 minutes) unless the caller says otherwise, abandons the wait when it passes, cancels the Incus operation (which asks the daemon to stop the command) and returns an error wrapping `incusapi.ErrExecTimeout` with the output so far. A `kind: exec` resource sets its own with `timeout: 90s` (exec-only, a non-negative Go duration, covering `check`, `check_output`'s command and `command`); it is separate from `agent_timeout`, which is how long to wait for a booting VM's agent, and the deadline is per attempt. Every other caller of `ExecInGuest`/`ExecWithStdin` (the volume-backup check, identity, the helper) gets the default.
- **A guest command's output is capped** at 1 MiB (`incusapi.MaxExecOutput`). The rest is read and dropped, and the output ends with `[output truncated at N bytes]`, so a command that prints without end costs time, bounded by the deadline, and not memory.
- **Ctrl-C.** `tink plan` and `tink plan apply` run under a context that ends on the first SIGINT/SIGTERM (`PlanOptions.WithContext`). A guest exec in flight is abandoned at once. `plan apply` lets the level in flight finish and then starts no further one, prints the usual summary with `interrupted: the levels after this point were not applied`, and exits non-zero; `plan` stops before the next level. A second Ctrl-C gets the default action and kills the process. A resource whose apply is a multi-step sequence inside one level is not interrupted between its steps by tink's choice, but a kill of the process (second Ctrl-C) still can.
- **Not covered yet:** the context reaches the guest exec, the level loop, planning (each resource checks it before it reads, see the 2026-10-20 update below) and a rebuild's pull; the other Incus calls (a create, a volume copy, a rebuild's stop and rebuild) still run to completion once started; and `tink run`, `deploy`, `backup` and `export` take no context into the resolver.

**Re-check at each Incus upgrade.** A VM whose agent is not running is recognised by the text of an Incus internal error (`agentOfflineMessage` in `internal/incusapi/exec.go`); Incus gives it no typed error, so no test here can notice the wording change. After upgrading Incus, run an `exec` resource against a VM whose agent is down and check that it reports "VM agent did not come up" rather than the raw Incus message.

## Update, 2026-10-20: what the context reaches in the resolver, and what it deliberately does not

Checked against fakes (`internal/resolve/rebuild_test.go`, `rebuild_pull_test.go`, `interrupt_test.go`); not run against a live server.

- **Planning stops at once.** A plan is pure reads, so `PlanWithOptions` checks the context before each resource reads anything and returns `planning interrupted`; there is nothing to leave half-done.
- **A rebuild is interrupted only while nothing has changed.** `runRebuild` takes the context and checks it before the snapshot listing, before the pull, and after the pull, all before the stop. The pull waits with `WaitContext`, so Ctrl-C abandons it and the client cancels the server's download, which touches no instance. A rebuild waiting for its turn behind another (`rebuildSlot`, which replaced a mutex so the wait can end) gives up the same way. The error says `nothing was changed` and wraps `context.Canceled`. From the stop on the context is not consulted: the stop, volume snapshots, rebuild, config and restart run to their end, because stopping between the stop and the restart leaves the instance down on a root filesystem that is deleted and not yet recreated. See [`image-updates.md`](image-updates.md).
- **Left to finish on purpose:** instance create (create, config, file placement and first start are one unit: an instance cut off after its create would be started later by the next apply without the files placed at first start), storage-volume create and the config update. A half-finished create cannot be told from an operation that is merely slow, and cancelling an Incus operation is not the same as undoing it, so these keep the "the level in flight finishes" rule from above. Unlike an exec they have no deadline either: a daemon that accepts a create and never answers is ended by a second Ctrl-C.
- **Still without a context:** `tink run`, `deploy`, `backup` and `export`, and the Incus calls in `internal/run`, `internal/volbackup`, `internal/bootstrap` and `internal/helper`.
