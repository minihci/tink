# Ownership stamps

**Status, 2026-10-20: phase 1 ("Write") is built and was run live on a disposable Incus 7.5.1 server; phases 2 and 3 are not built.** The design is issue #133 (the whole of it: the rules, the removal that is not built, the open questions); this page records what exists. Phase 1 is #127.

An ownership stamp is one config key, `user.tink.stack`, whose value is the name the stack gives itself with a `kind: stack` document. It answers "which stack made this object?" from the object, without a state file, the way a volume's backup policy already lives on the volume. It is a record, not a lock: nothing in phase 1 decides what may be removed, and tink still never deletes an object.

## What is stamped

Every object tink **creates** for a named stack carries the stamp in the creation request itself, so there is no moment (and no crash between two calls) in which it exists without it:

| Kind | Where the stamp is |
|---|---|
| project, profile, network, network ACL | the object's `config` |
| storage volume | the volume's `config` (volumes carried it before the other kinds did) |
| instance | the instance's own `config` (its local config, never the expanded one), in the creation request |
| image | the image's `properties`, written into the `metadata.yaml` of the import (an image has no `config`) |

Not stamped: a project that is only **named** by `project:` (it is a reference, never made by the stack); the daemon helper and its project (`kind: helper` is installed by the helper's own installer, which makes the project); and the declarations that are no object (`file`, `exec`, `backup-target`, `publish`, `stack`). A stack with **no** `kind: stack` name stamps and adopts nothing, as before, and `plan` says so in one line.

## Objects that exist already

An ordinary `plan apply` writes nothing for an object that is already there, stamped or not. The stamp is its own dimension of a plan, outside the diff of every kind:

- a stamp-only difference is never "would update", never changes the exit status, and never appears as a change line;
- `plan` ends with **one** line when declared objects that exist have no stamp:

  ```
  ownership: 5 declared objects have no stamp (made before stamps, or by hand): instance/ct@app, network/br, network-acl/in, profile/base@app, project/app; nothing changes because of it; `tink plan apply --adopt` stamps them in one pass and restarts none
  ```

  That line is the only change to the output of `plan` for a stack whose objects exist. A stack whose existing objects are all stamped, or that has none yet, prints no line; a stack with no name prints one line saying so instead. The one other difference is on a server that lacks the objects: the create of a storage volume no longer lists `config.user.tink.stack`, because the stamp is not part of what a declaration converges to.
- an object stamped by **another** stack is, in phase 1, treated as it was before: nothing is blocked and nothing is taken over. A volume says so in a warning (it said "applying takes it over" until this change; applying no longer rewrites the stamp). Blocking this, and `--adopt-from`, are phase 2.

## Adopting

`tink plan apply --adopt` (or `ownership: adopt-on-apply` on the `kind: stack` document, which makes it standing) stamps, **before** anything is converged, every declared object that exists and has no stamp:

```
adopt: stamped user.tink.stack on 14 objects (9 instance, 3 profile, 2 storage-volume); restarted 0
```

It is a pass of its own and not a part of converging, because converging would make three mistakes the design lists:

- **No restart.** An instance is stamped with a PATCH of that one config key; it never reads `restart:` and calls nothing that stops, starts, restarts or freezes. A test with a recording server fails if it does. `restarted 0` counts what *adopting* restarts, which is nothing by construction; an ordinary apply that follows it may still restart an instance whose own `restart: true` update it carries out.
- **No noise.** The stamp is outside the diff, so adopting is never an "update", and the plan after it reads "no changes".
- **No guess.** It stamps only what is declared, exists, and has no stamp: an object stamped by another stack is left (phase 2), an object that is not there is created stamped by the apply, and a network Incus does not manage (a LAN bridge) or that is not a finished bridge is left alone and listed as skipped.

**A volume stamped by another stack.** Before this change a storage volume stamped by another stack was planned as an update and the next apply took it over by rewriting the stamp. It now only warns, because a stamp is not a difference and taking it over would be a stamp write on every apply; "stamped elsewhere" becomes BLOCKED in phase 2 of #133. Until `--adopt-from` exists, a volume that really moved to this stack (a renamed stack, for instance) is moved by hand: `incus storage volume unset POOL VOLUME user.tink.stack`, then `tink plan apply --adopt`.

A project and an image are create-only for everything else, so each gets a narrow update that changes that one key: a project's write echoes its own config, and an image's write echoes its `profiles` (Incus resets them to `["default"]` when a write leaves them out). A profile, volume, network and ACL are a read and a write of the object with the key added, under the etag read, so a concurrent edit makes the write fail instead of being overwritten.

A running **virtual machine** can be stamped live, but Incus marks it as needing a reset (`volatile.vm.needs_reset`), so its next reboot is a full restart and not a warm one. Adopt prints a line for it; it cannot be avoided.

## The stamp cannot be declared or exported

- `validate` (the load of any stack file) refuses `user.tink.stack` in `config:` of every kind that has one, and in an image's `properties:`, naming `--adopt` as the way to take an object that was made earlier.
- `tink export` never writes it, for the project, the volumes or the instance it exports, so an exported stack can be applied under any name; its self-check still reads "no changes".
- `plan --extras` does not list it among what a stack leaves undeclared.
- The network disruption guard does not count it, as it does not count ACL keys: a stamp alone never blocks a bridge that instances use.

## Where the code is

`internal/ownership` is the key, the classification (`Classify`: unknown, unstamped, here, elsewhere) and `With` (a copy of a config carrying the stamp); it imports nothing of tink's, so the daemon can import it without `resolve`. The resolver's part is `internal/resolve/ownership.go` (the plan dimension, the line, validation) and `adopt.go` (the pass). `backupmeta.StackKey` is the same constant.

## Run live

On 2026-10-20 on a local test host (a disposable Incus 7.5.1 server, tink built from this change, throwaway projects deleted afterwards):

- A stack applied to a server without any of its objects created a project, a bridge, a network ACL, a profile, a volume and a running container, and each carried `user.tink.stack` straight away; the next plan and apply read "no changes". An image imported from a qcow2 by a `kind: image` carried it in its properties.
- Objects made by the previous build (a running container with `restart: true`, its project, profile, bridge in use by that container, ACL) planned identically with the old and the new build apart from the one ownership line, exit status 0 both. `plan apply --adopt` stamped the five of them; the container's PID and start time were unchanged and the uptime inside it kept counting (no restart), a profile under that running container and the bridge it uses were stamped without blocking, and the next plan read "no changes".
- A running virtual machine made by the previous build was stamped live: its QEMU process was unchanged, and `volatile.vm.needs_reset` was `true` afterwards (it was unset before).
- An image whose profiles were edited to two names kept both through the stamp.
- `ownership: adopt-on-apply` stamped a hand-made profile on the next ordinary apply; without it, an ordinary apply left a hand-made profile unstamped and `plan` listed it.
- Not run: stamping an object on a server that restricts the client to some projects; a bridge with an OVN or physical parent; `--remote`.

## Not built

Orphans in `plan`, "stamped elsewhere" as BLOCKED, `--adopt-from OLD`, and removal (`--prune`) are phases 2 and 3 of #133, and wait until something needs them. The `kind: helper` instance is not stamped when the stack installs it.
