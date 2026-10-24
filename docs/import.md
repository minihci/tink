# `tink import`: a compose file in, a stack out

**Status: built as a spike for the proposal in #179, unit-tested against the survey's compose files, and run live on a local Incus 7.5.1 server (L-2026-10-21b): five imported stacks (Vaultwarden, Uptime Kuma, Audiobookshelf, Actual Budget, WordPress with MySQL) were applied with `tink plan apply`, each answered on its port, and a second plan said no changes. #205 widened the subset to what `tink run` already accepts (`privileged`, `devices`, tmpfs) and added two tests so the two cannot drift; a stack with those keys was applied on a local Incus 7.5.1 server, the tmpfs sizes and the device were there, and a second plan said no changes.** Claims are marked **[code]**, **[lab]** or **[corpus]**.

```
tink import compose.yaml > tink.yaml          # warnings and notes go to stderr, the stack to stdout
tink import --stack blog compose.yaml         # name the stack when the file has no name:
```

It is `tink run` for a compose file. Each service becomes the same options `tink run` takes and goes through the same translation (`internal/run`), so a port or a volume means the same thing in both **[code]**. What it writes is a stack you keep and edit; nothing reads the compose file again, and tink holds no record of the import.

It carries what the compose file says about the app and nothing about the site: no project, network, address, published name or backup policy. A comment after each volume says its backup answer is missing, and `tink plan` warns until you give one.

## What it does with each key

The subset began as the keys the survey's 18 compose files use (`corpus/`; the files are in `internal/compose/testdata`), and grew by the keys the Home Assistant companions' install pages need (#205, below). Anything else is refused, naming the key and the service, and every refusal is printed, not only the first.

| Key | Result |
|---|---|
| `services`, top-level `volumes` | the instances, and the named volumes they use. A volume option other than `name:` is refused |
| `image`, `restart` | `image:`; the boot flags, as `tink run --restart` |
| `volumes` | a named volume becomes a storage volume; a path relative to the file (`./data`) becomes a managed volume named `SERVICE-data` and a note says so; an absolute path stays a bind mount, with `tink run`'s note about ownership; `:ro`, `z`, `Z` and the long form (`type`, `source`, `target`, `read_only`) work. A long-form `type: tmpfs` is a tmpfs (below). The Docker socket is refused |
| `ports` | proxy devices, as `tink run -p`. A container port alone is refused |
| `environment`, `env_file` | `environment.*`. A name that looks like a secret warns, and is written as it is. A name with no value is refused: compose would take it from the shell |
| `depends_on` | `depends_on` (start order only); a condition is dropped with a warning |
| `user` | the process user and the owner of the volumes it makes, as `tink run --user`. Only numbers |
| `entrypoint` | `oci.entrypoint`. Compose drops the image's command when an entrypoint is set, so this is exact |
| `command` alone | refused: it adds arguments to the image's entrypoint, and tink's replaces the entrypoint as well |
| `privileged` | `security.privileged`, as `tink run --privileged`, with its note: the container runs without user-namespace isolation, and the flag does not also hand over host devices |
| `devices` | a `unix-char` device, as `tink run --device`: `HOST:CONTAINER[:rwm]`. A directory, a GPU, the whole USB bus and a block device are refused with the `--incus-device` that does it. The long form is refused. The device is required: the instance does not start without it |
| `tmpfs`, `shm_size` | a `disk` device with `source: tmpfs:` and a size (`/dev/shm` for `shm_size`; an instance already has a 64 MiB one, and the device replaces it). Any tmpfs option other than `size` is noted as ignored: Incus's tmpfs device has none |
| `healthcheck`, `stop_grace_period`, `extra_hosts`, `cap_add` | dropped, with a warning and a comment in the output |
| `container_name`, `hostname`, `networks`, top-level `networks`, `version` | ignored, with a note |
| `network_mode` | refused: host networking has no equivalent. The message says what the Incus answer is (a NIC on the LAN's bridge, which is the site's decision) and where it is described |
| `read_only` | refused: a read-only root disk is possible but an app image tried did not start with one ([docker-to-tink.md](docker-to-tink.md)) |
| `security_opt`, `cap_drop` | refused: Incus has no instance key for an AppArmor profile, no-new-privileges or a dropped capability, and a hardening that silently does not happen is worse than a refusal |
| `name` (top level) | the stack's name |
| `$VAR`, `${VAR}`, `${VAR:-d}`, `${VAR-d}` | from a `.env` beside the compose file only, never the shell, so the result does not depend on who ran it. A default is itself expanded (`${USERDIR:-$HOME}`). An unset variable with no default is refused |

Anything not in this table is refused, naming the key and the service. The table is checked against the code in both directions (`internal/compose/drift_test.go`), and a field of `tink run`'s options that import neither fills nor
explains does not build (`internal/compose/classify.go`): see the next section.

## Why `tink run` and `tink import` cannot drift (#205)

Import builds on `tink run`'s translation, but fills its options key by key, so a flag added to `tink run` would not reach import by itself. That is how `devices`, `privileged` and a tmpfs came to be accepted by one door and refused by the other. Two tests close that **[code]**:

1. Every field of `run.Options` is classified in `internal/compose/classify.go`: the compose keys that fill it, or the reason import does not (a site decision, run-only, or not built yet). A new flag fails `go test` until it is classified, with the message "classify it for import".
2. Every compose key the importer knows has a row in the table above, and every key in the table is known to the importer.

A service called `actual_server` is written `actual-server`: an instance name cannot hold an underscore, and a note says so. A volume that shares a service's name (WordPress's `wordpress`) gets `-data`, because a stack has one namespace for every kind.

## What the survey did with it

Of the 18 compose files, 14 import and 4 are refused **[corpus; `go test ./internal/compose`]**:

- Syncthing: `network_mode: host`.
- Portainer: the Docker socket.
- Jellyfin: `user: uid:gid`, which is a placeholder in the project's own file, not a user.
- Nextcloud: `command:` on its database service, which adds an argument to the image's entrypoint.

The imported stacks of five apps were applied on a local server and answered **[lab: L-2026-10-21b]**. The imported stacks load with the same loader `tink plan` uses, in the test.

## What the second set did (the Home Assistant companions, #204 and #205)

The install pages added by #204 give seven more compose examples, and Home Assistant's own is an eighth. On `main` before #205, `tink import` took one of the seven (Node-RED) and refused six, for `network_mode: host` (4), `devices` (2), `privileged`, a `tmpfs` volume, `read_only`, `security_opt` and `${VAR}` forms (3) (`corpus/status.md`). #205 added `privileged`, `devices`, `tmpfs` and `shm_size`, read `$VAR` and a default that is itself a variable, and gave the refusals that remain a way out. Run over the same pages again **[code: `tink import` on each example, 2026-10-23]**:

| Example | Result |
|---|---|
| Node-RED | imports |
| Zigbee2MQTT | imports (it was refused for `devices`) |
| Frigate | refused: its example lists `/dev/bus/usb`, which is the whole USB bus, with the `--incus-device` for one USB device. The `shm_size` and the tmpfs volume now translate. Deleting the hardware lines the person does not have is the intended edit |
| ESPHome | refused: `network_mode: host` (`privileged` now translates) |
| Music Assistant | refused: `network_mode: host`, and `$HOME` in `${USERDIR:-$HOME}` is not in a `.env` (put it there) |
| Matter.js server | refused: `network_mode: host`, `read_only`, `${HOME}` as above |
| Python Matter server | refused: `network_mode: host`, `security_opt`, `$HOME` as above |
| Home Assistant's own | refused: `network_mode: host` (`privileged` now translates) |

Two of seven import, and every refusal left names a next step. What is left is host networking (needs the site's decision on a LAN NIC: #206), the two hardening keys and `read_only` (Incus has no key, or it did not start), and variables the file expects from the shell.

## What it does not do

`extends`, `include`, profiles, `build`, `deploy`, compose `secrets` and `configs`, a long-form `ports` entry, and writing compose back. A `docker run` line is `tink run`.
