# Glossary: tink's words, and where they come from

**Status: a draft for discussion.** It records the words tink uses today and the rules for choosing new ones. Rows marked *proposed* are not built.

## The principle

tink fills gaps; it does not replace what it builds on. It gives a home or self-hosted site the infrastructure pieces that are usually assembled by hand (a reviewable plan, backups that are verified, ingress, secrets, and later remote access) at close to no cost. Incus stays the engine and its documentation stays the reference. A `docker run` line or a Compose file is a reasonable place to start ([docker-to-tink.md](docker-to-tink.md)), and each opinion tink adds is something you turn on: a field you do not declare changes nothing.

## Choosing words

1. **An Incus object or key keeps Incus's exact word and spelling.** The `config:` and `devices:` blocks from `incus ... edit` paste in unchanged, and Incus's reference documents tink's `config:` and `devices:` too.
2. **A concept Incus lacks gets tink's own word, checked first** against Incus's vocabulary and against what Compose and Kubernetes users already mean by it (see [collisions](#collisions)).
3. **Everything tink writes on an Incus object goes under `user.tink.*`**, so it cannot collide with Incus's own keys, and under `user.tink.intent.` or `user.tink.outcome.` ([key classes](#key-classes)).
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
| stack | the documents applied together; a `kind: stack` document names it and may give a default project and `defaults:` ([docs](stack-defaults.md)). Incus has no word for it; Compose calls the same unit a *project* | yes |
| resource, kind | one document in a stack, and its type | yes |
| plan, apply | `tink plan` shows what would change; `tink plan apply` does it | yes |
| BLOCKED | a plan result for something tink refuses to touch, with the reason | yes |
| disruption guard | a change that could interrupt running instances is blocked unless the resource says `allow_disruption: true`. Attaching or changing an ACL (`security.acls*` on a network) is not counted | yes (networks) |
| extras | things in the project that the stack does not declare; reported, never deleted | yes |
| role | a job tink needs done that more than one technology can do (ingress, identity, single sign-on, mesh, backup) | design word; not a field in the YAML |
| engine | the technology that does a role's job today (Caddy, LLDAP, Authelia, Headscale) | design word |
| helper | a container Incus keeps running for tink that provides one role's capability. Always named by its role: *ingress helper*, *dns helper*. Incus starts it at boot, so tink writes no init scripts. One container per role ([helpers](what-tink-is.md#helpers)) | partly: only the daemon helper exists |
| daemon helper | the helper whose engine is tink itself: it runs `tink daemon run` (backup scheduler, job executor, optional ingress reconcile). Its certificate has the reach of root on the host. `tink helper ...` and [helper.md](helper.md) call it "the helper" | yes |
| site | the group of stacks that share a platform: the stack that declares the helpers and the app stacks whose publishes they show. A stack names its site with `site:` on its `kind: stack`; tink stamps `user.tink.intent.site` on the instances of the stack that state a role or are published, and the join and the derive passes read one site at a time. No `site:` is the unnamed site, where everything is on a server that never says it ([join.md](join.md#sites)) | yes |
| platform stack | a stack that declares a site's helpers, each from its upstream image pinned by digest. One per site (a server holds one site unless its stacks say `site:`). Proposed as a shipped recipe (a load-time expander), with `tink plan explain` as the eject; until then you write and own it | no (proposed) |
| ingress | the reverse proxy in front of apps, and the routes tink renders for it from `user.tink.ingress.*` on the apps (the Kubernetes sense of the word) | yes |
| backup target, copy, restore point | where copies go, a copy made there, a verified copy you can restore from | yes |
| secret | `${secret:NAME}` in an instance's `environment.*`, encrypted with an age identity | yes |
| `kind: exec` | run a command in an instance, guarded by a `check` so it is idempotent | yes |
| `kind: file` | a file placed in an instance | yes |
| `kind: incus` | the escape hatch: arguments for the local `incus` command | yes |
| stamp | a `user.tink.*` key tink writes on an object to remember something (which stack made an object, what it was made for, when a copy was made) | yes |
| owner | the stack named in an object's `user.tink.intent.stack` stamp, written when tink creates the object (a stack with no `kind: stack` name has none). A record of who made it, not a lock ([ownership.md](ownership.md)) | yes |
| resource stamp | `user.tink.intent.resource`, written beside the owner stamp: the object's own address (`instance/PROJECT/NAME`, `storage-volume/PROJECT/POOL/NAME`, `project/NAME`, `network/NAME`, ...; an empty project or pool is `default`), which says what the object was made for. "Resource" here is the **Incus object's address**, not a document in a stack. Built by `ownership.Address`; the same string names the object in status, logs and notifications ([ownership.md](ownership.md#the-resource-stamp-and-the-five-states)) | yes |
| the five states | what `ownership.Classify` answers for an object, from its stamps and its real address: *unstamped* (no owner stamp), *elsewhere* (another stack's), *here* (this stack's and its own address), *copy* (this stack's and another object's address: `incus copy` keeps every `user.*` key) and *unbound* (this stack's and no resource stamp yet). A reader acts only on *here* | yes |
| stamp dimension | the part of a plan that says whose an object is and whether its stamps are its own. It is never a change line, an action or an exit status; `plan` prints it as grouped lines ("N objects carry stamps made for another resource (copies?)", "N objects have no resource stamp yet"). An ordinary `plan apply` writes a missing resource stamp on an object this stack owns, restarting nothing | yes |
| adopt | claim a declared object that exists and has no stamp, or is a copy, for this stack and this address, with `tink plan apply --adopt` or `ownership: adopt-on-apply`. It writes the two stamps in one stamp-only update and restarts nothing; an object another stack stamped is left alone. (`kind: helper` also says "adopt", for "accept and leave alone"; this one writes.) | yes |
| `on_image_change` | the policy for when a pinned image's digest moves: update, or report | yes |
| offer | what an instance provides, stated as keys on it: `user.tink.offer.<offer>.port` and `.proto`. A fact about the app; it says nothing about who may use it | yes (read by `tink join`; web offers) |
| `kind: publish` | a declaration: a name in the site's zone (the document's `name:`, plus the dns helper's zone) shows one offer of an instance to named groups. tink converges nothing for it and `plan` leaves it out of the graph, but stamps it onto its instance as `user.tink.intent.publish.<name>` so the server holds it; `tink join` reads it, from files or `--from-server` ([join.md](join.md)) | yes (a declaration) |
| state | what the daemon found about one thing it keeps or watches, one of `converged`, `blocked`, `skipped`, `failing`, with a fixed reason and a `since`: a derived file, the dns records, a volume it will not copy, a copy that is failing. The status document carries them in one list, `states` ([helper.md](helper.md#the-states-list)); `internal/observe` is the one type for them and the one clock for their `since`. A rewrite of a file is not a state | yes |
| event | one thing the daemon did, from a fixed set of words (`healed`: a derived file was rewritten; `restarted`: its instance was restarted), with what it was done to and when (`at`), and no content. The status document keeps the most recent 20 in `events` ([helper.md](helper.md#the-events-list)) | yes |
| `kind: helper` | the daemon helper declared in a stack: adopted when present (plan reports its health and flags a missing remote, changes nothing), installed when absent ([helper.md](helper.md#declaring-it-in-a-stack)) | yes |
| join | the step that looks across the offers, the publishes and the helpers' own facts and derives what each helper needs to match: dns records, mesh records, proxy routes, sign-on rules, ACL rules. `tink join` prints them, and `tink join apply dns` writes the dns records | yes (print-only; dns can be applied) |
| leg | why an instance has a NIC where it does: `user.tink.leg.<nic>` is `serves` (LAN clients reach it here), `mesh` (mesh clients reach it here), `public` (the internet reaches it here, through the host's address) or `discovery` (it is not a way in for users); several may be given, separated by commas | yes (read by `tink join`) |

## Time words

One meaning for each word, so a reader of a stamp, a status document or a job file never has to ask which "time" is meant. These are tink's own keys and fields; Incus's and other tools' words (`created_at`, `expires_at`) are mirrored exactly where tink reads or writes them.

| Word | Meaning | Example |
|---|---|---|
| `<past-participle>-at` | An instant: the most recent time that thing happened. The verb is named once, in the key | `created-at`, `started-at`, `finished-at`, `failed-at`, `copied-at`, `verified-at`, `written-at` |
| `at` (alone) | An instant inside an *event* record, where an `event` field beside it names the verb: `{"event": "healed", "at": "..."}` says what `healed-at` says. It is never a key of its own, and never a stamp's last part | the status document's events (`healed`, `restarted`) |
| `<present-participle>-since` | When the current state began, in a flat stamp where the state has to be named | `failing-since` |
| `since` | When the state a record describes began. It appears only on a state (a record with a `state`), never as the time of a last attempt | a state's record (a reconcile, a volume that is skipped); a failing copy's onset |
| `until` | A future end: a pause, an expiry. Reserved; nothing writes it yet | |
| duration | A Go duration string (`10m0s`, `36h0m0s`), written as Go's `Duration.String` gives it and read with `time.ParseDuration`. Never a bare number of seconds | `heartbeat: "10m0s"` |
| calendar cadence | `daily`, `weekly`, `monthly`, and a policy's `every`. These are keywords and not durations, because a month is not a fixed length of time | `verify: weekly` |

A bare `at`, `time` or `tick` is not a key, and not the last part of one, outside an event record; `cmd/tink/timenaming_test.go` fails if one appears in a time-typed JSON field or a stamp key.

**Every stored or exchanged instant is RFC 3339, in UTC, to whole seconds** (`2026-11-01T09:30:00Z`), produced and read by one package (`internal/instant`), whose JSON type `instant.Time` writes the same way and leaves out a zero value. Human output never prints a raw timestamp in prose: it shows local time with the zone's name (`2026-11-01 10:30 CET`) or a relative age (`7m ago`).

## Key classes

Every key under `user.tink.` is one of three kinds, and the kind is the prefix, so nothing needs a table. `ownership.ClassOf` (`internal/ownership`, which imports nothing of tink's) answers it, and every consumer asks it.

| Class | Prefix | Written by | Examples |
|---|---|---|---|
| fact | none: the short list below | a person, in `config:` | `user.tink.role`, `user.tink.leg.<nic>`, `user.tink.offer.<name>.*`, `user.tink.dns.zone` |
| intent | `user.tink.intent.` | tink, from the declaration, by `plan apply` | `intent.stack` (the owner), `intent.resource` (the address it was made for), `intent.site`, `intent.publish.<name>`, `intent.derive.<file>`, `intent.backup.policy`; named and not yet written: `intent.applied` |
| outcome | `user.tink.outcome.` | whatever ran an operation | `outcome.backup.copy.<target>.copied-at`, `outcome.backup.verified-at`, `outcome.exec.<id>.trigger-hash`, `outcome.helper` (the marker), `outcome.helper.status`, `outcome.run.command` |

What each consumer does with the class:

- **Extras** (`plan`'s "not in the stack" line) are the same for every kind: an undeclared fact is reported, so a role set by hand is noticed; intent and outcomes never are.
- **`tink export`** keeps facts and drops intent and outcomes (a publish and the site are still written back as their document and field).
- **Validation** refuses an intent or outcome key in `config:` or `properties:` of every kind.
- **tink's own volume copies** (a restore point, a restored volume, a verification's scratch volume) drop the outcomes, the copy policy and the applied record, and keep the owner and resource stamps, so a copy reads as a copy.
- **Removal:** tink owns the whole `user.tink.intent.` prefix on an instance and a network ACL: an intent key the stack no longer derives is a planned removal. The owner, resource and applied keys are never derived into a config and never taken back. Nothing outside the prefix is ever removed.

`cmd/tink/keynamespace_test.go` fails if tink's source writes a `user.tink.*` literal that is none of the three, so a new key cannot be added without choosing its class. The old names, and how to fix a host that has them, are on [the removal page](removals/stamp-and-key-names.md).

## JSON keys

A key that tink defines in a JSON document it writes or prints (the status document, the `--json` output of `tink helper status`, a job's request and status, a backup run's summary) is lower case, and **a hyphen joins the words of one key**: `written-at`, `last-job`, `exit-code`. It is never an underscore, so one document does not mix two spellings and a reader never has to remember which key uses which. In `jq` a hyphenated key is read with brackets (`.["last-job"]`).

A key that belongs to **another tool's format** is mirrored exactly, as Incus's words are: `access_token` in a container registry's token response (`internal/imageconfig`) and `display_name` in the Headscale API (`internal/mesh/headscale`) keep their underscores, because tink reads a document it did not write. `cmd/tink/timenaming_test.go` fails if a struct field of tink's own source has a JSON key with an underscore, and lists those two by name, each with the format it comes from; a third has to be added to that list on purpose.

The keys of the stack language (YAML a person writes: `depends_on`, `on_image_change`, ...) are not covered by this rule. They are #280's.

## Proposed: remote access (not built)

These come from a design discussion; there is no design note yet, and the names are open.

| Word | Meaning |
|---|---|
| control plane | the coordination server (Headscale by default): who is in the network, their keys, the policy. It carries no traffic. It is something the gateway *needs*, not part of the gateway: tink's default is a Headscale helper, but the gateway needs only the server's address and a key, so Tailscale's hosted service or someone else's Headscale also works. Its own stack, on a host with a public name |
| gateway | the mesh helper: a container tink creates on a bridge that joins that bridge to the network and advertises routes to the apps that offer a port |
| offer | an app says it can be reached. **Built** for web apps as `user.tink.offer.<offer>.port` and `.proto` on the instance (see the table above); an offer grants nothing |
| grant | the site says who may reach an offer: `allow: [family]`. For a web offer a grant is a `kind: publish` document (built as a declaration); `kind: access`, one instance using another's offer, is not built. This is what is reviewed. It is also Tailscale's and Headscale's word for the same idea (who may reach what), which is deliberate: Headscale recommends grants over the older ACLs |
| relay | an optional component of the control plane that carries encrypted packets when two peers cannot connect directly. Off by default; the public relays are used |
| `<name>` | **the word for the feature is `mesh`**, decided 2026-10-26 ([#249](https://github.com/minihci/tink/issues/249), point 5). It is also the audience word decided for `via`, and the value of `derive: mesh`. See the first collision below |

## Collisions

| Word | The clash | What to do |
|---|---|---|
| ingress | tink: the reverse proxy. Incus network ACLs also have `ingress` and `egress` rules (a direction of traffic, from the NIC's point of view) | `kind: network-acl` uses Incus's meaning only, and says so ([network-acl.md](network-acl.md)); `ingress` on its own means the proxy everywhere else |
| overlay | in Incus and Docker an *overlay network* is OVN or VXLAN networking. tink says it does not manage OVN | decided 2026-10-26: the feature is the *mesh* and tink does not call it an overlay; `mesh` does not clash |
| helper | ordinary English, and other tools use it for other things (Docker's credential helpers, Helm's helper templates) | always say *\<role\> helper*: *ingress helper*, *daemon helper*. The bare "the helper" is ambiguous |
| project | Compose's project is tink's stack; Incus's project is Incus's | use *stack* for the tink unit (already the case) and never *project* for it |
| profile | Compose profiles are optional service groups; Incus profiles are reusable config | tink follows Incus; mention Compose's meaning where Compose users arrive |
| publish | Docker's `--publish` (and `tink run -p`) forwards a host port to a container; tink's `kind: publish` gives an offer a name in the site's zone and says who may open it. Both put something where outsiders can reach it | kept, decided 2026-10-14; the alternative was `route`. Say "publish a name" and "forward a port" |
| volume | Compose's *volume* is tink's `storage-volume` | the kind is spelled `storage-volume`, as in Incus |
