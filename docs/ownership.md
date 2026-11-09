# Ownership stamps

**Status: phase 1 ("Write") is built and was run live on a disposable Incus 7.5.1 server (2026-10-20); phases 2 and 3 are not built. The resource stamp (#309, the second stamp below) is built, unit-tested against fakes, and run live on a disposable Incus 7.5.1 server (2026-11-09, L-2026-11-09b): see [Run live](#run-live).** The design is issue #133 (the whole of it: the rules, the removal that is not built, the open questions); this page records what exists. Phase 1 is #127.

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
- an ordinary `plan apply` **keeps the stamps current on objects this stack already owns**, and only those: an *unbound* object (this stack's owner stamp, no resource stamp) gets its resource stamp written. When the object has a real change to apply, the stamp goes in the same write; otherwise it is a stamp-only write, the narrow one `--adopt` uses, and the object still reads "no changes". It restarts nothing, whatever the instance's `restart:` says, and a test with a recording server fails if it does. It never claims an object that is *unstamped*, a *copy* or *elsewhere*;
- an object stamped by **another** stack is, in phase 1, treated as it was before: nothing is blocked and nothing is taken over. A volume says so in a warning. Blocking this, and `--adopt-from`, are phase 2 and are not built.

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
- `tink export` never writes either, for the project, the volumes or the instance it exports, so an exported stack can be applied under any name; its self-check still reads "no changes".
- `plan --extras` does not list either among what a stack leaves undeclared.
- The network disruption guard does not count the owner stamp, as it does not count ACL keys: a stamp alone never blocks a bridge that instances use.

## Where the code is

`internal/ownership` is the keys, `Address`, the classification (`Classify`: unknown, unstamped, here, elsewhere, copy, unbound) and `With` (a copy of a config carrying both stamps); it imports nothing of tink's, so the daemon can import it without `resolve`. The resolver's part is `internal/resolve/ownership.go` (the plan dimension, the lines, validation) and `adopt.go` (the stamp-only write, used by `--adopt` and by an ordinary apply). The readers that act only on `here` are `backuprun.Discover` and `join` (`stamps.go`). `backupmeta.StackKey` is the same constant.

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
