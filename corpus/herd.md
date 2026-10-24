# The herd test

The corpus so far asks whether one app runs. The point of tink is a *site*: several stacks, one shared proxy, names, sign-on and
backup, with a person managing as little as possible. The herd test is the corpus applied to a site.

## Three levels, one count each

| Level | Question | Today |
|---|---|---|
| 1. Runs | Does the app answer when its own line is pasted into tink? | 14 of 21 (`status.md`) |
| 2. Imports | Does its published compose line become a stack with no hand editing? | 13 of the 17 apps with a compose file (14 of 18 files); the other four are refused with a reason (#179, #180) |
| 3. Herd | Is it applied together with the others, onto one site, and wired? | all 6 stacks (7 names) are on the fixture site and answer behind sign-on. Against the 5 criteria below: 2 and 3 hold for every stack; 5 holds (tried on one, `status`); 4 holds for one stack (`vault`); 1 holds except for eight lines each stack still writes (`site/README.md`); the smart-home herd adds four stacks, three applied (see below) |

## The first herd

Six stacks, eight instances, chosen to cover the shapes that interact (see `shapes.md` and `platform.md`):

| Stack | Instances | Why it is in |
|---|---|---|
| `vault` | Vaultwarden | one container; non-browser clients; a `keep` volume; told its public address |
| `status` | Uptime Kuma | an admin UI; a public status page that must bypass sign-on; WebSockets |
| `blog` | WordPress + MySQL | a sibling database called `db`; a public site |
| `shop` | WordPress + MySQL, again | a second stack with its own `db`, side by side with `blog` |
| `media` | Sonarr + qBittorrent | data shared between two apps (so one project); UDP and a non-HTTP port; admin UIs |
| `metrics` | Grafana | OIDC; a volume owned by the image's own user (#176) |

The shared site is what the herd is applied *to*: an ingress (Caddy), a DNS helper (CoreDNS), a sign-on helper (Authelia) and a backup
target. It is a fixture of the test, not of the stacks, and it is in [site/](site/README.md) (the backup target is not there yet).

## The first layout

One project and one bridge per stack, with an ACL on each bridge that lets only the ingress helper in. This is what
`docs/network.md`, `docs/network-acl.md` and `docs/join.md` already describe. A second run uses a single shared bridge, to see which
problems the layout really removes (the `db` name clash is the known one).

## The smart-home herd

A second herd on the same site (`smarthome/README.md`; the plan and the checks are #207): the apps a Home Assistant install grows around it. Four stacks and thirteen
apps. `home` (Home Assistant, Mosquitto, MariaDB, Node-RED, the Matter server, Piper, Whisper, openWakeWord), `esphome` and `music` are applied: ten apps, four published
names (`ha`, `nodered`, `esphome`, `music`). `radios` (Zigbee2MQTT, Z-Wave JS UI, the OpenThread border router) is planned and never applied, because it needs a radio.
Of 37 checks, 36 passed on the run that wrote this. The apps that ask for host networking run on their stack's bridge, so what that is for (discovery on the LAN) is not
measured, and the list of what else is not measured, with the reason, is in the set's README. Two failures are open: Home Assistant answers 400 behind the proxy (#223), and
Whisper cannot download its model on a fresh `up` because a stack's apps cannot reach the outside (#225).

## A stack passes level 3 when

1. Its stack file declares the app, its volumes' backup policy and one `kind: publish`, and nothing else is hand-made: no bridge, no
   ACL, no static address, no proxy route, no DNS record, no sign-on rule.
2. `tink plan` shows every change that publish implies, and a second plan says `no changes`.
3. The name answers through the proxy, behind sign-on unless the app's tags say part of it must be public.
4. Every `keep` volume (see `platform.md`) has a backup policy, and one copy has been restored and checked.
5. Removing the stack's publish removes its name, route and rule, and nothing else.

## What is left

- Criterion 1: the bridge, ACL, static address, proxy route, dns record and sign-on rule are derived now. What each stack still writes beside the
  app and the grant: its project, the offer keys, the name the app is told it has, the compose file's host port taken out, a volume's owner
  where the image runs as its own user (#176), and a backup answer. `site/README.md` lists them.
- Criterion 4: only the `vault` volume has a real policy; the other stacks say `none` for a throwaway fixture. A copy to the same host is not 3-2-1.
- The apps that must be told their public address or to trust the proxy (`platform.md`) have no place to say so in a publish.
- A public path (Uptime Kuma's status page) cannot be opened apart from its name's sign-on.
