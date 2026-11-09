# Ownership stamps

**The applied record (#312, the third intent key below) is built and unit-tested against fakes; it has not been run against a real Incus server yet (the live checks are listed under [Not run live](#the-applied-record)).** **Status: phase 1 ("Write") is built and was run live on a disposable Incus 7.5.1 server (2026-10-20); phases 2 and 3 are not built. The resource stamp (#309, the second stamp below) is built, unit-tested against fakes, and run live on a disposable Incus 7.5.1 server (2026-11-09, L-2026-11-09b): see [Run live](#run-live).** The design is issue #133 (the whole of it: the rules, the removal that is not built, the open questions); this page records what exists. Phase 1 is #127.

An ownership stamp is one config key, `user.tink.intent.stack`, whose value is the name the stack gives itself with a `kind: stack` document. It answers "which stack made this object?" from the object, without a state file, the way a volume's backup policy already lives on the volume. It is a record, not a lock: nothing in phase 1 decides what may be removed, and tink still never deletes an object. It is one of tink's *intent* keys (written from the declaration, under `user.tink.intent.`); what an intent key is, and the facts and outcomes beside it, is defined once, in the [glossary](glossary.md#key-classes) and `internal/ownership`.

## The resource stamp, and the five states

A stamp that says only "stack lab" is true of a **copy** too: `incus copy` keeps every `user.*` key of the original, and so do a volume copy, a profile or a project made with `show | create`. So beside the owner stamp, tink writes a second intent key, `user.tink.intent.resource`: **the object's own address**, which says what the object was made for. ("Resource" here is the Incus object's address in tink's form, not a document in a stack.) One function, `ownership.Address` in `internal/ownership`, builds it, and the same string is the subject key used in status, logs and notifications:

| Kind | Address |
|---|---|
| instance, profile | `instance/PROJECT/NAME`, `profile/PROJECT/NAME` |
| storage volume | `storage-volume/PROJECT/POOL/NAME` |
| image | `image/PROJECT/ALIAS`: the project the stack declares and the alias tink looks the image up by |
| project | `project/NAME` |
| network, network ACL | `network/NAME`, `network-acl/NAME` (server-global: no project) |

An empty project or pool is written `default`, so a declaration that leaves it out and a listing that spells it out meet.

A reader compares the stamp with the object's real address (`ownership.Classify(config, stack, address)`) and gets one of five states, and **acts only on `here`**:

| State | The object carries | Means |
|---|---|---|
| unstamped | no owner stamp | made before stamps, by hand, or by another tool |
| elsewhere | another stack's owner stamp | that stack's |
| here | this stack's owner stamp and a resource stamp equal to its address | this stack's, and the stamps are meant for it |
| copy | this stack's owner stamp and a resource stamp that is another address | made from another object, and carrying what that one was made for |
| unbound | this stack's owner stamp and no resource stamp | made before the resource stamp existed: not yet told from a copy |

Who acts on it: the backup scheduler schedules a volume only when it is not a copy or unbound, and says so with one of two fixed phrases as `skipped` states in the helper's status document ([the states list](helper.md#the-states-list)) ("its stamps were made for another object (a copy?)", "it has no resource stamp yet (plan apply writes it)"); a volume that no stack stamped is scheduled as before. The join built from the server, and the derived files the daemon keeps, read an instance that is a copy or unbound *without its stamps* (its publish, site and derive stamps) and report one finding for it, so a copy of a published instance is not a second publisher and does not block the site's derived files; its facts (`role`, `leg.*`, `offer.*`) are a person's and are still read, so a copy of a helper still counts as a helper until it is renamed or deleted. tink's own copies of a volume (a restore point, a restored volume) keep both stamps, so they read as copies; restore points are left out of the copy count in `plan` by their markers. The planned prune and healing of #133 are not built; they will act only on `here`.

## What is stamped

Every object tink **creates** for a named stack carries both stamps in the creation request itself, so there is no moment (and no crash between two calls) in which it exists without them:

| Kind | Where the stamp is |
|---|---|
| project, profile, network, network ACL | the object's `config` |
| storage volume | the volume's `config` (volumes carried it before the other kinds did) |
| instance | the instance's own `config` (its local config, never the expanded one), in the creation request |
| image | the image's `properties`, written into the `metadata.yaml` of the import (an image has no `config`) |

Not stamped: a project that is only **named** by `project:` (it is a reference, never made by the stack); the daemon helper and its project (`kind: helper` is installed by the helper's own installer, which makes the project); and the declarations that are no object (`file`, `exec`, `backup-target`, `publish`, `stack`). A stack with **no** `kind: stack` name stamps and adopts nothing, as before, and `plan` says so in one line.

## Objects that exist already

The stamps are a **dimension** of a plan, outside the diff of every kind:

- a difference in a stamp is never "would update", never changes the exit status, and never appears as a change line;
- `plan` ends with up to three **grouped lines** about declared objects that exist, one for each way their stamps are not right:

  ```
  ownership: 5 declared objects have no stamp (made before stamps, or by hand): instance/ct@app, network/br, network-acl/in, profile/base@app, project/app; nothing changes because of it; `tink plan apply --adopt` stamps them in one pass and restarts none
  ownership: 2 declared objects carry stamps made for another resource (copies?): instance/web-copy, storage-volume/data-copy; nothing acts on them and nothing changes because of it; `tink plan apply --adopt` takes them over for this declaration in one pass and restarts none
  ownership: 3 declared objects have no resource stamp yet: instance/ct@app, profile/base@app, project/app; `tink plan apply` writes it and restarts none
  ```

  A stack whose existing objects are all stamped for themselves, or that has none yet, prints no line; a stack with no name prints one line saying so instead. The create of a storage volume does not list the stamps, because they are not part of what a declaration converges to. A restore point of a declared volume is not counted as a copy: it carries its source's stamps on purpose, and is known by its markers.
- an ordinary `plan apply` **keeps the stamps current on objects this stack already owns**, and only those: an *unbound* object (this stack's owner stamp, no resource stamp) gets its resource stamp written. When the object has a real change to apply, the stamp goes in the same write; otherwise it is a stamp-only write, the narrow one `--adopt` uses, and the object still reads "no changes". (From #312 it also writes the [applied record](#the-applied-record) that way, when the object has none or an out-of-date one.) It restarts nothing, whatever the instance's `restart:` says, and a test with a recording server fails if it does. It never claims an object that is *unstamped*, a *copy* or *elsewhere*;
- an object stamped by **another** stack is, in phase 1, treated as it was before: nothing is blocked and nothing is taken over. A volume says so in a warning. Blocking this, and `--adopt-from`, are phase 2 and are not built.

## The applied record

The owner and resource stamps say whose an object is. A third intent key, `user.tink.intent.applied`, says **what tink applied to it**: the config keys it set (with their values), the devices it gave (as whole blocks), the profile list of an instance, and the rules of a network ACL, as the stack declared them the last time tink wrote the object. It is one line of JSON with a `proto`; the code is `internal/applied`, a leaf package (it imports only `internal/ownership`) so the resolver and a daemon can both read it and neither imports the other.

**Why.** A declaration only says what an object should have. A key or a device that the stack used to declare and has dropped is, without a record, the same as one a person added by hand: tink cannot tell them apart, so it could not take it back. With the record it can, exactly, and the repository is the truth for what it declared. A later healing pass has what it needs to compare live state against.

**What is written, and when.** It is written **in the same call as the config it describes**, so after an ordinary apply the record and the config cannot disagree, and a disagreement means a hand edit:

| Kind | Where the record goes |
|---|---|
| profile, storage volume, network, network ACL | the creation request, and the update's own write |
| instance | the one PUT that `ApplyConfig` makes (creation first makes the instance with the two stamps, then sets config, devices and the record in that PUT), and every later update |
| project, image | nothing: they are create-only, so there is nothing `apply` could change. They get a record when `apply` can change them |

An ordinary `plan apply` on an object this stack owns (`here`, or `unbound`, whose resource stamp is written at the same time) that has no record, or an out-of-date one, writes it: in the same write as a real change, or alone through the narrow stamp-only write otherwise, which restarts nothing, whatever `restart:` says (a test with a recording server fails if it does). It never writes a record on an object that is unstamped, a copy or another stack's. `--adopt` writes the two stamps only; there is no adopt-time special case, the next ordinary apply writes the record. A stack with no name owns nothing and records nothing.

**What is in it, and what is not.** The keys a person declares in `config:` (facts such as `user.tink.role` included) with their values; each device as the block that was applied; the declared `profiles:` of an instance; both rule lists of an ACL, including the ingress rules the derive stage filled in. Left out, because another pass owns them: the other intent stamps (owner, resource, publish, site, derive, backup policy), outcomes, the snapshot keys a volume's `backup:` block writes (the backup capability owns them, and `backup: none` deliberately leaves a schedule alone), and a volume's creation-only `initial.*` keys, which Incus reads once. A recorded profile list is for a later healing pass: nothing takes a profile off an instance, because `apply` never changes an instance's profiles.

**Secrets and size.** The record never holds a value that must not be copied, and it is never a snapshot. A key whose value held a `${secret:}` reference, a key whose name looks like a secret (`secrets.SensitiveKey`), a device with such a field, and any value over **2 KiB** (cloud-init user data and long environment values are the realistic ones) are listed **by name only**: they are still tink's, so removing one works, but their values are never stored and never put back. `plan` says in a note which values were too large. A record over **32 KiB** is cut to names (no values, profiles or rules), with a warning; one that is too long even then is not written, with a warning, and nothing is removed because of it. The limits are tink's: Incus has no limit of its own below the whole request body (1 MiB, shared by everything in one write) [lab, Incus 7.5.1, 2026-11-09].

**An empty value is not recorded.** Incus does not store an empty config value: a key declared `""` is absent from the object after the write [lab, Incus 7.5.1, 2026-11-09]. So the record never lists one, and its absence is neither drift (`plan` already read it so) nor a hand removal, nor a stale key.

**Removal.** Keys and devices that the record lists, the object still has and the declaration no longer gives are a planned removal, shown by `plan` as change lines and made by `plan apply`, **by default**:

```
instance/app: would update
    config.limits.cpu: "2" -> (unset) (applied by this stack before, no longer declared)
    device.scratch: removed (type=disk path=/scratch source=tmp; applied by this stack before, no longer declared)
```

One function, `applied.StaleApplied`, decides them, and `plan` and `apply` both ask it (through `staleFor`), the way they both ask `instance.StaleOwnedKeys` for the intent keys, so the two cannot disagree. Everything else about it is conservative:

- **Only what the record lists.** A key or device added by hand is not in the record and is never removed; `plan --extras` still reports it, and a key that is a planned removal is not also reported as left alone. A first apply on an object with no record removes nothing: it writes the record, and the next one can use it.
- **Only where it is true.** The record is trusted only on an object whose resource stamp matches (`here`). On a copy, an unbound object, an unstamped one and another stack's it is ignored, in `plan` and in `apply`, so a copy of an object cannot cause a removal on the original or the other way round.
- **Never tink's or Incus's own keys.** `StaleApplied` never returns a key under `user.tink.intent.`, `user.tink.outcome.` or `volatile.`, even from a record that lists one.
- **A secret-named key that is removed is reported without its value** (`(value hidden) -> (unset)`).
- **A network ACL** replaces a direction it declares as a whole, as before. A direction it no longer declares at all loses the rules the record lists there, and a rule added by hand stays.
- **A bridge that instances use** counts a removed key as a change to it: it needs `allow_disruption: true` like any other, except an ACL attachment (`security.acls`), for the reason given for the same exception on a set.
- **A record that cannot be read** (not JSON, or another protocol) is treated as absent, said in one fixed phrase in `plan`'s grouped line, and replaced by the next apply; nothing is removed because of it.

**Display.** The record is a derived stamp, so it is never a change line and never an exit status, and `plan` never prints its value. Where a declared object that is `here` has no record, or one that this declaration changes, `plan` ends with one more grouped line each, which says only that:

```
applied record: 2 declared objects have no record of what was applied yet: instance/app, profile/base; `tink plan apply` writes it, in the same write as any change and otherwise alone, and restarts none
applied record: 1 declared object whose record of what was applied is changed by this declaration (a derived stamp: its value is not shown): instance/web; `tink plan apply` writes the new one and restarts none for it
applied record: the applied record cannot be read on 1 declared object: profile/base; it is treated as absent, so nothing is removed because of it, and `tink plan apply` writes a new one and restarts none for it
```

**By class.** It is under `user.tink.intent.`, so by its prefix it is refused in `config:` and `properties:`, dropped by `tink export`, never listed as an extra, never taken back by the owned-prefix removal, and dropped from tink's own volume copies (the owner and resource stamps stay; the record of the volume it was copied from is not true of the copy).

**Not run live:** against a real server, writing a stack and reading the key with `incus config show`; removing a device from the YAML, seeing `plan` show it and `apply` remove it while a device added by hand stays and is reported; and an apply to an existing owned instance writing the record with no restart. Everything above is unit-tested against fakes only, with mutation checks (a broken create, update, removal, trust check or empty-value rule makes its test fail).

## Adopting

`tink plan apply --adopt` (or `ownership: adopt-on-apply` on the `kind: stack` document, which makes it standing) **claims**, before anything is converged, every declared object that exists and either has no owner stamp or is a copy, by writing both stamps in one stamp-only update (a copy's old resource stamp is replaced by the object's own address). It changes ownership and nothing else:

```
adopt: stamped user.tink.intent.stack and user.tink.intent.resource on 14 objects (9 instance, 3 profile, 2 storage-volume); restarted 0
adopt: 2 of them carried stamps made for another resource (copies); they now say they were made for the object the stack declares
```

An object owned by *another* stack is left alone, and so is an unbound one, which the apply that follows writes (an object this stack owns already is not "adopted"). `--adopt-from` is not built, so there is no way yet to take an object another stack stamped other than the by-hand one below.

It is a pass of its own and not a part of converging, because converging would make three mistakes the design lists:

- **No restart.** An instance is stamped with a PATCH of those two config keys; it never reads `restart:` and calls nothing that stops, starts, restarts or freezes. A test with a recording server fails if it does. `restarted 0` counts what *adopting* restarts, which is nothing by construction; an ordinary apply that follows it may still restart an instance whose own `restart: true` update it carries out.
- **No noise.** The stamps are outside the diff, so adopting is never an "update", and the plan after it reads "no changes".
- **No guess.** It claims only what is declared, exists, and has no stamp or is a copy: an object stamped by another stack is left (phase 2), an object that is not there is created stamped by the apply, and a network Incus does not manage (a LAN bridge) or that is not a finished bridge is left alone and listed as skipped.

**A volume stamped by another stack.** Before this change a storage volume stamped by another stack was planned as an update and the next apply took it over by rewriting the stamp. It now only warns, because a stamp is not a difference and taking it over would be a stamp write on every apply; "stamped elsewhere" becomes BLOCKED in phase 2 of #133. Until `--adopt-from` exists, a volume that really moved to this stack (a renamed stack, for instance) is moved by hand: `incus storage volume unset POOL VOLUME user.tink.intent.stack`, then `tink plan apply --adopt`. A volume renamed by hand with `incus storage volume rename` is a copy by this page's definition (its resource stamp names the old name), and `--adopt` makes it managed again.

A project and an image are create-only for everything else, so each gets a narrow update that changes those two keys: a project's write echoes its own config, and an image's write echoes its `profiles` (Incus resets them to `["default"]` when a write leaves them out). A profile, volume, network and ACL are a read and a write of the object with the keys added, under the etag read, so a concurrent edit is never overwritten: Incus refuses the write, and it is read again and redone (up to three times, `incusapi.RetryOnConflict`), which asks again whether the object is still one that is claimed.

A running **virtual machine** can be stamped live, but Incus marks it as needing a reset (`volatile.vm.needs_reset`), so its next reboot is a full restart and not a warm one. Adopt prints a line for it; it cannot be avoided.

## The stamps cannot be declared or exported

- `validate` (the load of any stack file) refuses `user.tink.intent.stack` and `user.tink.intent.resource` (every key under `user.tink.intent.` is refused, by its prefix) in `config:` of every kind that has one, and in an image's `properties:`; for the owner stamp it names `--adopt` as the way to take an object that was made earlier.
- `tink export` never writes either (nor the applied record), for the project, the volumes or the instance it exports, so an exported stack can be applied under any name; its self-check still reads "no changes".
- `plan --extras` does not list either among what a stack leaves undeclared.
- The network disruption guard does not count the owner stamp, as it does not count ACL keys: a stamp alone never blocks a bridge that instances use.

## Where the code is

`internal/applied` is the applied record (the document, `Build`, `Encode`, `Parse`, `Trusted` and `StaleApplied`); the resolver's part is `internal/resolve/applied.go` (what each kind records, `stampedConfig` on a creation, `staleFor` and the plan lines, `upkeepJob`), `instance.Spec.StaleApplied` for the instance's one PUT, and the display in `ownership.go`. `internal/ownership` is the keys, `Address`, the classification (`Classify`: unknown, unstamped, here, elsewhere, copy, unbound) and `With` (a copy of a config carrying both stamps); it imports nothing of tink's, so the daemon can import it without `resolve`. The resolver's part is `internal/resolve/ownership.go` (the plan dimension, the lines, validation) and `adopt.go` (the stamp-only write, used by `--adopt` and by an ordinary apply). The readers that act only on `here` are `backuprun.Discover` and `join` (`stamps.go`). `backupmeta.StackKey` is the same constant.

## Run live

On 2026-10-20 on a local test host (a disposable Incus 7.5.1 server, tink built from this change, throwaway projects deleted afterwards):

- A stack applied to a server without any of its objects created a project, a bridge, a network ACL, a profile, a volume and a running container, and each carried `user.tink.intent.stack` straight away; the next plan and apply read "no changes". An image imported from a qcow2 by a `kind: image` carried it in its properties.
- Objects made by the previous build (a running container with `restart: true`, its project, profile, bridge in use by that container, ACL) planned identically with the old and the new build apart from the one ownership line, exit status 0 both. `plan apply --adopt` stamped the five of them; the container's PID and start time were unchanged and the uptime inside it kept counting (no restart), a profile under that running container and the bridge it uses were stamped without blocking, and the next plan read "no changes".
- A running virtual machine made by the previous build was stamped live: its QEMU process was unchanged, and `volatile.vm.needs_reset` was `true` afterwards (it was unset before).
- An image whose profiles were edited to two names kept both through the stamp.
- `ownership: adopt-on-apply` stamped a hand-made profile on the next ordinary apply; without it, an ordinary apply left a hand-made profile unstamped and `plan` listed it.
- Not run: stamping an object on a server that restricts the client to some projects; a bridge with an OVN or physical parent; `--remote`.

## Run live

The resource stamp (#309) was run on a disposable Incus 7.5.1 server over `--remote` on 2026-11-09 (L-2026-11-09b), with the tink of this change and, as the control, the tink of `main` before it:

- **Stamps.** An instance, a profile, a network, a network ACL, a project and a storage volume each carried `user.tink.intent.resource` with their own address (for example `instance/default/whoami`, `network/tink0`, `storage-volume/default/default/keep`). A second plan said no changes for them: the stamp is not a change line.
- **A copy of a published instance.** After `incus copy` of an instance that carries publish stamps, `main` refused to render the site (`error: the name "whoami" is also published by default/<copy's source>: a published name is unique in the zone`, "nothing was rendered") and `tink plan` of the platform stack stopped on it. With this change `plan` of the platform said no changes, and `tink join --from-server` rendered and reported one warning for the copy: its publish, site and derive stamps are ignored.
- **The scheduler.** With `tink daemon run`, a copy made by `incus storage volume copy` of a managed volume was skipped with "its stamps were made for another object (a copy?)", a volume created by the tink of `main` (owner stamp, no resource stamp) with "it has no resource stamp yet (plan apply writes it)", and the original was scheduled (a copy job was queued). The reasons were read from the daemon's published status document.
- **An unbound instance.** An instance created by the tink of `main` was planned with the line "has no resource stamp yet: `tink plan apply` writes it and restarts none", and an ordinary `plan apply` wrote `instance/default/legacy`; its process id and uptime were unchanged, so nothing was restarted.
- **`--adopt`.** A hand-made volume copy that a stack then declared was planned with one grouped line naming it ("carries stamps made for another resource (copies?)"); `plan apply --adopt` rewrote its resource stamp to its own address, and the next plan had no such line.

Not run live: `tink helper status` reading those reasons (the status document was read directly); an image (`kind: image`) carrying the stamp; and a daemon running as a helper instance under a narrowed certificate. The mutation checks of the tests (a broken consumer makes its test fail) were run on fakes.

## Not built

Orphans in `plan`, "stamped elsewhere" as BLOCKED, `--adopt-from OLD`, and removal (`--prune`) are phases 2 and 3 of #133, and wait until something needs them. The `kind: helper` instance is not stamped when the stack installs it.
