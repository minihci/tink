# Apply that knows what it owns: removals, ownership and project drift

**Status: design. Nothing here is built.** Written 2026-10-12, from using `tink plan` as the phase gate of a Home Assistant migration on a
single-host Incus server. It is the reasoning to be argued with, not a decision record.

Claims are marked **[verified]** (tried live; the commands are in the [appendix](#appendix-how-the-claims-were-verified)), **[code]** (read in
this repo), **[docs]** (read in upstream documentation) or **[hypothesis]** (believed, to be checked).

## The problem

`plan` ending in "no changes" is used as the proof that a stack is converged. It is weaker than it reads, for three reasons that share one cause:
tink compares what the YAML **declares**, and nothing else, and it keeps no record of what it applied.

| # | Symptom | What happens today |
|---|---|---|
| S1 | A device or config key is **removed from the YAML**, or one is **added by hand** to the live instance | Neither is ever mentioned, and a removed one is never removed. **[verified]** An instance with a `config` disk device and `limits.cpu: 2` was planned against a stack that omitted both: the only line printed was a difference on a device the stack *did* declare |
| S2 | A **project's** declared `config` differs from the live project | `plan` says "no changes". **[verified]** The stack declared `limits.containers: "5"` for a project that has no such setting |
| S3 | Whether tink manages an instance at all | Nothing on the instance says so. Only storage volumes carry `user.tink.stack`. **[docs]** `opinion-as-code.md` (on the `platform-design` branch) records the same finding for a whole host |

Concrete costs seen:

- A planned step "the instance has a second NIC for LAN discovery now; drop it after the router change" **cannot be expressed**: deleting the device
  from the YAML leaves it on the instance and `plan` stays silent.
- The migration's gate "every phase ends with `plan` reading no changes" cannot see a hand-added device, a hand-set limit, or a project setting that drifted.
- A hand-made change and a stack's declaration cannot be told apart, so "the stack is the source of truth" is a claim and not a check.

## What is true today

| Fact | Where |
|---|---|
| `diffConfig` is **one-directional on purpose**: keys the stack does not declare are never reported ("a live object carries plenty of keys we don't own") | **[code]** `internal/resolve/plan.go` |
| `diffDevices` walks the **declared** devices only | **[code]** `plan.go` |
| Applying an existing instance copies the declared config keys and devices over the live ones and sends the result; it never deletes. Devices are replaced as whole blocks by name | **[code]** `updateInstance` -> `run.ApplyConfig` |
| A project is created if absent and otherwise reported "no changes", whatever its declared `config` says | **[code]** `planProject` |
| `planInstance` does not compare the declared `profiles` list with the live one | **[code]** `planInstance` has no profile diff |
| tink holds no state of its own: "Incus owns everything, tink diffs live Incus state directly. Don't add a state file" | **[code]** `CLAUDE.md` |
| `tink export` already tells what the user set from what Incus and the image wrote: it drops `volatile.*` and `image.*`, and `oci.*`/`environment.*` that equal the image's | **[code]** `internal/resolve/export.go` |
| The Incus client can set instance config and devices atomically in one update | **[code]** `UpdateInstance` |

## Principles to keep

From the project's own text: no state file of tink's own; manage only what you are told to manage and leave the rest (`opinion-as-code.md`); opinions
warn and do not block, except where proceeding loses data; and tink "never removes what it is no longer told about" (`volume-backup.md`), which is the
sentence this note asks to refine, because for *devices and keys that tink itself applied* it is the wrong default. Removal is destructive, so it is
shown before it is done.

## Proposal: three layers, each useful alone

### Layer 1: report what is not declared (read-only)

`plan` lists, under each instance, what is on it and not in the stack:

```
instance/ha-restore-b: no changes
    not in the stack (not managed here): device.config (disk), config.limits.cpu, config.limits.memory
```

Classified with the same rules `export` uses, so `volatile.*`, `image.*` and image-equal `oci.*`/`environment.*` are never listed. Nothing is removed and
the exit status does not change; the summary line gains a count, so "converged" and "converged, with undeclared extras" read differently. Cost: small.
It needs no marker, so it works on any instance, including ones tink did not create.

### Layer 2: remember what was applied, so removal is safe

On every create and update, tink writes onto the instance, **in the same update call**, `user.tink.applied`: the sorted *names* of the config keys and
devices it manages (never values, so a secret cannot end up in it), plus `user.tink.stack` when the stack is named. This is the idea behind
`kubectl apply`'s last-applied record, kept on the Incus object, not in a tink file, so it respects "Incus owns everything".

On the next apply, anything in `applied` that the YAML no longer declares is **removed**, and `plan` shows it first:

```
instance/ha: would update
    device.lan: removed (applied by this stack before, no longer declared)
```

- **Adoption is safe by construction.** The first apply on an existing instance writes the marker from what is declared and removes nothing. Whatever
  predates tink is only ever reported (Layer 1), never removed.
- **Hand-made additions are left alone** and reported, because they were never in `applied`.
- Because it is one update, "remove a device and add another with the same name" cannot half-happen.
- `plan` can finally say whose an instance is: "managed by stack `smarthome`", and warn on a takeover by another stack exactly as it does for volumes.

### Layer 3: exclusive, by choice

`exclusive: true` on a resource removes **everything** undeclared (except what Incus and the image write). For an instance tink fully owns, such as a
generated one. Not needed first; it is cheap once Layer 1's classifier exists, and it is the named alternative for people who want Terraform's stance.

### Projects (S2), separately and smaller

`planProject` compares the declared keys with the live project, which needs no marker:

- a key Incus can change after creation (`limits.*`, `restricted.*`) is updated;
- a key it cannot change once the project holds objects (`features.*`) is **BLOCKED with the reason**, not silently "no changes".

The create-only comment in `planProject` ("a project's feature flags are a one-way door") is the reason for the second rule, and it is also the
reason the first is safe to add.

## Details that need a decision

| Question | Suggested answer |
|---|---|
| A device that also comes from a profile | Compare **local** devices only. Removing a local device may reveal the profile's device of the same name; `plan` should say so |
| Secrets in the marker | None: names only |
| A running instance when a device is removed | Same as a changed device today: Incus may refuse or need a restart; the error surfaces, and `restart: true` keeps its meaning |
| Two stacks declaring one instance | The marker carries the stack name; `plan` warns on takeover (same as volumes) |
| `environment.*` copied from the image | Never listed or removed: it is the image's, as `export` already decides |
| Marker size | A list of names; bounded by the instance's keys and devices |
| `profiles:` on existing instances | Diff it (add to Layer 1/2 as a declared-but-unchecked field) |
| Does Layer 2 also cover profiles and volumes? | Profiles yes (same shape: config keys and devices); volumes already have `user.tink.stack` |

## Alternatives considered

| Alternative | Why not |
|---|---|
| Send the declared config as the whole instance (authoritative PUT) | Takes the instance over, and drops Incus-written keys. The opposite of "manage only what you are told" |
| A tink state file, as Terraform has | Rejected by the project's own rule; a second source of truth that can drift from Incus |
| One label per owned key (`user.tink.owned.config.X`) | Many keys, noisy `incus config show`, no gain over one list |
| Do nothing, document that "no changes" means "declared things match" | Honest but leaves the phase gate weak, and removals impossible |

## What this serves

It is a prerequisite for opinions of the kind `opinion-as-code.md` describes, because an opinion can only judge what it knows tink manages. "A LAN leg
must carry a reason" needs to know the leg is the stack's; "this instance departs from the default" needs a record of which instance. It also gives the
`accept:` ledger something to attach to.

## Questions for the author

1. **How noisy is Layer 1?** Cheap to find out and read-only: run it over every stack file on the lab host (31 on the evaluation host) before deciding
   anything else. If real stacks show a lot of undeclared extras, Layer 2 matters more; if few, Layer 1 alone may be enough for now.
2. Is `user.tink.applied` the right name and shape (names only, one list), or one list per kind (`applied.config`, `applied.devices`)?
3. Should Layer 2 removal be on by default, or behind a flag until adoption has been run on real hosts? The safe default is "on, because the first apply
   removes nothing and later removals are shown in `plan` first", but that is a judgment to make.
4. `exclusive` at all, or leave Layer 3 until someone asks?

## Smallest first step

Layer 1 for instances, and the project comparison (S2). Both are read-only or additive and answer "what is different" truthfully. Layer 2 follows once
Layer 1 has been run over real stacks.

## Appendix: how the claims were verified

All read-only (`plan` never writes), against a disposable project on the evaluation host, 2026-10-12, with `tink v0.1.4`.

```
# S2: the stack declares limits.containers: "5" for project ha-eval. Live: `incus project get ha-eval limits.containers` prints nothing (unset).
$ tink --remote tron plan --offline stack.yaml
level 0:
  project/ha-eval: no changes

# S1: the stack declares instance ha-restore-b with only an eth0 device and only environment.TZ.
#     Live: devices are eth0 and `config` (a disk device); limits.cpu is 2.
instance/ha-restore-b: would update
    device.eth0: map[ipv4.address:10.10.10.232 network:incusbr0 security.acls:ha-lab-egress type:nic] -> map[network:incusbr0 type:nic]
```

The second plan reports the one declared device that differs and says nothing about the undeclared `config` device or `limits.cpu`.

## Appendix: Layer 1 sizing run (2026-10-12)

`UndeclaredExtras` (branch `eval/undeclared-extras`) was run, read-only, over every stack file on the evaluation host: 33 files, 104 instance
declarations. 100 of those name an instance that exists, and 83 of the 100 repeat an instance already counted (the "mirror" files are many variants
of the same few live instances), which leaves **17 distinct live instances**: four Immich containers, mosquitto, zigbee2mqtt, frigate, the Matter
server, the X10 bridge, three Home Assistant lab containers, three system containers, a sandbox container and a stopped test VM.

| Run | Clean | With extras | Why |
|---|---|---|---|
| First | 10 of 17 | 7 | three were classifier gaps (below), four were real |
| After two classifier fixes | **13 of 17** | **4** | every remaining line is a true difference |

**The three false positives, and the fixes.**

- `user.tink.exec.<name>.trigger-hash`, on two instances: tink's own record that a `kind: exec` ran. It is state, not declaration, so it is excluded.
- `environment.HOME: /home/node`, on the Matter server: Incus writes the home directory of the user an OCI process runs as. `export` knew only
  `/root`, so for a non-root user it treated HOME as something a person wrote. Fixed in `export` itself (so `export` also stops writing it), with the
  same rule shared by the report.

**The four real ones.**

- `boot.autorestart` on zigbee2mqtt and frigate, and on mosquitto together with its `config` and `data` volume devices. All were set or added after the only
  YAML for them, a snapshot taken earlier, was written. For mosquitto a current stack exists and is clean; for the other two nothing current declares them.
- A `proxy` device `web8123` on a stopped test VM that its stack file does not declare.

**What the run cannot see.** A key or device that was *removed* from a stack and is still live: that is the case Layer 2's record exists for, and
nothing in this sample shows how common it is. It also cannot see an instance with no stack file at all.

**Reading.** Noise is low once tink's own stamps and Incus's HOME are excluded, and every line left is something a reader would want to know. That
favours shipping Layer 1 in `plan` first, as a quiet per-instance line, and deciding Layer 2 on its own merits. Caveats: 17 instances is a small, mostly
OCI sample; two of the mirror files could not be loaded at all (they use a field name from before it was renamed); and the registry lookups that tell
the image's own `oci.*`/`environment.*` keys from a person's need network access, without which every such key would count as an extra.
