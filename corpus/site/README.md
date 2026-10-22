# The herd test's site

The shared site the herd test (`../herd.md`) applies stacks to, and the six stacks of the first herd. A fixture, not the platform recipe (#131): the addresses and the zone are documentation values, certificates come from Caddy's own local CA, and the site's one user is a throwaway.

```
TINK=./tink corpus/site/herd.sh up       # the site, then the six stacks
corpus/site/herd.sh test                 # the checks below
TINK=./tink corpus/site/herd.sh backup   # a copy of the vault volume to another pool, and two restore checks
corpus/site/herd.sh down                 # removes everything it made
```

It runs on a local Incus server and touches the host in one place: on a host running firewalld it puts the platform bridge and every bridge named `tink-*` in the `trusted` zone of the running firewall (not permanent). That is one rule, added before any bridge exists, so tink creates the stacks' bridges from nothing and they get DHCP and DNS from the host (#177). With `SUDO_ASKPASS` set it asks through that.

## What is in it

| File | What |
|---|---|
| `site.yaml` | a project `herd-site`, the platform bridge `herd0`, and three role helpers: **dns** (CoreDNS), **sso** (Authelia), **ingress** (Caddy), with their files |
| `files/` | the Corefile and zone, the Caddyfile and its routes, Authelia's configuration and one user (`tester`, password `herd-test-password`, groups `family` and `admins`) |
| `vault.yaml`, `status.yaml`, `blog.yaml`, `shop.yaml`, `media.yaml`, `metrics.yaml` | the herd: Vaultwarden; Uptime Kuma; WordPress with MySQL twice; Sonarr with qBittorrent; Grafana. Each is what `tink import` writes for the project's own compose file, plus the lines marked `site:` |
| `paste.sh` | stands in for the derive stage: runs `tink join` and writes its renderings into the places they belong |
| `herd.sh` | `up`, `test`, `backup`, `down` |

Every stack has a project and a bridge of its own (`herd-NAME`, `tink-NAME`) with an ACL that admits only the ingress helper. The ingress helper is reached from the host on `127.0.0.1:443`; names are tried with `curl --resolve NAME:443:127.0.0.1`.

## What is still by hand

Each line marked `site:` in a stack is something the derive stage (#177, #130) should make unnecessary.

| By hand | Should become |
|---|---|
| a project of its own | the stack's project |
| a bridge for the stack, and an ACL on it | derived (#177) |
| a static address on the app's NIC, and the NIC pointing at the bridge | allocated and derived (#177) |
| the offer keys (`user.tink.offer.web.*`) | stated by the app's stack, or imported |
| a `kind: publish` | stays: it is the grant |
| the public name in the app's environment (`DOMAIN`, `GF_SERVER_ROOT_URL`, WordPress's `WP_HOME`) | a place in the publish for it |
| removing the compose file's host port | the importer's offer-or-port choice (#179) |
| a backup answer for each volume | stays: it is the site's decision |
| the `caddy`, `sso` and `acl` renderings written into their files | derived files (#130); `paste.sh` does the writing here |
| a volume's owner when the image runs as its own user (Grafana) | #176 |
| the firewall rule, once | a documented prerequisite (#177) |

`tink join apply dns` writes the dns records; nothing else the join renders is applied by tink yet.

## The checks (`herd.sh test`)

Run on 2026-10-21 and 22 (L-2026-10-21b), Incus 7.5.1, Caddy 2.11.7, CoreDNS 1.15.0, Authelia 4.39.28. For each of the seven published names (`vault`, `status`, `blog`, `shop`, `sonarr`, `qbittorrent`, `metrics`):

1. the name answers from the dns helper;
2. with no session, the ingress helper sends the request to sign-on (302);
3. after signing in as `tester`, the app answers (200, or a redirect that is not back to sign-on);
4. the app is not reachable from the host directly.

And for the stacks: another platform instance does not reach an app; `blog` and `shop` each have a running `db`; Sonarr and qBittorrent share `/downloads`. All 32 checks pass, and a second `tink plan` of the site and of every stack says no changes.

**Removing a publish** was tried on `status`: dropping its `kind: publish` and running `paste.sh` removed its route, its sign-on rule, its dns record (`tink join apply dns`), its stamp on the instance and its ACL rule, and left the other six names answering. The ACL rule goes only because the ACL says `ingress: []`: leaving the direction out means "not managed" and the rule stayed (`docs/network-acl.md`). A derived ACL has to declare its direction even when nothing is published through it.

**Backup** (`herd.sh backup`, vault only): the volume declares a snapshot schedule, a copy to the `vm` pool and a weekly restore check. A copy was made, and both the snapshot and the copy were restored to a scratch volume where the check (`test -s /data/db.sqlite3`) passed. `tink plan` still warns that 3-2-1 is not met, which is true: the second pool is on the same host. The other five stacks answer `none: a throwaway fixture`, so the herd does not yet meet "every `keep` volume has a policy".

## What it found

- **A file cannot be placed in a directory that is not there** (#140). Authelia's image has no `/config`, Caddy's no routes directory, so the fixture points Authelia at `/app` and imports one `apps.caddy`. A failed push also left a crash-looping instance that blocked the next apply until it was stopped by hand.
- **A one-time rule for `tink-+` is enough.** With it in place before any bridge exists, tink created six bridges from nothing, all with DHCP and DNS (#177).
- **An env value cannot hold a line break** (Incus: `Environment variables cannot contain line breaks`). WordPress's `WORDPRESS_CONFIG_EXTRA` had to be one line. `tink import` passes a multi-line value through and the apply fails; it should refuse it (#179). The failed apply left the instance half made, and the retry resumed it.
- **Two stacks with a `db` run side by side** when each has its bridge: `blog` and `shop` are the same compose file twice. On one bridge the second would be refused (#143, #177).
- **A first run of Grafana exits at once** on a root-owned volume (#176); the stack gives the volume an owner.
- **qBittorrent refuses a Host header whose port is not its own** (`Invalid Host header, port mismatch`). It only showed because the fixture first listened on 18443; on 443 the header has no port. A proxy that serves a non-standard port meets it.
- **A publish is a whole name.** Uptime Kuma's public status page cannot be opened without sign-on while its dashboard stays behind it (`platform.md`, `public-paths`). Not tried; the stack publishes the admin UI only.
- **Apps on one bridge are not isolated from each other by the bridge ACL**: Sonarr reached qBittorrent directly. That is what a stack of apps sharing data wants, and it is why a bridge per stack and not per app is the unit.
- **The join over seven publishes in six projects, two of them with an instance `wordpress` and an instance `db` each, rendered correctly.** Its ACL rendering matched the rules written by hand.
