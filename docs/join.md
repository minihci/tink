# `tink join`: what a published name needs from the site's helpers

**Status: built. `tink join` prints; `tink join apply dns` is the first renderer that writes; `plan apply` stamps each publish onto its instance and `tink join --from-server` reads them back.** All are unit-tested and the file-based join has been run on the author's own platform and canary stacks, where all five renderings below matched the pieces that were written by hand, including the control plane's live file **[live]**. The stamps and `--from-server` were run against a real Incus 7.5.1 server on 2026-10-17 **[live]**: `plan apply` wrote `user.tink.publish.<name>` as the compact, sorted JSON described below, and a second plan said no changes; changing a publish's groups updated the stamp, and dropping a publish removed only its stamp (`user.foo`, `user.tink.publisher` and `user.tink.role` on the same instance were left alone); a hand-written `user.tink.publish.*` key and a publish whose instance is not in the stack were refused; `tink join --from-server` printed exactly what the file-based join printed, and all five renderings matched the golden files (with one address substituted for the host bridge the lab does not have); a stamp with an extra field was an error naming its instance, exit 1, with nothing rendered; `join apply dns --dry-run --from-server` refused, writing nothing, on a helper without the `/db.<zone>` layout. **Not run:** `join apply dns` writing to a real CoreDNS helper. Only the dns records can be applied so far (see [Applying: dns](#applying-dns)). The design is in the design note, now in #130.

## The idea

Publishing an app behind sign-on touches five places that have to agree: the dns records, the mesh control plane's records, the proxy's routes, the sign-on helper's access rules, and the ACL on the app's bridge. Written by hand, one name is five edits in five files, and nothing notices when one is forgotten. With `join` the app says what it **provides**, the site says who may **use** it under what **name**, and the join derives the five.

```
an app states an offer ──┐
                         ├── join ──> dns records, mesh records, proxy routes, sign-on rules, ACL rules
the site publishes it ───┘                     (printed; compared with the hand-written ones)
```

## The three pieces

**An offer** is a fact the app states about itself, as keys on its instance. It says nothing about who may use it.

```yaml
kind: instance
name: whoami
config:
  user.tink.offer.web.port: "80"
  user.tink.offer.web.proto: http
  # user.tink.offer.web.listen: eth0     # only needed when the instance has more than one NIC
devices:
  eth0: {name: eth0, type: nic, network: canary0, ipv4.address: 198.51.100.10}   # the address must be static (see below)
```

**A publish** gives one offer a name in the site's zone and says which groups may use it. The same offer can be published twice, to different groups.

```yaml
kind: publish
name: whoami            # the host label: whoami.home.example.org is this plus the dns helper's zone
instance: whoami        # the instance that makes the offer
offer: web              # which of its offers
allow: [family]         # groups, never people; a typed subject such as group:family is refused
```

A publish is a **declaration**. `tink plan` leaves it out of the dependency graph exactly as it leaves out `kind: stack`: it has nothing to create, nothing waits for it, and it may share its name with the instance it publishes (the host label and the instance are very often the same word, and the resolver's names are one flat namespace per stack). Two publishes with one name are an error, because a published name is unique in the zone.

### Stamps: how the server learns of a publish

`tink plan` and `tink plan apply` also **stamp** every publish onto the instance it names, as one config key per publish, so that whatever acts for the site later (the daemon helper) can read the declarations from the server and does not need the stack files:

```
user.tink.publish.whoami:        {"allow":["family"],"offer":"web"}
user.tink.publish.whoami-admin:  {"allow":["admins"],"offer":"web"}
```

- The key suffix is the publish's host label. The value is compact JSON with its keys sorted and the groups in the order the publish declares them, so the same stack always stamps the same bytes and a re-plan reads `no changes`. It carries **no secret**: a publish holds group names only. It names no instance (the key is on it).
- The stamps are **derived into the instance's desired config**, so the ordinary instance plan and apply converge them: a new publish is a planned `config.user.tink.publish.<name>: "" -> "{...}"` on the instance, a changed one a normal change. The publish itself stays out of the plan graph exactly as before.
- **A publish must be declared in the same stack and project as its instance.** A grant sits next to the thing it governs, and the server holds it there. If the instance is not in the stack (or is in another project) `plan` and `plan apply` stop with an error that says so. `tink join FILE...` is not held to this: it still reads a publish in one file and its instance in another, as it always did, but only a publish beside its instance reaches the server, and so only that one is seen by `--from-server`.
- **Declaring a key under `user.tink.publish.` by hand is refused** at load time: the stamp is derived, and a hand-written one would be overwritten or removed.

#### The owned prefix, and the one place tink removes a key

Everywhere else tink never removes a config key it is no longer told about: a key dropped from the YAML stays on the instance and `plan` reports it as "not in the stack" (see [plan-extras.md](plan-extras.md)). A stamp is different, because one that stays after its publish is deleted keeps the name **rendered**: still routed, still let in. So `user.tink.publish.` is **owned by tink**, and for it alone (the stamps of derived files, `user.tink.derive.`, [below](#the-stamp-of-a-derived-file), are owned in the same way):

- when `plan` or `plan apply` handles an instance **that the stack declares**, every live key under `user.tink.publish.` that the stack no longer derives is a planned change, `config.user.tink.publish.<name>: "{...}" -> (unset)`, and `plan apply` removes it in the same update that sets the others (also on an instance rebuilt onto a new image);
- **no other key is touched**: not another `user.tink.*` key (`user.tink.role`, `user.tink.helper.status`, `user.tink.publisher`), not a foreign `user.*` key, not an Incus key. The rule is a prefix match with the trailing dot, so `user.tink.publish` and `user.tink.publisher` are not under it;
- it applies **only to instances in the stack being planned**. An instance the stack does not declare is never read for stamps, whatever it carries;
- a stack that has dropped *all* its publishes therefore removes all the stamps on its instances, which is what "stop rendering them" means;
- stamps are never listed as "not in the stack" (they are either declared by derivation or a planned removal), and `tink export` writes them back as `kind: publish` documents, so a stack exported from a server applies without taking the grants away.

**The helpers' own facts**, which the join needs to fill in the rest. Helpers are found by role, never by name:

| Key | On | Says |
|---|---|---|
| `user.tink.role: dns` / `ingress` / `sso` / `mesh` | each helper | which job it does. `ingress` must be exactly one and `sso` at least one; `dns` and `mesh` are optional, at most one each (without a `dns` helper there are no dns records to render; without `mesh` there is no mesh view) |
| `user.tink.dns.zone: home.example.org` | the dns helper, or the ingress helper when there is no dns helper | the zone every published name ends with. With both stated they must agree |
| `user.tink.leg.<nic>: serves` | the ingress helper | LAN clients reach it on this NIC: the address the dns records answer with. Needed when there is a dns helper |
| `user.tink.leg.<nic>: mesh` | the ingress helper | mesh clients reach it on this NIC: the address the mesh view answers with, and a source address the apps' ACLs admit (these coincide by design: the gateway advertises exactly that address **[to confirm]** if a site ever needs them apart). Needed when there is a mesh helper |
| `user.tink.leg.<nic>: public` | the ingress helper | the internet reaches it on this NIC, through the host's address: proxy devices on the host (`nat` on, so the helper sees the client's address) connect to it. A source address the apps' ACLs admit. Nothing is rendered for it: the names live in public DNS, outside tink |
| `user.tink.leg.<nic>: discovery` | any | why a NIC exists when it is not a way in for users (not read by the join) |

A NIC may have several purposes, separated by commas (`mesh, public`), and an address that is both is admitted once. At most one NIC has each of `serves`, `mesh` and `public`. A site needs at least one source address for its ACLs, so it has a `mesh` leg or a `public` leg; a LAN-only site with neither has no way for the ingress helper to be admitted, and that is an error rather than an ACL that admits nobody.

**A site with no dns helper** (a VPS whose names are in public DNS) states the zone on the ingress helper and gets `caddy`, `sso` and `acl` only. The join prints one warning naming the names that belong in public DNS and the address that carries them. It was written for the shape in `internal/join/testdata/public/`, whose hand-written pieces are `internal/join/testdata/public/golden/`. The `public` leg also checks, as warnings, that a proxy device on the host connects to its address and has `nat` on; see [public-exposure.md](public-exposure.md).

## Running it

```
tink join platform/platform.yaml canary/canary.yaml
tink join --only dns platform/platform.yaml canary/canary.yaml      # one section, no heading: easy to diff
```

Each argument is one stack (that is what gives its resources the stack's default project, and fills in its [`defaults:`](stack-defaults.md)); a stack split across files is given as `a.yaml,b.yaml`. Output, for two publishes of one app:

```
== dns: records for the dns helper's hosts file (zone home.example.org)
192.0.2.231 whoami.home.example.org
192.0.2.231 whoami-admin.home.example.org

== mesh: ...                                  a JSON array of A records, the format of Headscale's extra_records file

== caddy: routes for the proxy, to sit inside the wildcard site block ...
@whoami host whoami.home.example.org
handle @whoami {
	import sso
	reverse_proxy 198.51.100.10:80 {
		import no_portal_cookie
	}
}
...
== sso: rules for the sign-on helper, to sit under access_control.rules
- domain: 'whoami.home.example.org'
  policy: one_factor
  subject: 'group:family'
...
== acl: rules for the ACLs on the apps' bridges, under ingress:
# network-acl canary0-in (network canary0)
ingress:
  - {action: allow, source: 192.0.2.3/32, protocol: tcp, destination_port: "80", state: enabled, description: "ingress helper publishes whoami, whoami-admin"}
```

### From the server

A stack that says `network: own` ([network.md](network.md#a-bridge-of-the-stacks-own)) has its bridge, NICs and addresses derived by `tink plan`, so they are not in its files: `tink join FILE...` cannot see them, and `tink join --from-server` after `plan apply` can.

```
tink join --from-server                       # the same five renderings, from what the server holds
tink join --from-server --only dns
tink --remote lab join --from-server          # --remote and --socket work as for the other commands
```

With `--from-server` the files are not read at all (and giving both is an error: two sources would leave it unclear where a rendering came from). The join is built from the live server, **read-only** (only list calls are made; nothing is written):

| What | Read from |
|---|---|
| instances | every project: name, project, and the **stored** config, devices and profiles (not Incus's expanded view, which already has the profiles merged in) |
| profiles | every project, because an offer's NIC may come from a profile |
| networks | the managed ones (server-global); the ACL is found from `security.acls` |
| publishes | one per `user.tink.publish.*` stamp, in the project of the instance that carries it |

The same `Build` and the same renderers then run, so for stacks that have been applied the output is the same as the file-based join's (a test builds a server from the fixture stacks, applies the stamps the way `plan` does, and compares each section with the hand-written golden files). The finding texts are the file-based ones: "which no file given declares" then means "which the server does not hold".

**A network whose config the caller may not read.** Incus shows a managed network's config only to a client that may edit the network. For one that may not (the daemon, once its certificate is narrowed: [helper.md](helper.md#narrowing-the-daemons-certificate)) the join reads which ACL is attached from the ACLs' used-by lists instead, which is the one thing it reads from the config.

**A stamp that cannot be read is an error that names its instance, and nothing is rendered.** The value must be one JSON object with exactly the fields `allow` and `offer`, each once, nothing after it, and the publish it describes must pass the same rules a `kind: publish` document is held to at load time (a DNS label for the name, a plain group name, no quote or control character, and so on). Anything else is reported as

```
error: instance/whoami: (project canary) user.tink.publish.whoami: "instance" is not a field of a publish stamp (known: allow, offer)
```

and the exit status is 1. A stamp is never skipped and never repaired: a name half-read would be half-rendered. Two instances stamping one name are the join's own duplicate error.

What the daemon will read is exactly this: `tink join apply dns --from-server` makes the dns helper's `/hosts` equal to the `dns` rendering of the server's stamps, with the same refusals as below. Zero stamps is refused like zero publishes in files (the `--allow-empty` flag says it is meant), because an empty server also looks like a server that `plan apply` has not stamped yet.

What each section is shaped for, and what it assumes:

- **dns**: a hosts-file line per name. Fits CoreDNS's `hosts` plugin; every name answers with the ingress helper's `serves` address. Left out when no helper has role `dns`.
- **mesh**: Headscale's `extra_records` entries, answering with the ingress helper's `mesh` address. Left out, with a warning, when no helper has role `mesh`.
- **caddy**: `handle` fragments for the **wildcard** site, not a site block per name: a site block of its own would ask for its own certificate instead of using the wildcard one **[docs, from memory]**. The upstream is the app's **address**, because names do not resolve across bridges. They import two snippets the platform's Caddyfile defines by hand: `sso` (the forward-auth call to the sign-on helper) and `no_portal_cookie` (which strips the portal's zone-wide session cookie before a request reaches the app, so an app cannot replay a user's portal session against another host).
- **sso**: one Authelia rule per name, `one_factor` (a publish does not choose; a field for it arrives when something needs two factors). Several groups render as an OR of single-group lists, `[['group:a'], ['group:b']]`, which Authelia's own policy checker confirmed means "either" **[live]**.
- **acl** (`derive_ingress: true` on a `kind: network-acl` makes `tink plan` write these rules itself, from the same code: [network-acl.md](network-acl.md#rules-derived-from-the-publishes)): one rule per port per ACL: the ingress helper (from its `mesh` and `public` legs) may reach it; two sources are written as one comma-separated list. Several publishes of one port collapse into one rule whose description names them. The ACL is found from the network the app's NIC is on (`security.acls` on the `kind: network`).

## Sites

**Status: built (#189); unit-tested against fake servers and the fixture stacks, and run once on a local Incus 7.5.1 server on 2026-10-22.** A stack with `site:` created a helper and a published app, and each carried `user.tink.site` beside `user.tink.stack`; a second plan said no changes; `tink join --from-server` with no `--site` left the site's publish out and warned that a helper of site `sitelab` was left out (before the count of publishes was added to that warning), and with `--site sitelab` it read that site's publish; and the same stack without `site:` planned the removal of that one key on each instance and nothing else. The herd's fixture (`site: herd` on its seven stacks) was then applied to a homelab host that already ran a platform and role-stamped lab helpers (L-2026-10-22b, Incus 7.5.1): 32 checks passed, a second plan said no changes, `tink join --from-server --site herd` read the herd and exited 0, the unnamed site's join named the two dns helpers it found, `join apply dns --from-server --site herd --dry-run` chose the herd's helper, and removing the herd left the host as it was. **Not run:** a server that holds two complete platforms, a platform that tink manages and stamps, `join apply dns --site` writing (only the dry run), and the daemon helper with a site. **Found:** the host's daemon helper read the herd's vault volume and reported a failing copy until the herd was removed (the helper serves the whole server, question 5 of #189). Incus has no object for a site, so none is made: a site is a name, kept where the server already learns things.

A server that holds one platform needs none of this. The join and the derive passes read **one site at a time**, and a site is the group of stacks that share a platform: the stack that declares the helpers, and the app stacks whose publishes those helpers show. Before sites the whole server was one site, so a second dns helper or ingress helper anywhere on it (a second platform, a trial of the next version, a lab someone left behind) stopped every derived file with "exactly one". A stack names its site once, on its `kind: stack` document:

```yaml
kind: stack
name: platform
site: herd              # optional; lower case letters, digits, '.', '_' and '-', as a stack's name
```

`plan` then stamps `user.tink.site: herd` onto **the instances of that stack that state a role** (the helpers) **and the instances a `kind: publish` of the stack names**, as an ordinary config change (a missing one is `config.user.tink.site: "" -> "herd"`, applied live with no restart). A publish is in the site of its instance, so the publish stamp keeps the shape it has.

- **No `site:` is the unnamed site.** An instance with a role or a publish stamp and no site stamp is in it, and a server that never says `site:` behaves exactly as before.
- **The key is tink's.** Declaring `user.tink.site` in an instance's `config:` is refused, and a stamp the stack no longer derives (the stack dropped `site:`, or the instance lost its role) is a planned removal, as a stale publish stamp is. It takes only that key: `user.tink.sitemap` is not the stamp.
- **A site is read whole and alone.** `tink join` and `join apply` take `--site NAME`; the derive passes use the site of the stack being planned. Helpers and publishes of another site are not read, and a publish stamp of another site that cannot be read does not stop this one (one of this site's still does). Given files and no `--site`, the join reads the one site they name, and refuses files that name several (including the unnamed one) rather than guess. With `--from-server` and no `--site` it is the unnamed site.
- **Uniqueness is per site.** Exactly one dns, ingress and sso helper, one zone, and one use of each published name, all within the site. The error lists the helpers it found, as `project/name`, and whenever a view left helpers or publishes of other sites out, a warning counts them by site (`other sites were left out of this view: site "herd" (5 helper(s), 7 publish(es))`). It is said even when the view works, because a stack that forgot its `site:` has its publishes in the unnamed site and a view of the named one then lacks them with no error; the count is the only sign.
- **Apps that state no role, no publish and no site are in every view.** They are only looked at through a publish. Their findings (an offer that nothing publishes) are therefore said in the view of every site, including one they have nothing to do with.
- **What is not scoped.** Networks, ACLs and pools are server-wide, and a stack's bridge is still derived per stack. The daemon helper is one per server, and which site's records it writes is open (#189, question 5).

A host that cannot clean up its labs has two ways out. Name the new site and leave the old platform and the labs in the unnamed site, which is how the herd's fixture was applied to a server that already had a platform (L-2026-10-22b); or put `site:` on the platform's own stack, so that the labs' helpers, which state none, fall outside it. The first changes nothing that exists. The second stamps the platform's helpers, and for a platform that no stack declares that is a decision of its own.

## Derived files

**Status: built for the proxy's routes, the sign-on helper's rules and the dns helper's records (the third and fourth pieces of the derive stage, #130); unit-tested, and run on a local Incus 7.5.1 server on 2026-10-22 (L-2026-10-21b).** Six stacks were brought up from nothing and the site's three files followed them: the routes, rules and records for seven names were written by `tink plan apply` of the site, with no `join apply dns` and no paste step; the 32 checks of the herd test passed, a second plan of every file said no changes, and dropping or adding a publish moved all of them. A hand-written `/hosts` was BLOCKED until `adopt_existing` took it over, and removing every stamp was BLOCKED until `allow_empty` said it was meant.

A `kind: file` with `derive:` is a file around a region that `tink plan` writes, from the same renderings as above:

```yaml
kind: file
name: ingress-routes
instance: ingress
path: /etc/caddy/apps.caddy
derive: caddy                 # or sso
restart: true
content: |
  # the proxy's routes for the published apps
  # BEGIN tink derive
  # END tink derive
```

Before the stack is planned (`internal/derive`, the fifth pass of `prepare`), what lies between the two marker lines is replaced by the rendering, indented as the first marker is, so a region inside a YAML list stays inside it. The file is then an ordinary file whose content is the filled one: planned as `would update` when it differs, pushed, and restarted for (`restart: true`) like any other. The Caddyfile imports the routes file inside its wildcard site, and the sign-on helper's configuration holds its own rules around the region (`docs/join.md` above says what each rendering assumes).

- **The view is the whole site.** A file at the site's door shows every name the site publishes, and those are declared in many stacks. So the plan reads the server (every instance with its stored config, and every publish as the stamps say it) and lays the stack being planned over it, the stack winning, as it does for an ACL. The file follows the stacks that have been *applied*: a stack applied after the site changes the file at the next `plan apply` of the site, and not before. Something has to apply the site again when a stack changes (a person today, the daemon later).
- **It is as strict as the join.** A stamp that cannot be read, or anything the join reports as an error (an app on DHCP, no ingress helper, no sign-on helper), stops the plan of the site and says what: a route missing looks exactly like a route that was never wanted.
- **A file that does not ask is left alone**, and the server is not read.

#### The stamp of a derived file

**Status: built (#273); unit-tested against fakes, and run live on a local Incus 7.5.1 on 2026-10-26 (L-2026-10-26e and L-2026-10-26f): a proxy instance and a derived file were applied, the stamp was on the instance, a second plan said no changes, dropping `derive:` removed the stamp and nothing else, and `tink export` named it in a note; then the platform site of the herd corpus (the dns helper, the sign-on helper and the proxy) was applied and each of its three derived files was stamped on its instance with its own `restart`, a second plan said no changes, and `tink join --from-server` still read the server. Not run live: a derived file whose instance the stack does not declare (fakes only), `derive: mesh` (the stamp is the same code), and anything reading the stamp.** Nothing reads it yet: the daemon will (#274).

A file that says `derive:` is also **stamped onto its instance**, the way a publish is, so that whatever keeps the site converged later can read what to keep from the server and does not need the stack files:

```
user.tink.derive.ingress-routes: {"path":"/etc/caddy/apps.caddy","render":"caddy","restart":true}
```

The key ends in the file document's name. The value is compact JSON with its keys sorted, decoded strictly (exactly those three keys, a path that is absolute, a `render` that is one of the four), and holds no content: what lies between the markers is the rendering and what lies outside is yours. The prefix is owned like the publishes': a stamp the stack no longer derives is a planned removal (dropping `derive:` from the file removes it, and nothing else), and a stack that declares the key itself is refused.

- **It carries no guard.** `adopt_existing` is never the daemon's, and `allow_empty` is not a standing property of a declaration: whoever keeps a file converged on its own refuses to take over a file tink did not write and refuses to empty one that has records. A stack that wants either runs `plan apply`.
- **The instance must be declared in the same stack and project.** A file on an instance the stack does not declare has nothing to carry the stamp. It is not an error (it worked before, and still does under `plan apply`), and `plan` says so: `file/NAME: its instance X is not declared in this stack, so the daemon has nowhere to record that it keeps this file`.
- **`tink export` does not write the file.** Its content is yours and may hold secrets, and a `kind: file` written from the stamp would replace the whole file with a stub when applied. Export names the stamp in a note instead, and a stack written without the file would remove the stamp.
- A network ACL that derives its rules is not stamped yet (#273, second part).
- **The dns records are `derive: dns`**, with the rules `tink join apply dns` has, now the plan's. The file must be on the dns helper. A published name that is already a record in the zone file (`/db.<zone>`, from the stack's own `kind: file` for it, else the server's) is an error, because the generated records answer first and would take the name over. A zone file found nowhere is an error too, not a guess.
- **Two refusals protect every derived file**, shown by `plan` as BLOCKED with the reason:
  - A file that exists, has lines in it and carries no `# BEGIN tink derive` line was not written by tink, and applying would lose them. `adopt_existing: true` on the file takes it over, once; remove the line after it is applied.
  - A rendering with nothing in it does not empty a region that has lines in it: nothing published usually means the wrong server, or stacks that have not been applied yet, and an empty file takes every name down. `allow_empty: true` says it is meant.
- **A derived file is read back after it is written**, and the apply fails if the file does not hold what was written.
- **The mesh view is `derive: mesh`, and it is the whole file.** Headscale's `extra_records` file is a JSON array, and JSON has no comment to mark a region with, so the file takes no `content` and is exactly the join's rendering: one A record per published name, answering with the ingress helper's mesh-leg address (`[]` when nothing is published). A site needs a helper with `user.tink.role: mesh` for this, and says so if it has none. With no marker to say that tink wrote the file, the guards look at what is in it: a file that is not a JSON array of records, or that holds a record outside the site's zone, is BLOCKED until `adopt_existing: true` takes it over, once, and one that has records is not emptied until `allow_empty` says it is meant. A record inside the zone that the rendering lacks is a name that was unpublished, which is what the file is for.
- **It reaches a file on the server the stack is applied to.** A control plane that runs on another server (a Headscale on a VPS, say) is not reachable by a `kind: file` of this stack: one apply is one server (`--remote`). The view that `derive: mesh` would write is in `tink plan explain` of a stack that declares such a file on a local instance; carrying it to the other server is not built.

## What is left before `tink join` can go

`tink join` is a verb for a computation that belongs in `plan` (the roadmap, Phase 3). It goes when nothing it prints is only printed:

1. ~~The mesh records as a planned change~~, built (`derive: mesh`, above), for a control plane whose records file the stack can reach. All five renderings are planned changes now.
2. ~~`tink plan explain` (#129)~~, built: [plan-explain.md](plan-explain.md) prints the routes, rules and records of a site, line by line, without writing them. That is what `tink join --only ...` was for.
3. **The daemon applies planned changes** instead of running `join apply dns --from-server`, under a credential narrow enough for it (Phase 5). The first part is built for the dns records: `tink daemon run --reconcile-dns` runs that apply whenever the server changes ([helper.md](helper.md#the-reconciler)), under the daemon's existing certificate. The other four renderings need a stamp for each derived file first (built for the files, [above](#the-stamp-of-a-derived-file); the ACL is next, #273); the narrow credential is #252.
4. **A removal page** in the style of `docs/removals/ingress-self-registration.md`, because the verb is deployed on the author's hosts.

Until then `tink join` and `tink join apply dns` stay, and `--from-server` is the way to see what a stack that derives its own network produced. `tink join apply dns` and a `derive: dns` file do the same job; use one of them for a dns helper, not both.

## Applying: dns

```
tink join apply dns platform/platform.yaml canary/canary.yaml
tink join apply dns --dry-run platform/platform.yaml canary/canary.yaml      # show the change, write nothing
tink join apply dns --from-server                                            # from the stamps on the server, with no files
```

It makes the dns helper's record file equal to the `dns` rendering and nothing else, through the Incus API with the certificate of whoever runs it (`--remote` works); the same code is meant to be run by the daemon helper on a timer.

**The layout it needs on the dns helper** (measured against CoreDNS 1.14.7 **[lab]**):

- **`/hosts` is the generated file and holds nothing else.** CoreDNS's `hosts` plugin answers from it first and re-reads it every few seconds, so a change needs no restart: a new name answered in about 5 s and a removed one stopped in about 6 s. The file is rewritten whole, so a name that is no longer published disappears.
- **The platform's own static names go in the zone file, `/db.<zone>`** (the sign-on portal, the directory's UI, the helper itself). `hosts` falls through to it for a name it does not have. An empty `/hosts` therefore takes down no platform name.
- **The two cannot be two hosts files.** CoreDNS refuses to start with a second `hosts` plugin in a server block (`this plugin can only be used once per Server Block`): found when a lab instance crashed on it.
- **Where a name is in both, `/hosts` wins.** An app published as `auth` would silently take the portal's name.

**What it refuses, writing nothing:**

| Refusal | Why |
|---|---|
| the join has any error | a partial rendering looks like a complete one |
| no published name is found | applying would replace every record with none, which usually means the wrong files were given (or, with `--from-server`, that nothing is stamped yet); `--allow-empty` says it is meant |
| a published name is already a record in the zone file | the generated record would answer first and take the name over |
| `/hosts` exists and does not begin with the note the join writes | it may hold hand-written records; move the platform's own to the zone file, then give `--adopt` once |
| the zone file is missing, or any read fails | a failed read is an error, never an empty file |

After a write it reads the file back and fails if it does not hold what was written. A second run reports `no changes`. The author's platform was moved over in three steps: the static names into the zone file (one planned restart of the dns helper, about 7 s), a dry run, then `--adopt` once.

**What it needs from Incus** (so the credential it runs under can be judged): it reads two files and writes one, all in the dns helper instance and its project: `/db.<zone>`, `/hosts` and `/hosts` again to check. No instance, profile, network, volume or trust object is read or changed **[code]**. The entitlement is the instance file access one (`can_access_files` on that instance **[docs, from memory; to be measured]**).

With `--from-server` it also **reads** the instances of every project, the profiles of every project and the networks (list calls only), to build the join. That is a wider read than the file-based apply needs, and it is what the daemon helper's certificate will have to be able to do **[code; the entitlements are not yet measured]**. Writing is unchanged: the same two files in the dns helper.

## Findings

Findings go to standard error. **Any error means nothing is printed and the exit status is 1**: a partial rendering (a mesh view missing one name) looks exactly like a complete one. Warnings are printed beside a complete rendering.

| Finding | Severity |
|---|---|
| a publish names an instance the files do not declare, or an offer the instance does not make (the message lists the ones it does) | error |
| the offer is not `http` (a route and a sign-on rule are for web traffic) | error |
| an offer key tink does not know (`user.tink.offer.web.prot`), a missing `port` or `proto`, a port out of range | error |
| the offer's NIC takes DHCP: the proxy and the ACL name an address, so it must be fixed | error |
| the offer's NIC is on an unmanaged bridge: no ACL can restrict who reaches it | error |
| the instance has several NICs and the offer does not say which (`...listen`) | error |
| the app's network has no ACL attached (`security.acls`), or several | error |
| no ingress helper, or more than one ingress or dns helper in the site (the message lists them); no zone (on the dns helper, or on the ingress helper when there is no dns helper), or two that differ; no `serves` leg when there is a dns helper, no `mesh` leg when there is a mesh helper, or neither a `mesh` nor a `public` leg; two NICs with one purpose; a leg word that is not `serves`, `mesh`, `public` or `discovery`; a read leg on a NIC without a static address; no sign-on helper | error |
| the same name published twice; an instance declared twice | error |
| `--from-server`: a publish stamp of the site that is not valid JSON, has a field it should not or lacks one, or breaks a rule of `kind: publish` (the message names the instance, its project and the key) | error |
| `plan`: a publish whose instance is not in the same stack and project | error (from `plan`, not from `join`) |
| an app's `http` offer that no publish shows (helpers are not asked) | warning |
| the app's network is not declared in the files given, so its ACL cannot be shown | warning |
| no helper has role `mesh`, though a `mesh` leg is stamped | warning |
| no helper has role `dns`: the names that belong in public DNS | warning |
| the `public` leg has no host proxy device connecting to its address, or some of those lack `nat` | warning |
| no publish at all in the files given | warning |

## What it does not do

- **`join` itself writes nothing**: with files it is a pure function of them and reads no server; with `--from-server` it reads the server and still writes nothing. Only the dns rendering can be applied. The others need a decision on who holds the credential for reconcile jobs (#130) when the daemon helper runs them, and the ACL rules need a way for the `network-acl` kind to mark a rule list as derived: that kind treats a direction it is given as the whole list, so a declared `ingress:` would erase a derived rule on the next plan. The mesh records are written on the control plane's host, which is its own decision. The daemon loop itself, which would run `join apply dns --from-server` on a timer, is not built.
- **It does not check the listener.** Whether an app listens only where it should (the bypass on a discovery leg) is the next check; an image with no shell, like `whoami`'s, will have to answer *unverifiable*, never *ok*.
- **Only web.** An `access` document (one instance using another's offer, an MQTT broker, say), paths that bypass sign-on (Home Assistant's companion app), names that are LAN-only, and a per-name authentication strength are not modelled. The platform's own routes (the sign-on portal, the directory's UI) are not published apps and stay hand-written.
