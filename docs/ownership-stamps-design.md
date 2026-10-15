# Ownership stamps and scoped removal

**Status: a design note for review, 2026-10-14. Nothing in it is built.** Claims are marked **[code]** (read in this repository, or in the Incus 7.4.0 source tink builds against; nothing was run), **[docs]**, **[proposed]** (not built) or **[to confirm]** (needs a run on a real host). It answers [open decision 1](what-tink-is.md#open-decisions) and [question 3 of the offer/grant note](offer-grant-join-design.md#open-questions). Four words would join the [glossary](glossary.md): *owner*, *orphan*, *adopt*, *prune*.

## Problem

tink creates and updates but never deletes: what a stack stops declaring is left behind **[docs: what-tink-is.md]**, and `plan` cannot say so, because its "not in the stack" line looks inside one instance, never at whole objects **[docs: plan-extras.md]**. The only ownership record is `user.tink.stack`, on **storage volumes** only, written when the stack is named and never removed **[code: `backupmeta.StackKey`]**. For an instance, profile or bridge nothing answers "is this mine?", so nothing safe can answer "may it go?". A removed grant leaves the access it gave **[docs: offer-grant-join-design.md, question 3]**, and the daemon helper cannot find a stack's objects.

**Decision taken:** stamp ownership on everything tink creates; stamp an object-less declaration only when something other than `plan apply` must act on it later; defer last-applied intent for keys and devices (Layer 2 of [authoritative-apply-design.md](authoritative-apply-design.md)). The model is Compose's `--remove-orphans` and kubectl's `--prune`: a label says whose it is, and removal is a separate explicit act.

## Goals and non-goals

1. Every object tink creates names its stack in the object, from its creation request.
2. Removal is explicit, run by a person, scoped to one stack and to orphans, previewed first, never the default.
3. Introducing stamps on a running host causes no restart and no wall of "would update".
4. The daemon helper reads stamps and cannot delete.

Non-goals (v1): remembering which keys, devices or files a stack once applied; deleting hand-made objects; removing a whole stack (use `incus`); stamping what `tink run`, `tink deploy` or `tink helper install` create; state outside Incus.

## What gets a stamp

One key, **`user.tink.stack`**, value = the stack's name (lower case, digits, `.`, `_`, `-`, at most 63 **[code: stack.go]**), in every row below unless the row says otherwise. The value stays the plain name: a timestamp or version would change on every apply and churn every object.

| Kind | Stamp lives in | Prunable | BLOCKED when |
|---|---|---|---|
| project | project `config` | yes, last, only if empty | it holds any object, stamped or not; `default`. A project merely named by `project:` is a reference: never stamped |
| profile | profile `config` | yes | `used_by` is not empty; `default` |
| storage-volume | volume `config` (exists today) | yes | attached to an instance; and without `--prune-data` always, since tink cannot tell empty from full |
| instance | instance `config` (read the local one, never the expanded), written in the create request | yes | running or frozen; `security.protection.delete`; a VM or one with snapshots needs `--prune-data` |
| network | network `config` | yes | `used_by` is not empty; Incus does not manage it (already BLOCKED **[code: `planNetwork`]**) |
| network-acl | ACL `config` | yes | `used_by` is not empty |
| backup-target | none: no object. The volume's copy policy carries the resolved target, and that is what the daemon reads **[docs: daemon-jobs.md]** | nothing to remove | one still named by a declared copy is already a load error **[code]** |
| image | image `properties` | report only in v1 | an instance's `volatile.base_image` is its fingerprint |
| file | none: it lives in an instance, so pruning the instance removes it. A file dropped from a stack stays (Layer 2) | no | n/a |
| exec | none; its `user.tink.exec.<name>.trigger-hash` on the instance becomes an owned prefix (R10) | key only | n/a |
| helper | instance `config`, only when `kind: helper` installs it; adopting changes nothing **[docs: helper.md]** | never: `tink helper remove` | listed as an orphan, "not prunable: tink helper remove". Its project is made by the installer: never stamped |

**The test for an object-less declaration:** stamp it only if something other than `plan apply` must act on it later. Today only `kind: publish` passes: it is stamped onto its instance as `user.tink.publish.<name>`, an owned prefix that tink removes when the stack stops deriving it **[proposed: the publish-stamps branch, unmerged]**. Backup targets, files and execs fail the test.

## Rules

- **R1 Owner.** The value of `user.tink.stack` in the object's own config. A stack with no `kind: stack` name stamps, adopts and prunes nothing, as today; `plan` says so once.
- **R2 Orphan.** A live object of a prunable kind, stamped with this stack's name, not declared by the files given, and not a restore point (`user.tink.backup.copy-of` or `copy-partial-of`; a restore point is a copy and can carry the source's config, stamp included **[docs: daemon-jobs.md]**). Identity is kind, project, pool (volumes), name, so moving a resource to another project leaves an orphan.
- **R3 Hand-made** means no stamp: never an orphan, whatever its name.
- **R4 Conflict.** Declared here, stamped by another stack: BLOCKED, naming the owner (a volume only warns today **[code: `decideVolume`]**). So when two stacks declare one object, the first to apply owns it and the second is BLOCKED. Duplicates inside one stack are already rejected **[code: graph]**.
- **R5 Adopt.** A declared object with no stamp is not touched by an ordinary apply; `plan` reports them in one line. `plan apply --adopt` stamps them, and `ownership: adopt-on-apply` on `kind: stack` makes that standing. `--adopt-from OLD` also takes over declared objects stamped OLD. (`kind: helper` says "adopt" for "accept and leave alone"; this one stamps.)
- **R6 Rename a stack.** Every object then reads as foreign and is BLOCKED; `plan apply --adopt-from OLD` restamps the declared ones. Undeclared objects stamped OLD stay OLD's, not the new stack's orphans.
- **R7 Multi-file.** A stack is exactly the files given to one invocation, with one `kind: stack` **[code: `StackName`]**. Orphans are computed against those files, so planning half a stack lists the other half as orphans. `plan` prints the files it considered; split a stack only by naming each part.
- **R8 Project default.** `project:` on `kind: stack` only places resources that name no project **[code]**. Orphans are searched on the whole server by stamp, so changing it shows the old objects as orphans instead of losing them.
- **R9 Wrong-name guard.** Prune refuses when none of the stack's declared objects carries its stamp. That catches a mistyped name equal to another stack's, whose objects would read as orphans; it is also why whole-stack removal is out of scope.
- **R10 Keys are not objects.** Removing a key tink derives from a declared object is healing derived state and stays in plain `plan apply` (a stale `user.tink.publish.<name>` keeps a name routed). Only for owned prefixes (`user.tink.publish.`, `user.tink.exec.`), never for an object **[proposed]**.

## What the user sees

`plan` (phase 2) adds one section after the levels, as the extras count and helper note do; an orphan has no place in the dependency levels. The exit status does not change.

```
ownership, stack smarthome (tink.yaml, apps.yaml: 31 declared, 28 stamped):
  3 orphans: stamped by this stack, no longer declared. Nothing was removed.
    instance/old-ui          Stopped    prune would delete
    profile/old-ui                      prune would delete, after instance/old-ui
    storage-volume/old-data  detached   BLOCKED: holds data (--prune-data)
  instance/ha-b is declared here and stamped by stack "lab": BLOCKED (see its line above)
```

- **`plan apply --prune`** converges as usual, then prints that section as a dry run with a **token** (six hex digits of a SHA-256 over the server, the stack, what would be deleted, and whether `--prune-data` was given). It deletes nothing.
- **`plan apply --prune=TOKEN`** converges; only if that left nothing BLOCKED or failed does it re-evaluate. If the deletable set no longer hashes to the token it refuses and prints the new list. Otherwise it deletes in the order instance, profile, volume, network, ACL, project, re-checks each guard before each delete, and stops at the first failure. Nothing is deleted that was not shown; no state is kept.
- **Extras** is unchanged (keys, devices, profiles inside an object; never deleted), minus the stamp key. `validate` refuses the key in `config:` for every kind, as it does for volumes now **[code: `validateVolumeConfig`]**.
- **`tink export`** never writes the stamp (it drops it for volumes today **[code: export.go]**; extend to all kinds), and adds a comment naming the stack when the objects agree. It does not write `kind: stack`, so its self-check stays "no changes".

## First-apply churn

A host with 17 live instances declared **[docs: authoritative-apply-design.md, appendix]** would show 17 "would update" lines and the first apply would write each. Three separate hazards:

1. **Noise.** The stamp is derived into desired state outside `diffConfig`, as its own ownership dimension of the planned resource. A stamp-only difference is never "would update", never changes the exit status, and prints as one grouped line.
2. **Restart.** `updateInstance` restarts a running instance after any in-place update when `restart: true` **[code: apply.go; `EnsureRunning` restarts a Running one, run.go]**. A stamp-only write needs its own path that sets one key and never reads `Restart` **[proposed]**, with a test that fails if it restarts.
3. **Disruption guard.** The network guard counts every key except ACL keys **[code: `isACLKey`]**, so a stamp on a bridge in use would be BLOCKED. The stamp key is exempted the same way.

**How.** New objects are stamped in their create request (for an instance, out of the post-create config step, which also closes the window where a crash leaves one unstamped). Existing objects are untouched until adopted, so an ordinary apply on an existing host writes nothing. `--adopt` is the one pass:

```
ownership: 14 declared objects have no stamp (made before stamps, or by hand): instance/ha, instance/mqtt, ... (+12)
           nothing changes because of it. `tink plan apply --adopt` stamps them in one pass; none is restarted.
adopt: stamped user.tink.stack=smarthome on 14 objects (9 instance, 3 profile, 2 storage-volume); restarted 0
```

**What Incus does with a `user.*`-only change** **[code: Incus 7.4.0; to confirm live]**:
- Running container: a database write, a backup-file rewrite and an event to the guest API; no restart. Bridge: `user.*` is left out of the changed keys that trigger a re-setup. Volume: an explicit user-only fast path. ACL and project accept the key.
- Running VM: allowed live, but any live change also sets `volatile.vm.needs_reset`, so its next reboot is a full restart, not a warm one. Unavoidable; adopt prints it.
- Profile: the update reaches every instance using it, so one live update per instance, with the effects above.
- Image: a PUT resets `profiles` to `["default"]` when omitted, so the write must echo them. Project and image are create-only in tink today **[code: `planProject`, `planImage`, `updateOne`]**: adopting them needs a narrow stamp-only update, not convergence.

## Phases

| Phase | Ships | Ends when |
|---|---|---|
| **1 Write** | Stamp on create for every kind; `--adopt`, `ownership:`; the validate, export, extras and guard changes; a small `internal/ownership` package (key, owner, classification) the daemon can import without `resolve`. Existing stacks see only the ownership line | (a) `plan` over the 30 real stack files differs from the previous build only by that line, the check used before **[docs: what-tink-is.md]**; (b) `--adopt` on a disposable copy, with a running `restart: true` container, a VM and a bridge in use, leaves start times and PIDs unchanged and a ping unbroken; (c) the next plan reads "no changes"; (d) `export` writes no stamp |
| **2 Report** | Orphans in `plan`; takeover BLOCKED; `--adopt-from` | On the author's host every reported orphan is explained by a person (leftover, copy or mistake), and the number of copies read as orphans is recorded (it decides question 4) |
| **3 Prune** | `--prune`, `--prune=TOKEN`, `--prune-data`, the guards | In a lab each guard BLOCKS, the token refuses after the set changes, the order is right; then one real leftover is pruned on the author's host after a snapshot. Docs flip: what-tink-is.md, plan-extras.md, glossary |

## The daemon helper never prunes

Orphans are declared state going missing, and the rule is "derived state heals, declared state reports" **[docs: helper.md]**. Three layers: **code** (the prune executor is its own package that `internal/daemon`, `internal/jobs` and `internal/backuprun` may not import, by an import-scan test like `keynamespace_test.go`; there is no prune job kind) **[proposed]**; **credential** (the restricted certificate of open decision 2a should hold no delete rights on instances, projects, profiles, networks or ACLs **[to confirm]**; volumes are the exception, because backup retention already deletes restore points gated by their markers **[code: `backupmeta`]**, so for volumes only the other two layers hold); **command** (deleting needs the token form of `--prune`, typed by a person). The daemon reads stamps freely and may later heal owned-prefix keys (R10). It does not count orphans in its status in v1.

## Risks

| Risk | What keeps it small |
|---|---|
| Half a stack planned under one name lists the rest as orphans (R7), or a mistyped name equals another stack's | Files considered are printed; the token binds the list; R4, R9 |
| `incus copy` keeps every non-volatile key, so a hand copy of an owned instance carries the stamp and reads as an orphan **[code: Incus `InstanceIncludeWhenCopying`]** | Reported for all of phase 2 before anything can be pruned; `incus config unset NAME user.tink.stack` disowns it; question 4 |
| A stamp-only write restarts an instance or trips the disruption guard | Their own tests, in phase 1 |
| The set changes between the dry run and the delete; the stamp is no security boundary | The token covers the set and guards are re-checked per delete. Anyone who can set a stamp can already delete: it prevents mistakes |

## Test plan

Unit, with the existing fakes: each kind against four live states (absent, unstamped, stamped here, stamped elsewhere); a stamp-only update on a running `restart: true` instance makes no state-change call, and on a bridge in use is not BLOCKED; orphan classification (restore points, unstamped and foreign objects are not orphans; a stamped helper is listed, not prunable); each guard; the token changes with the set and with `--prune-data`, and a refused token deletes nothing; order, stop at the first failure, no prune after a BLOCKED apply; R9; golden output; `export` and `validate` for every kind; the import-scan test; `keynamespace_test.go` still passes. By hand on a disposable host (recorded in the README's "Status" notes): the phase 1 and 3 checks, a profile stamp under a running VM, an image write that keeps its profiles, and whether Incus filters a list on the stamp server-side, else tink lists and filters as the backup scheduler does **[to confirm]**.

## Open questions

Each has my default, so the answer can be "yes" or a change.

1. **Adoption.** Default: explicit (`--adopt`, or `ownership: adopt-on-apply` once per stack). Reason: a stamp is what makes an object prunable, so consent should be one visible act, and an ordinary apply on an existing host writes nothing. Alternative: adopt on any apply of a declared object (declaring it is consent): no ceremony, but the first apply on every existing host is the churn.
2. **An unnamed stack.** Default: stays ownership-blind, one note per run. Reason: it works today and this note need not change that. Alternative: warn on every apply, later require a name.
3. **Takeover.** Default: BLOCKED from phase 2, moved once with `--adopt-from`. Reason: two stacks passing an object back and forth is the failure ownership exists to stop. Alternative: keep today's volume warning.
4. **Stamp value.** Default: the plain stack name. Reason: it matches the decision taken and the stamps already on volumes. Alternative: `<stack>/<name>`, so a copy or renamed object reads as foreign, not as an orphan; it costs a migration for volumes and misses a copy into another project. Phase 2's count of copies decides.
5. **Confirmation.** Default: the token from the dry run. Reason: stateless, scriptable, cannot delete what was not shown. Alternatives: name each object (`--prune instance/old-ui`), or an interactive yes.
6. **Data.** Default: a separate `--prune-data` for volumes, VMs and snapshots. Reason: tink cannot tell empty from full, and a VM's root disk is data. Alternative: the token alone consents to everything listed.
7. **Flag name.** Default: `--prune`. Reason: the decision's wording and kubectl's. Alternative: `--remove-orphans` (Compose's, says what it does); `prune` already means retention for backup restore points.
8. **Images and search scope.** Default: stamp images but prune none in v1 (the gain is disk space, and a rebuild may want the blob); search the whole server by stamp, not only the stack's projects (a move must show). Alternative: prune an unused image; restrict the search, which is cheaper but blind to a move.
