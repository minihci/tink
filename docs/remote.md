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
certificate. `tink remote` manages those entries, with no Incus client installed, writing the same files `incus remote` does:

```
# on the server: a trust token (it carries the server's certificate fingerprint and addresses)
incus config trust add my-laptop > token.txt

# on the client
tink remote add tron --token-file token.txt          # the token supplies the address and verifies the server
tink --remote tron plan stack.yaml
```

- **The server is verified before anything secret is sent to it**, one of three ways: the token's own fingerprint, `--fingerprint FP`, or
  `--accept-certificate` (trust on first use; at a terminal you are shown the fingerprint and asked). The server's certificate is then stored
  and every later connection is **pinned** to it. A certificate that does not match is refused and the token is never presented.
- **The token never goes on the command line** (shell history, `ps`): `--token-file FILE`, `--token-file -` for standard input, or
  `$TINK_REMOTE_TOKEN`. A token works once. A server that already trusts this machine's certificate (an admin ran `incus config trust
  add-certificate` with `client.crt` from the configuration directory) needs no token at all.
- `tink remote add NAME ADDRESS --fingerprint FP` for a given address; `--project` to pick the project the remote defaults to.
- Nothing is saved, and no server certificate is left behind, unless the whole thing succeeds.
- `tink remote list` shows the remotes and the built-in image remotes; `tink remote remove NAME` forgets one and its stored certificate. It
  cannot make the server forget this machine: `incus config trust remove` on the server does that.
- **TLS only.** A server that offers OIDC as well is still added with TLS. (`incus remote add` prefers OIDC, an interactive browser login,
  whenever the server advertises it, unless given `--auth-type tls`.)

## Image remotes need no setup

Names like `docker-oci:library/alpine:3` are not known to an Incus server: they are names in the *client* configuration, resolved
client-side. A machine that has never run `incus remote add` has none, so tink supplies built-in definitions for the registries a
stack commonly uses, only where the client configuration lacks them:

| Name | Is | Protocol |
|---|---|---|
| `docker-oci` | https://docker.io | oci |
| `ghcr` | https://ghcr.io | oci |
| `images` | https://images.linuxcontainers.org | simplestreams |

A remote the client configuration defines under the same name **always wins** (a mirror, say). The server pulls the image itself, and the instance is created for the **server's** architecture.

`plan` and `plan apply` also ask the registry whether an image has moved ([image-updates.md](image-updates.md)). tink does that itself, over HTTPS, from wherever it runs, and asks for the
image of the **server's** architecture, not the machine's own: an arm64 laptop planning against an amd64 server gets the answer the server would, and nothing has to be installed on
the client. It reaches the registry as the login written into the remote's URL says (`https://user:password@host`), else as the container-registry login of whoever runs tink (the one
`docker login` and `skopeo login` write), else anonymously. A registry that cannot be reached, or an image it does not have, is reported against that image, and an instance set to
`on_image_change: rebuild` is blocked on an image it cannot verify; `--offline` skips the lookups on purpose. The same goes for a rebuild's image: the server is told to pull it, so none of
it passes through the client either.

## What does not work under a remote, and says so

| Command or resource | Why | What happens |
|---|---|---|
| `tink deploy` | It provisions the machine it runs on (instances, registries, the daemon's own configuration). | Refuses, naming the remote. |
| `tink ingress reconcile`, `ingress status` | They read and write a path inside a storage pool on the host. | Refuse, unless `--via-api` sends them through the ingress instance's file API, which is the same from anywhere ([helper.md](helper.md#the-ingress-half)). |
| `tink daemon run` | Its backup half is a client of the server like any other command; its ingress half reads and writes the same host path. | Refuses, unless `--ingress-via-api` runs the ingress half through the ingress instance's file API, `--no-ingress` leaves it out, or `--routes-dir` names a directory the process can reach. The helper runs it with one of the first two, as a client of its own host. |
| `kind: incus` resources | They are argv for the **local** `incus` CLI, often with local paths, against that CLI's own default remote, which is not the remote tink was told to manage. | The resource is **BLOCKED** with the reason; the rest of the stack plans and applies. |

## Data stays on the server where it can

`backup restore`, and `backup run` to a **pool** target, copy inside one server, which Incus does server-side. A copy to **another server**
(a `remote:` backup target) is relayed through the machine running tink. Run from a laptop that means the volume's data passes through the
laptop, and `backup run` prints a note saying so. The way around this for scheduled copies is [the helper](helper.md), a long-running instance that does that work next to the data. A `backup run` you
start yourself still runs, and relays, on the machine you start it on: handing it to the helper is not built.

## Not covered yet

- OIDC remotes (use the `incus` client for those).
- Latency: every API call is a network round trip; large stacks will feel it.
