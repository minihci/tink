# What `plan` says about things the stack does not declare

`plan` compares what a stack **declares** with what exists. It has always been one-directional on purpose: a live instance carries plenty of keys tink
did not set (Incus's `volatile.*`, the image's own settings), and none of them is drift. The cost is that a device or config key added by hand, or left
behind when it was deleted from the YAML, was never mentioned, and "no changes" quietly meant only "what you declared matches".

For an instance that already exists, `plan` adds one line under it:

```
  instance/ha-restore-b: would update
      device.eth0.ipv4.address: "203.0.113.232" -> (unset)
      not in the stack (left alone): device.config (disk), config.boot.autorestart, config.limits.cpu, config.limits.memory
note: 1 instance carries devices, config keys or profiles that the stack does not declare (the "not in the stack" lines above).
      plan changes nothing because of it and apply never removes them; declare them to manage them, or ignore the line. See docs/plan-extras.md.
```

For a profile, a storage volume, a network and a project that already exists, the same line lists its config keys (and a profile's devices) that the
stack does not declare:

```
  profile/web: no changes
      not in the stack (left alone): config.limits.cpu, device.scratch (disk)
```

## What it means

The line lists what is **on the object and not in the stack**: for an instance its own devices, the names of its config keys, and profiles; for the other kinds the names of its config keys, and a profile's devices. Nothing is changed or
removed because of it: not by `plan`, not by `plan apply`, and the exit status does not change. Config **values are never printed**, only key names.
It is information for someone asking "is this instance exactly what my YAML says?". Declare a thing to manage it, or leave it and ignore the line.

## What is not counted

The instance is first reduced to what a person set, the same way `tink export` does it, so these are never listed:

- Incus's own keys (`volatile.*`) and the image's (`image.*`).
- `oci.*` and `environment.*` keys whose value is what the image itself says. This needs a look at the image in its registry, as `plan` already does for
  image drift.
- `environment.TERM` and `environment.HOME` when Incus wrote them (the home directory of the user the process runs as).
- Devices a **profile** supplies (only the instance's own devices count).
- tink's own state under `user.tink.helper.`, `user.tink.run.` and `user.tink.exec.`, and the stamps of `kind: publish` under `user.tink.publish.` (a stamp the stack derives is declared; one it no longer derives is a planned removal, the one case where `plan apply` removes a key: see [join.md](join.md#the-owned-prefix-and-the-one-place-tink-removes-a-key)).

For a profile, a storage volume, a network and a project the following are not counted either: `volatile.*` and tink's own `user.tink.*` (a volume's backup policy, stack pointer and copy markers); a volume's snapshot keys that its `backup:` block declares; and on a project, a `features.*` key that still has the value Incus gives it when it creates a project (Incus writes all of them, so a project that declares only some carries the rest). A `features.*` flag with any other value is listed. Incus can also generate other keys when it creates an object (a network's address and NAT keys, say, when the stack does not set them); those are not recognised and are listed, so expect them on a network that declares only some of its keys. This part has been tested against fakes, not a live server.

A key or device the stack **does** declare is never listed, even if its value differs; that difference is an ordinary change `plan` already shows. Profiles are
compared only when the stack lists some.

## When it cannot be complete

If the image cannot be read (`--offline`, or its registry is unreachable), tink cannot tell the image's `oci.*`/`environment.*` keys from yours. Rather than
list every variable the image sets, it leaves those two groups out and says so at the end of the line:
`[environment.* and oci.* not compared with the image]`. Everything else on the line is still reliable. An instance that is not built from an OCI image has
no such limit.

## What it cannot see

- A key or device that was **removed from the YAML** but is still on the instance looks the same as one added by hand, and is listed. What it cannot do is
  tell you that tink once applied it, or remove it: that needs tink to remember what it applied, which is a separate design (#138).
- An instance with no stack file at all.
- Network ACLs and the other kinds (files, execs, images, backup targets) are not compared in this direction. An ACL's rules are converged rule by rule, not left in place.

## There is no switch to turn it off

It is one line per object that has something to say, and silent for one that does not. If it is noisy on your stacks, that is worth knowing: please say
so, with the line you would not want. On the evaluation host (17 live instances declared across 33 stack files) it was silent for 13, and every line on the
other 4 was a real difference: a `boot.autorestart` set by hand after the YAML was written, volume devices a stale snapshot never had, and a proxy device
on a test VM. Before tink's own `user.tink.exec.*` stamps and Incus's `HOME` for a non-root user were excluded it was noisier, which is why they are.
