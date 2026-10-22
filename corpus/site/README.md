# The herd test's site

The shared site the herd test (`../herd.md`) applies stacks to, and the six stacks of the first herd. A fixture, not the platform recipe (#131): the addresses and the zone are documentation values, certificates come from Caddy's own local CA, and the site's one user is a throwaway.

```
TINK=./tink corpus/site/herd.sh up       # the site, the six stacks, then the site again
corpus/site/herd.sh test                 # the checks below
TINK=./tink corpus/site/herd.sh backup   # a copy of the vault volume to another pool, and two restore checks
corpus/site/herd.sh down                 # removes everything it made
```

It runs on a local Incus server and touches the host in one place: on a host running firewalld it puts the platform bridge and every bridge named `tink-*` in the `trusted` zone of the running firewall (not permanent). That is one rule, added before any bridge exists, so tink creates the stacks' bridges from nothing and they get DHCP and DNS from the host (#177). With `SUDO_ASKPASS` set it asks through that. firewalld often makes a change and then does not answer, so the script asks the zone whether it took, tries again, and gives each call a time limit.

## What is in it

| File | What |
|---|---|
| `site.yaml` | a project `herd-site`, the platform bridge `herd0`, and three role helpers: **dns** (CoreDNS), **sso** (Authelia), **ingress** (Caddy), with their files. Three of the files say `derive:` and hold what the stacks publish: the proxy's routes, the sign-on helper's rules and the dns records |
| `files/` | the Corefile and zone, the Caddyfile, Authelia's configuration and one user (`tester`, password `herd-test-password`, groups `family` and `admins`); the three derived files are a note and a pair of `# BEGIN tink derive` / `# END tink derive` lines that tink fills |
| `vault.yaml`, `status.yaml`, `blog.yaml`, `shop.yaml`, `media.yaml`, `metrics.yaml` | the herd: Vaultwarden; Uptime Kuma; WordPress with MySQL twice; Sonarr with qBittorrent; Grafana. Each is what `tink import` writes for the project's own compose file, plus the lines marked `site:`, and says `network: own` |
| `herd.sh` | `up`, `test`, `backup`, `down` |

Every stack has a project and a bridge of its own (`herd-NAME`, `tink-NAME`) with an ACL that admits only the ingress helper. The ingress helper is reached from the host on `127.0.0.1:443`; names are tried with `curl --resolve NAME:443:127.0.0.1`.

## What is still by hand

Each line marked `site:` in a stack. The derive stage (#177, #130) took the rest: the bridge, its ACL and the ACL's rules, the NIC and the static address, and the proxy's routes, the sign-on helper's rules and the dns records (13 steps before, 8 now).

| By hand | Stays? |
|---|---|
| a project of its own | could be derived from the stack's name; nothing does yet |
| the offer keys (`user.tink.offer.web.*`) | the app's stack states them; `tink import` could |
| a `kind: publish` | stays: it is the grant |
| the public name in the app's environment (`DOMAIN`, `GF_SERVER_ROOT_URL`, WordPress's `WP_HOME`) | a place in the publish for it, not built |
| removing the compose file's host port | the importer's offer-or-port choice (#179) |
| a backup answer for each volume | stays: it is the site's decision |
| a volume's owner when the image runs as its own user (Grafana) | #176 |
| `site: herd` on each stack | only on a server that already has a platform: it puts the herd in a site of its own (#189, [join.md](../../docs/join.md#sites)); on a server with nothing else, drop it |
| the firewall rule, once | a documented prerequisite (#177) |

## The checks (`herd.sh test`)

Run on 2026-10-22 (L-2026-10-21b), Incus 7.5.1, Caddy 2.11.7, CoreDNS 1.15.0, Authelia 4.39.28, tink built from `main` after the derive stage merged. For each of the seven published names (`vault`, `status`, `blog`, `shop`, `sonarr`, `qbittorrent`, `metrics`):

1. the name answers from the dns helper;
2. with no session, the ingress helper sends the request to sign-on (302);
3. after signing in as `tester`, the app answers (200, or a redirect that is not back to sign-on);
4. the app is not reachable from the host directly.

And for the stacks: another platform instance does not reach an app; `blog` and `shop` each have a running `db`; Sonarr and qBittorrent share `/downloads`. From nothing, `up` leaves 32 passing checks and a second `tink plan` of the site and of every stack that says no changes.

**Removing a publish** was tried on `status`: dropping its `kind: publish` and applying the stack, then the site, removed its route, its sign-on rule, its dns record, its stamp and its ACL rule, with no other step, and left the other six names answering.

**Backup** (`herd.sh backup`, vault only): the volume declares a snapshot schedule, a copy to the `vm` pool and a weekly restore check. A copy was made, and both the snapshot and the copy were restored to a scratch volume where the check (`test -s /data/db.sqlite3`) passed. `tink plan` still warns that 3-2-1 is not met, which is true: the second pool is on the same host. The other five stacks answer `none: a throwaway fixture`, so the herd does not yet meet "every `keep` volume has a policy".

**As a named site, beside a platform** (`site: herd` on every stack, L-2026-10-22b, Incus 7.5.1, tink built from the `site-scoping` branch). On a homelab host that already ran a platform and role-stamped lab helpers, `up` made the seven projects, the platform bridge, six bridges and their ACLs with no step by hand, and left 32 passing checks and a second `tink plan` of every file that said no changes. The three helpers and the seven published apps carried `user.tink.site: herd`, and nothing that existed on the host was stamped or changed. `tink join --from-server --site herd` read the herd and exited 0 beside seven helpers of the unnamed site; without `--site`, the same command read the unnamed site and said that the herd's 3 helpers and 7 publishes were left out. `tink join apply dns --from-server` of the unnamed site named the two dns helpers it found there; with `--site herd --dry-run` it chose the herd's and refused to replace a `/hosts` that the derive stage had written. `down` left the host as it was before. Not run: `herd.sh backup` (the host has no `vm` pool), two complete platforms on one server, and a platform that tink manages.

## What it found

- **A file cannot be placed in a directory that is not there** (#140). Authelia's image has no `/config`, Caddy's no routes directory, so the fixture points Authelia at `/app` and imports one `apps.caddy`. A failed push also left a crash-looping instance that blocked the next apply until it was stopped by hand.
- **A one-time rule for `tink-+` is enough.** With it in place before any bridge exists, tink created six bridges from nothing, all with DHCP and DNS (#177).
- **An env value cannot hold a line break** (Incus: `Environment variables cannot contain line breaks`). WordPress's `WORDPRESS_CONFIG_EXTRA` had to be one line. `tink import` passes a multi-line value through and the apply fails; it should refuse it (#179). The failed apply left the instance half made, and the retry resumed it.
- **Two stacks with a `db` run side by side** when each has its bridge: `blog` and `shop` are the same compose file twice. On one bridge the second would be refused (#143, #177).
- **A first run of Grafana exits at once** on a root-owned volume (#176); the stack gives the volume an owner.
- **qBittorrent refuses a Host header whose port is not its own** (`Invalid Host header, port mismatch`). It only showed because the fixture first listened on 18443; on 443 the header has no port. A proxy that serves a non-standard port meets it.
- **A publish is a whole name.** Uptime Kuma's public status page cannot be opened without sign-on while its dashboard stays behind it (`platform.md`, `public-paths`). Not tried; the stack publishes the admin UI only.
- **Apps on one bridge are not isolated from each other by the bridge ACL**: Sonarr reached qBittorrent directly. That is what a stack of apps sharing data wants, and it is why a bridge per stack and not per app is the unit.
- **The host's daemon helper reads every project** (L-2026-10-22b). It found the vault volume's copy to a pool the host does not have, tried it, and reported itself degraded until `down` removed the volume. A site's backup policy reaches the helper that serves the whole server (#189, question 5).
- **"Another platform instance does not reach an app" waited for the kernel.** `wget -T` does not bound the connect to an address whose ACL drops packets; the check took about five minutes. `herd.sh` now gives the call a time limit.
- **firewalld stops answering.** Its calls often make the change and then time out, and sometimes hang. A script that believes the answer leaves interfaces in the trusted zone; `herd.sh` asks the zone instead (it left the zone clean on the first teardown that used it).
