# What `plan` says about things the stack does not declare

`plan` compares what a stack **declares** with what exists. It has always been one-directional on purpose: a live instance carries plenty of keys tink
did not set (Incus's `volatile.*`, the image's own settings), and none of them is drift. The cost is that a device or config key added by hand, or left
behind when it was deleted from the YAML, was never mentioned, and "no changes" quietly meant only "what you declared matches".

For an instance that already exists, `plan` now adds one line under it:

```
  instance/ha-restore-b: would update
      device.eth0.ipv4.address: "10.10.10.232" -> (unset)
      not in the stack (left alone): device.config (disk), config.boot.autorestart, config.limits.cpu, config.limits.memory
note: 1 instance carries devices, config keys or profiles that the stack does not declare (the "not in the stack" lines above).
      plan changes nothing because of it and apply never removes them; declare them to manage them, or ignore the line. See docs/plan-extras.md.
```

## What it means

The line lists what is **on the instance and not in the stack**: its own devices, the names of its config keys, and profiles. Nothing is changed or
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

A key or device the stack **does** declare is never listed, even if its value differs; that difference is an ordinary change `plan` already shows. Profiles are
compared only when the stack lists some.

## When it cannot be complete

If the image cannot be read (`--offline`, or its registry is unreachable), tink cannot tell the image's `oci.*`/`environment.*` keys from yours. Rather than
list every variable the image sets, it leaves those two groups out and says so at the end of the line:
`[environment.* and oci.* not compared with the image]`. Everything else on the line is still reliable. An instance that is not built from an OCI image has
no such limit.

## What it cannot see

- A key or device that was **removed from the YAML** but is still on the instance looks the same as one added by hand, and is listed. What it cannot do is
  tell you that tink once applied it, or remove it: that needs tink to remember what it applied, which is a separate design.
- An instance with no stack file at all.
- Only instances for now. Storage volumes, profiles and projects are not compared in this direction.

## There is no switch to turn it off

It is one line per instance that has something to say, and silent for one that does not. If it is noisy on your stacks, that is worth knowing: please say
so, with the line you would not want. On the evaluation host (17 live instances declared across 33 stack files) it was silent for 13, and every line on the
other 4 was a real difference: a `boot.autorestart` set by hand after the YAML was written, volume devices a stale snapshot never had, and a proxy device
on a test VM. Before tink's own `user.tink.exec.*` stamps and Incus's `HOME` for a non-root user were excluded it was noisier, which is why they are.
