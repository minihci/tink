# Running tink against another machine

By default tink manages the Incus daemon on the machine it runs on, over its unix socket. With `--remote` (or `$TINK_REMOTE`) it
manages a **remote** instead: any server defined in the Incus client configuration, reached over the Incus network API. That is what
lets `tink` run from a laptop against a server.

```
tink --remote tron plan  stack.yaml
tink --remote tron apply stack.yaml
TINK_REMOTE=tron tink backup verify immich-library
```

`--remote` is a global flag and wins over `$TINK_REMOTE`. **There is no ambient default**: without either, tink uses the local
daemon, whatever the Incus client's own default remote is, so a forgotten `incus remote switch` cannot make tink apply a stack to
the wrong server. `local` means the local daemon. `--socket` and `--remote` contradict each other and are an error.

## Setting up a remote

Tink stores no credentials of its own. A remote is a name in the **Incus client configuration** of the user running tink (the Incus
client's own configuration directory; `$INCUS_CONF` overrides it; under `sudo` it is root's), and the server must trust that client's
certificate. Add one with the Incus client:

```
incus remote add tron https://tron:8443 --token <token from `incus config trust add` on the server>
```

(A way to do this with tink alone, with no Incus client installed, is not built yet.)

## Image remotes need no setup

Names like `docker-oci:library/alpine:3` are not known to an Incus server: they are names in the *client* configuration, resolved
client-side. A machine that has never run `incus remote add` has none, so tink supplies built-in definitions for the registries a
stack commonly uses, only where the client configuration lacks them:

| Name | Is | Protocol |
|---|---|---|
| `docker-oci` | https://docker.io | oci |
| `ghcr` | https://ghcr.io | oci |
| `images` | https://images.linuxcontainers.org | simplestreams |

A remote the client configuration defines under the same name **always wins** (a mirror, say). The server pulls the image itself, so no
`skopeo` is needed on the client, and the instance is created for the **server's** architecture.

## What does not work under a remote, and says so

| Command or resource | Why | What happens |
|---|---|---|
| `tink deploy` | It provisions the machine it runs on (instances, registries, the daemon's own configuration). | Refuses, naming the remote. |
| `tink ingress reconcile`, `ingress status`, `daemon run` | They read and write a path inside a storage pool on the host. | Refuse. |
| `kind: incus` resources | They are argv for the **local** `incus` CLI, often with local paths, against that CLI's own default remote, which is not the remote tink was told to manage. | The resource is **BLOCKED** with the reason; the rest of the stack plans and applies. |

## Data stays on the server where it can

`backup restore`, and `backup run` to a **pool** target, copy inside one server, which Incus does server-side. A copy to **another server**
(a `remote:` backup target) is relayed through the machine running tink. Run from a laptop that means the volume's data passes through the
laptop, and `backup run` prints a note saying so. The planned way around this is a long-running "helper" instance that does that work next to the data (designed, not built).

## Not covered yet

- Setting up a remote with tink itself (no Incus client installed).
- Latency: every API call is a network round trip; large stacks will feel it.
