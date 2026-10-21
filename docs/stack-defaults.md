# Stack defaults

A stack repeats itself. In one real platform stack of five helpers, `boot.autostart` and `boot.autorestart` were written 26 times, `name: eth0` 24 times, `limits.memory` 18 times, and the same eight-line backup block three times. `defaults:` on the `kind: stack` document says each of those once.

## The design, in one paragraph

`kind: stack` gains an optional `defaults:` with one section per kind that has something worth defaulting: `instance` (a `config` map, and `devices` keyed by device **type**) and `storage-volume` (a `backup` block). A resolver pass, `resolve.ApplyDefaults`, copies each default into every resource of that kind that does not set it, after the files are loaded and before secrets are expanded, the connection is made and the dependency graph is built. A default is a shorthand for writing the value on each resource, and nothing more: a value a resource sets always wins; a default adds no resource; the value it fills in is held to the same rules as if the resource had written it; and a stack with no `defaults:` goes through the pass untouched, so it plans as it always did. The schema is deliberately the smallest that covers what was measured. Anything else a stack repeats (`profiles`, `pool`, a file's mode) is not defaultable yet, and waits for a measurement that asks for it.

## What it looks like

```yaml
kind: stack
name: platform
project: platform
defaults:
  instance:
    config: {boot.autostart: "true", boot.autorestart: "true", limits.memory: 128MiB}
    devices: {nic: {name: eth0}}
  storage-volume:
    backup:
      snapshots: {schedule: "0 3 * * *", retain: 14d}
      copies: [{target: offsite, schedule: "0 4 * * *", retain: 30d}]
      verify: weekly
---
kind: instance
name: dns
image: docker-oci:coredns/coredns:1.14.7
profiles: [tink-base]
config:
  limits.memory: 64MiB          # wins over the default
  user.tink.role: dns
devices:
  eth0: {type: nic, network: tink0, ipv4.address: 192.0.2.12}    # gets name: eth0
---
kind: storage-volume
name: sso-data                  # gets the backup block above
pool: default
---
kind: storage-volume
name: mesh-state
pool: default
backup:
  none: regenerable             # a block of its own: nothing is merged into it
```

The instance is planned with `boot.autostart`, `boot.autorestart`, `limits.memory: 64MiB` and `user.tink.role: dns`, and its NIC with `name: eth0`; `sso-data` is planned with the full backup block; `mesh-state` with `none:`. The two fixtures `internal/resolve/testdata/defaults/helpers-long.yaml` and `helpers-short.yaml` are the same five-helper platform written without and with defaults, and a test proves they resolve to exactly the same resources and the same dependency levels.

## The sections

| Section | Key | What it fills | Granularity |
|---|---|---|---|
| `instance` | `config` | the instance's `config` | one key at a time |
| `instance` | `devices.<type>` | every device of that type (`nic`, `disk`, `proxy`...) | one device key at a time; it never adds a device |
| `storage-volume` | `backup` | the volume's `backup:` block | the whole block, or nothing: a volume that declares any `backup:` (even `none:`) keeps exactly what it wrote |

Every level is strict, like the rest of the stack language: an unknown section, an unknown key, a device type that is not an Incus device type, or a device type with no key is a load error, and so is a `defaults:` on any document but `kind: stack`.

The name of the section is the `kind:`. A kind that is not listed has no defaults.

## The rules

- **What a resource sets wins**, including a set empty value (`limits.memory: ""` is a value). Config and device keys are filled one at a time. A backup block is not merged: half of one policy joined to another would be a policy nobody wrote.
- **Defaults add no resource and no dependency.** They only fill keys of resources the stack already has. A device default may not name another resource or change what a device is, so `network`, `source`, `security.acls*` and `type` are refused: write them on the device. (A `backup` default does name its copy targets, as the same block written on the volume would; a target is a declaration, and an undeclared one is the same error as on a volume that wrote it.)
- **The same checks as a written value.** The backup block is validated as a volume's is. The tink-owned prefix `user.tink.publish.` is refused in an instance default as it is on an instance, and the filled resource is validated once more after it receives a default.
- **Secrets.** `${secret:NAME}` is accepted only in an instance's `environment.*` values, and a default does not widen that: a reference in any other default (another config key, a device key, a backup block) is a load error, and one in an `environment.*` default is expanded like one written on the instance, so the stack then needs the store as if every instance had written it. Defaults are filled **before** secrets are expanded for exactly that reason.
- **The `user.tink.*` namespace** holds as everywhere: nothing here names a key outside it, and a default can carry a `user.tink.*` key only if an instance could.
- **A stack without `defaults:` is byte-identical.** The pass returns the loaded resources as they were. The stack files in the repository's test data are checked to come back untouched.

## Where it runs

In `internal/prepare`, the one place where the order of the passes is written: second, right after the files are loaded. It needs only the loaded documents (no server), so a bad `defaults:` fails before a connection is made. It runs before the secret pass because an `environment.*` default may carry a reference that pass has to expand, and before the graph is built because the graph reads devices and backup copies. `tink join` and `tink volume backup`, which read a stack's files without planning them, call `prepare.Load` (the first two passes), so they see the same resources `plan` does.

The result is the resources themselves: `resolve.ApplyDefaults` returns the filled copy and leaves its input alone, so a test (or the `plan explain` of [#129](https://github.com/minihci/tink/issues/129)) can compare what was written with what will be planned. Nothing records which keys came from a default yet; that comparison is how it is found today.

## What a default is not

- **Not a profile.** Incus profiles can hold the instance-config half of this, but not a NIC's key on a device that is not in a profile, a volume's backup policy, a file's content or an ACL, and `plan`, `join` and `export` read the stored configuration of the instance and not the expanded view a profile gives. A default is filled into the resource tink stores, so it is on the instance and visible to all of them. Profile-carried defaults stay parked.
- **Not a value in the middle.** A key the instance gets from one of its profiles is not seen by the pass: a default for it is written on the instance, which then overrides the profile's value. Say the value in one place.
- **Not a template.** There is no expression, no per-name rule and no inheritance between sections. `user.tink.role` is a different value on every helper, so a stack can default it only if every instance has the same role, which is rarely the point; it stays written on each instance.
- **Not a way to add a device or a resource.** A `nic` default fills a NIC that is already declared.

## Size

For the fixture above, the whole file is 150 lines written out and 123 with defaults. Leaving out the 24 lines that neither version can shorten (the stack, the network, the backup target and the profile), the five helpers and three volumes are 126 lines written out and 99 with defaults, the `defaults:` block (nine lines) included. The file is not halved: each helper still states its image, its role, its address and its state volume, which no default can know. A stack with more helpers or volumes saves more, because the block is paid for once.
