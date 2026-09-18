# Resolving a stack: translators and resolvers

Named idea, not built. This is a synthesis of a real day's worth of
experimentation (2026-09-18), not a whiteboard guess — every claim below
was verified live against a real 5-instance stack, not assumed. See
`internal/run/DESIGN.md` for `tink run`'s own design, which this
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
(`nextcloud`/`nightscout` projects on `incus.xlii.co`) — not something
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

## Status

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

The convergence half got built after all, as `internal/resolve` on the
`resolve-spike` branch — not OpenTofu, not `incus-apply`, a small
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
