# Glossary: tink's words, and where they come from

**Status: a draft for discussion.** It records the words tink uses today and the rules for choosing new ones. Rows marked *proposed* are not built.

## The principle

tink fills gaps; it does not replace what it builds on. It gives a home or self-hosted site the infrastructure pieces that are usually assembled by hand (a reviewable plan, backups that are verified, ingress, secrets, and later remote access) at close to no cost. Incus stays the engine and its documentation stays the reference. A `docker run` line or a Compose file is a reasonable place to start ([docker-to-tink.md](docker-to-tink.md)), and each opinion tink adds is something you turn on: a field you do not declare changes nothing.

## Choosing words

1. **An Incus object or key keeps Incus's exact word and spelling.** The `config:` and `devices:` blocks from `incus ... edit` paste in unchanged, and Incus's reference documents tink's `config:` and `devices:` too.
2. **A concept Incus lacks gets tink's own word, checked first** against Incus's vocabulary and against what Compose and Kubernetes users already mean by it (see [collisions](#collisions)).
3. **Everything tink writes on an Incus object goes under `user.tink.*`**, so it cannot collide with Incus's own keys.
4. **A field may differ from Incus's shape only where it mirrors the command people type.** `image:` as one string and `vm: true` pass that test; the mapping is documented where it is used.

## Words taken from Incus

| Word | In tink | Incus | Compose |
|---|---|---|---|
| instance | `kind: instance`: a container, an OCI application container or a VM | the same | roughly a *service* |
| project | an Incus project; a stack can name its default once | namespace for instances, profiles, volumes and images | **means the whole application** (a collision, see below) |
| profile | `kind: profile` | reusable config and devices | **means an optional group of services** (a collision) |
| storage-volume | `kind: storage-volume`; `-v name:/path` in `tink run` | a volume in a storage pool | *volume* |
| network | `kind: network`: a managed bridge, server-global, so never in a project | managed network (bridge, OVN, ...) | *network* |
| network-acl | `kind: network-acl`: a named list of firewall rules, with Incus's rule fields, attached by `security.acls` on a network or a NIC. Server-global like a network ([docs](network-acl.md)) | network ACL | none |
| image | `kind: image`, and `image:` on an instance | image or OCI image | *image* |
| remote | `--remote NAME`, and `remote:` on a backup target | a named server in the client config | none |
| trust token | used to enrol the helper | a single-use token that adds a client certificate | none |
| `config`, `devices`, `profiles`, `description` | passed through unchanged | the same | none |
| `depends_on` | an ordering edge, only to a resource in the same stack | none | `depends_on` |

## tink's own words

| Word | Meaning | Built |
|---|---|---|
| stack | the documents applied together; a `kind: stack` document names it and may give a default project. Incus has no word for it; Compose calls the same unit a *project* | yes |
| resource, kind | one document in a stack, and its type | yes |
| plan, apply | `tink plan` shows what would change; `tink plan apply` does it | yes |
| BLOCKED | a plan result for something tink refuses to touch, with the reason | yes |
| disruption guard | a change that could interrupt running instances is blocked unless the resource says `allow_disruption: true`. Attaching or changing an ACL (`security.acls*` on a network) is not counted | yes (networks) |
| extras | things in the project that the stack does not declare; reported, never deleted | yes |
| role | a job tink needs done that more than one technology can do (ingress, identity, single sign-on, mesh, backup) | design word; not a field in the YAML |
| engine | the technology that does a role's job today (Caddy, LLDAP, Authelia, Headscale) | design word |
| helper | a container Incus keeps running for tink that provides one role's capability. Always named by its role: *ingress helper*, *dns helper*. Incus starts it at boot, so tink writes no init scripts. One container per role ([helpers](what-tink-is.md#helpers)) | partly: only the daemon helper exists |
| daemon helper | the helper whose engine is tink itself: it runs `tink daemon run` (backup scheduler, job executor, optional ingress reconcile). Its certificate has the reach of root on the host. `tink helper ...` and [helper.md](helper.md) call it "the helper" | yes |
| platform stack | a stack that declares a site's helpers, each from its upstream image pinned by digest. One per Incus server. Starts as a template you copy and own | no (proposed) |
| ingress | the reverse proxy in front of apps, and the routes tink renders for it from `user.tink.ingress.*` on the apps (the Kubernetes sense of the word) | yes |
| backup target, copy, restore point | where copies go, a copy made there, a verified copy you can restore from | yes |
| secret | `${secret:NAME}` in an instance's `environment.*`, encrypted with an age identity | yes |
| `kind: exec` | run a command in an instance, guarded by a `check` so it is idempotent | yes |
| `kind: file` | a file placed in an instance | yes |
| `kind: incus` | the escape hatch: arguments for the local `incus` command | yes |
| stamp | a `user.tink.*` key tink writes on an object to remember something (which stack owns a volume, when a copy was made) | yes |
| `on_image_change` | the policy for when a pinned image's digest moves: update, or report | yes |

## Proposed: remote access (not built)

These come from a design discussion; there is no design note yet, and the names are open.

| Word | Meaning |
|---|---|
| control plane | the coordination server (Headscale by default): who is in the network, their keys, the policy. It carries no traffic. It is something the gateway *needs*, not part of the gateway: tink's default is a Headscale helper, but the gateway needs only the server's address and a key, so Tailscale's hosted service or someone else's Headscale also works. Its own stack, on a host with a public name |
| gateway | the mesh helper: a container tink creates on a bridge that joins that bridge to the network and advertises routes to the apps that offer a port |
| offer | an app says it can be reached: `user.tink.<name>.expose: "8123"`. It grants nothing |
| grant | the site says who may reach an offer: `allow: group:family`. This is what is reviewed. It is also Tailscale's and Headscale's word for the same idea (who may reach what), which is deliberate: Headscale recommends grants over the older ACLs |
| relay | an optional component of the control plane that carries encrypted packets when two peers cannot connect directly. Off by default; the public relays are used |
| `<name>` | **the word for the feature is undecided**: `overlay` (engine-neutral), `mesh` or `vpn`. See the first collision below |

## Collisions

| Word | The clash | What to do |
|---|---|---|
| ingress | tink: the reverse proxy. Incus network ACLs also have `ingress` and `egress` rules (a direction of traffic, from the NIC's point of view) | `kind: network-acl` uses Incus's meaning only, and says so ([network-acl.md](network-acl.md)); `ingress` on its own means the proxy everywhere else |
| overlay | in Incus and Docker an *overlay network* is OVN or VXLAN networking. tink says it does not manage OVN | decide before the kind is built; `mesh` and `vpn` do not clash |
| helper | ordinary English, and other tools use it for other things (Docker's credential helpers, Helm's helper templates) | always say *\<role\> helper*: *ingress helper*, *daemon helper*. The bare "the helper" is ambiguous |
| project | Compose's project is tink's stack; Incus's project is Incus's | use *stack* for the tink unit (already the case) and never *project* for it |
| profile | Compose profiles are optional service groups; Incus profiles are reusable config | tink follows Incus; mention Compose's meaning where Compose users arrive |
| volume | Compose's *volume* is tink's `storage-volume` | the kind is spelled `storage-volume`, as in Incus |
