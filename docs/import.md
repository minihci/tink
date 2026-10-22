# `tink import`: a compose file in, a stack out

**Status: built as a spike for the proposal in #179, unit-tested against the survey's compose files, and run live on a local Incus 7.5.1 server (L-2026-10-21b): five imported stacks (Vaultwarden, Uptime Kuma, Audiobookshelf, Actual Budget, WordPress with MySQL) were applied with `tink plan apply`, each answered on its port, and a second plan said no changes.** Claims are marked **[code]**, **[lab]** or **[corpus]**.

```
tink import compose.yaml > tink.yaml          # warnings and notes go to stderr, the stack to stdout
tink import --stack blog compose.yaml         # name the stack when the file has no name:
```

It is `tink run` for a compose file. Each service becomes the same options `tink run` takes and goes through the same translation (`internal/run`), so a port or a volume means the same thing in both **[code]**. What it writes is a stack you keep and edit; nothing reads the compose file again, and tink holds no record of the import.

It carries what the compose file says about the app and nothing about the site: no project, network, address, published name or backup policy. A comment after each volume says its backup answer is missing, and `tink plan` warns until you give one.

## What it does with each key

The subset is the keys the survey's 18 compose files use (`corpus/`; the files are in `internal/compose/testdata`). Anything else is refused, naming the key and the service, and every refusal is printed, not only the first.

| Key | Result |
|---|---|
| `image`, `restart` | `image:`; the boot flags, as `tink run --restart` |
| `volumes` | a named volume becomes a storage volume; a path relative to the file (`./data`) becomes a managed volume named `SERVICE-data` and a note says so; an absolute path stays a bind mount, with `tink run`'s note about ownership; `:ro`, `z`, `Z` and the long form (`type`, `source`, `target`, `read_only`) work. The Docker socket is refused |
| `ports` | proxy devices, as `tink run -p`. A container port alone is refused |
| `environment`, `env_file` | `environment.*`. A name that looks like a secret warns, and is written as it is. A name with no value is refused: compose would take it from the shell |
| `depends_on` | `depends_on` (start order only); a condition is dropped with a warning |
| `user` | the process user and the owner of the volumes it makes, as `tink run --user`. Only numbers |
| `entrypoint` | `oci.entrypoint`. Compose drops the image's command when an entrypoint is set, so this is exact |
| `command` alone | refused: it adds arguments to the image's entrypoint, and tink's replaces the entrypoint as well |
| `healthcheck`, `stop_grace_period`, `shm_size`, `extra_hosts`, `cap_add` | dropped, with a warning and a comment in the output |
| `container_name`, `hostname`, `networks`, top-level `networks`, `version` | ignored, with a note |
| `network_mode` | refused: host networking has no equivalent |
| `name` (top level) | the stack's name |
| `${VAR}`, `${VAR:-d}`, `${VAR-d}` | from a `.env` beside the compose file only, never the shell. An unset variable with no default is refused |

A service called `actual_server` is written `actual-server`: an instance name cannot hold an underscore, and a note says so. A volume that shares a service's name (WordPress's `wordpress`) gets `-data`, because a stack has one namespace for every kind.

## What the survey did with it

Of the 18 compose files, 14 import and 4 are refused **[corpus; `go test ./internal/compose`]**:

- Syncthing: `network_mode: host`.
- Portainer: the Docker socket.
- Jellyfin: `user: uid:gid`, which is a placeholder in the project's own file, not a user.
- Nextcloud: `command:` on its database service, which adds an argument to the image's entrypoint.

The imported stacks of five apps were applied on a local server and answered **[lab: L-2026-10-21b]**. The imported stacks load with the same loader `tink plan` uses, in the test.

## What it does not do

`extends`, `include`, profiles, `build`, `deploy`, compose `secrets` and `configs`, a long-form `ports` entry, and writing compose back. A `docker run` line is `tink run`.
