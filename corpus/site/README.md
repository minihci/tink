# The herd test's site

The shared site the herd test (`../herd.md`) applies stacks to, and the first stack on it. A fixture, not the platform recipe (#131): the addresses and the zone are documentation values, certificates come from Caddy's own local CA, and the site's one user is a throwaway.

```
TINK=./tink corpus/site/herd.sh up      # the site, then the vault stack
corpus/site/herd.sh test                # the checks below
corpus/site/herd.sh down                # removes everything it made
```

It runs on a local Incus server and touches the host in one place: on a host running firewalld it puts the two bridges in the `trusted` zone for the running firewall (`sudo firewall-cmd --zone=trusted --add-interface=...`, not permanent). A new bridge needs this to get DHCP and DNS from the host (#177). With `SUDO_ASKPASS` set it asks through that.

## What is in it

| File | What |
|---|---|
| `site.yaml` | a project `herd-site`, the platform bridge `herd0`, and three role helpers: **dns** (CoreDNS), **sso** (Authelia), **ingress** (Caddy), with their files |
| `files/` | the Corefile and zone, the Caddyfile and its routes, Authelia's configuration and one user (`tester`, password `herd-test-password`) |
| `vault.yaml` | the first herd stack: Vaultwarden, from `tink import` plus the site's half |
| `herd.sh` | `up`, `test`, `down` |

The ingress helper is reached from the host on `127.0.0.1:18443`. Names are tried with `curl --resolve NAME:18443:127.0.0.1`.

## The first stack, and what is still by hand

`vault.yaml` is what `tink import` writes for Vaultwarden's own compose file, plus the lines marked `site:`. Each is something the derive stage (#177, #130) should make unnecessary. Counted:

| # | By hand | Should become |
|---|---|---|
| 1 | a project of its own | the stack's project |
| 2 | a bridge for the stack | derived (#177) |
| 3 | an ACL on it, with the join's `acl` rendering pasted in | derived (#177, #130) |
| 4 | a static address on the app's NIC | allocated (#177) |
| 5 | the NIC pointing at the bridge | derived (#177) |
| 6 | the offer keys (`user.tink.offer.web.*`) | stated by the app's stack, or imported |
| 7 | a `kind: publish` | stays: it is the grant |
| 8 | the public name in the app's environment (`DOMAIN`) | a place in the publish for it |
| 9 | removing the compose file's host port | the importer's offer-or-port choice (#179) |
| 10 | a backup answer for the volume | stays: it is the site's decision |
| 11 | the `caddy` rendering pasted into `files/apps.caddy` | a derived file (#130) |
| 12 | the `sso` rendering pasted into `files/configuration.yml` | a derived file (#130) |
| 13 | firewall: the bridge in a trusted zone | a documented prerequisite (#177) |

`tink join apply dns` writes the dns records; nothing else the join renders is applied by tink yet.

## The checks (`herd.sh test`)

Run on 2026-10-21 (L-2026-10-21b), Incus 7.5.1, Caddy 2.11.7, CoreDNS 1.15.0, Authelia 4.39.28:

1. The name answers from the dns helper.
2. With no session, the ingress helper sends the request to sign-on (302).
3. After signing in as `tester` (group `family`), the app answers (200).
4. The app is not reachable from the host directly: the ACL on its bridge admits only the ingress helper's address.
5. The ingress helper reaches the app; another platform instance (the sign-on helper) does not.

A second `tink plan` of `site.yaml` and of `vault.yaml` says no changes, run after a from-scratch `up` (the site's files already hold the vault stack's pasted renderings).

Not yet checked, so the stack does not pass level 3 of `../herd.md`: a backup copy and a restore have not been made, and removing the publish has not been tried. The pasted files are the site's, so the second plan says nothing about whether the vault stack's own publish still drives them.

## What it found

- **A file cannot be placed in a directory that is not there** (#140). Authelia's image has no `/config`, and Caddy's has no `/etc/caddy/routes`, so the fixture points Authelia at `/app` with an `oci.entrypoint` and imports a single `apps.caddy` instead of a directory of files.
- **An instance that exits at its first start and restarts on its own blocks the next apply** (`Instance is busy running a "stop" operation`) and the file reads fail on it, until it is stopped. It happened when the first apply put the config in a directory that was not there. The file-before-first-start work (`docs/first-start-files-design.md`) covers the case where the push succeeds.
- **The join's ACL rendering matches the rule written by hand**, and the proxy route, sign-on rule and dns record it prints were enough to make the name work, once pasted into the right files.
