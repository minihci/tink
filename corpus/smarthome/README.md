# The smart-home herd

A second set of stacks for the herd test (`../herd.md`), applied to the same site as the first herd (`../site/`): the apps a Home Assistant install
grows around it. Plan and checks: #207. Four stacks: `home`, `esphome` and `music` are applied; `radios` is planned and never applied.

```
TINK=./tink HERD_SET=corpus/smarthome corpus/site/herd.sh up      # the site if it is not there, the stacks of this set, then the site again
HERD_SET=corpus/smarthome corpus/site/herd.sh test                # the per-name checks, the checks in set.sh, and the plan of the plan-only stack
HERD_SET=corpus/smarthome corpus/site/herd.sh down                # removes this set's stacks only; the site and the other herd stay
```

`HERD_SET` names a directory with a `set.sh` (`STACKS`, `PLAN_ONLY`, `APPS`, `set_checks`). A stack in `PLAN_ONLY` is planned by `up` and `test` and never applied. Its `*.yaml` and `files/*` get the same `@DNS_IP@`, `@SSO_IP@` and
`@INGRESS_IP@` placeholders as the site's, filled in from the platform bridge. Run with no `HERD_SET`, `herd.sh` is the first herd, as before. `down`
with no set removes everything, the site and every set beside it.

## What is in it

| File | What |
|---|---|
| `home.yaml` | the stack `home`: Home Assistant, Mosquitto, a MariaDB recorder, Node-RED, the Matter server and the Wyoming voice trio (Piper, Whisper, openWakeWord), a project and a bridge of their own with an ACL that admits only the ingress helper. Starts from what `tink import` writes for compose files made of the corpus pages; Node-RED is published as `nodered` for admins |
| `files/mosquitto.conf` | the broker's configuration: Mosquitto answers only on localhost without a file, so the stack places one before its first start |
| `files/ha-http-store.json` | Home Assistant's HTTP settings store (`/config/.storage/http`) trusting the ingress helper, placed before its first start with `seed: true`; Home Assistant writes the rest of its configuration itself |
| `esphome.yaml` | the stack `esphome`: the ESPHome device-builder dashboard, published as `esphome` for admins |
| `music.yaml` | the stack `music`: Music Assistant, published as `music` for the family group |
| `radios.yaml` | the stack `radios`: Zigbee2MQTT, Z-Wave JS UI and the OpenThread border router, which need a radio on the host. Declared and planned, never applied (below). Zigbee2MQTT and Z-Wave JS UI are published as `z2m` and `zwave` for admins in the plan |
| `set.sh` | the names to check and the checks of the stacks |

Home Assistant's page asks for host networking, which `tink import` refuses (`docs/docker-to-tink.md`). The stack leaves it out, so the app runs on its
stack's bridge, and the herd does **not** measure what host networking is for (discovery by multicast). A NIC on a real LAN bridge waits for #206.

## The checks

For each published name (`ha`, `nodered`, `esphome`, `music`), the four of the first herd. For the stacks, 16 more and 2 controls; and for `radios` 5, which need no radio because they read the plan: it is accepted, it would create the stack, nothing of it exists on the server, it derives the ACL rules for its two publishes, and it allocates their addresses. 39 in all. On the run that wrote this, 38 passed and `ha` signed in failed (below):

- the ingress helper reaches the port it fronts (the control for the denials below);
- the broker answers from Home Assistant and from Node-RED; MariaDB and the Matter server's web socket answer from Home Assistant;
- Piper, Whisper and openWakeWord each answer a Wyoming `describe` from Home Assistant;
- the platform's ingress and sign-on helpers do not reach the broker, MariaDB or the Matter server;
- ESPHome and Music Assistant, in stacks of their own, do not reach the `home` stack's broker. Their stacks say `egress: open`, and a control shows it (each reaches the platform's dns helper), so the denial is the `home` stack's ACL.

A second `tink plan` of the rendered `home.yaml` and of the site says no changes.

## What it found

- **`herd.sh` globbed `files/*` from the working directory**, so `corpus/site/herd.sh up` failed when run from the repository root, as the first
  herd's README says to. Fixed: the glob is taken in the script's own directory.
- **A site that is already there cannot be applied first once stacks have left it.** tink refuses to empty a derived file ("nothing is published"),
  which is right; so `up` applies the site first only when it does not exist, and the stacks come before the final apply.
- **Home Assistant behind the ingress helper answered 400, and the cause was the fixture** (#223, #238). Caddy adds `X-Forwarded-For`, and Home Assistant refuses it until its
  HTTP settings trust the proxy (`platform.md`, `trust-proxy`). The first fixture placed a `configuration.yaml` with an `http:` block, which includes
  `automations.yaml`, `scripts.yaml` and `scenes.yaml`; Home Assistant writes those itself only when it writes `configuration.yaml` too, so they were missing
  and it started in recovery mode, which ignores YAML and runs `http` with its default (no proxy trusted). Adding the three files fixed it for 2026.10 (PR #239),
  but from 2026.8 Home Assistant imports `http:` **once**, on the first boot, and ignores the YAML after that (a first boot without the block marks the migration
  done), and the YAML block stops working in 2027.2. So the stack now places the store file itself, `/config/.storage/http`, before the first start, and nothing
  else: Home Assistant bootstraps its own `configuration.yaml` and include files. The file is `seed: true`, because Home Assistant rewrites it (a trial
  config that is promoted, or the UI), and `kind: file` makes the missing `.storage` directory (#140). The other route Home Assistant provides is an admin
  WebSocket command, `http/config/configure`, then `http/config/promote` (a new config reverts after 5 minutes otherwise); tested under Podman, not built here,
  because it needs an admin token and secrets are accepted only in `environment.*`. The evidence is on #223 and #238.
  Run on a local Incus 7.5.1 server on 2026-10-24 (Home Assistant 2026.10.3), from a fresh `ha-config` volume: the instance was created with the store file
  placed in the volume before its first start; Home Assistant wrote `configuration.yaml`, the three include files and `secrets.yaml` itself, logged no
  recovery mode and no proxy error, and left the store file as placed; the herd test passed, `ha` signed in included; a second plan said no changes.
  **Limits:** the store file is undocumented (a forum reply calls editing it unsupported), so a Home Assistant release may change it; and because it is seeded,
  changing the trusted address in the stack does not reach a running instance (the UI or the WebSocket command does).
- **The stacks say `egress: open`** (#225). The derived bridge of `network: own` rejects outbound traffic by default, so Whisper, which downloads its model on first
  start, could not, and its `describe` failed on a fresh `up`; Home Assistant's own update check could not connect either. With the field, a fresh `up` from nothing
  (volumes included) downloaded the model and every voice server answers. `open` is Incus's `allow` on the bridge and not a promise of internet access (`docs/network.md`).
- **`tink import` did what the fixture needed.** A compose service with `user: "1000:1000"` (Node-RED, the Matter server) gets a volume owned by that user
  (`initial.uid`, `initial.gid`: #176), and `entrypoint:` with `command:` (Piper, Whisper) becomes one `oci.entrypoint` that keeps the image's own entrypoint.
  The same line given to `tink run` used to replace the entrypoint, and the instance never started (#230); `tink run` now reads the entrypoint from the registry and puts the command after it.
- **The apps of a stack are not isolated from each other**, as in the first herd: Node-RED reaches the broker, and so would anything else on the bridge.
- **The ESPHome and Music Assistant stacks run without host networking** and answer behind sign-on (`esphome` 200, `music` 302 to its own `setup` page). What their
  pages say host networking is for is not measured: ESPHome's mDNS discovery of devices, and Music Assistant's discovery of and streaming to players, which needs its
  stream port (8097) reachable by IP from the players' network. In the stack the port has no route and the bridge's ACL admits only the ingress helper to 8095.
- **ESPHome's own login is on behind the site's.** `/` (the page's shell) answers 200 and `/devices` and `/info` answer 401 without the dashboard's credentials
  (`ESPHOME_USERNAME` and `ESPHOME_PASSWORD`, set in the stack). From those status codes, seen with `wget` through the ingress helper and not in a browser, a person would sign in
  twice. The signed-in check only sees the shell. #200 (an app published with its own sign-on) is the proposal for this.
- **Apps in different stacks cannot reach each other.** Each bridge's ACL admits only the ingress helper, so Home Assistant could not reach Music Assistant's stream port or
  ESPHome from the `home` stack. Not tested; it follows from the ACL, and it is why the apps that talk to each other share a stack.

## What is not measured, and why

- **Discovery by multicast** (mDNS, SSDP, DHCP sniffing, Thread): Home Assistant, ESPHome, the Matter server and Music Assistant ask for host networking, which
  import refuses and which has no equivalent here. They run on their stack's bridge, so what host networking is for is not measured. A NIC on a real LAN bridge
  waits for #206 (declare that an app needs the LAN). Upstream Incus has no host-network mode for OCI containers (checked 2026-10-24).
- **`radios`**: Zigbee2MQTT and Z-Wave JS UI need a USB adapter and the border router a Thread radio, and this host has none. The stack is declared (two of its three instances are
  what `tink import` writes), and `tink plan` accepts it: it would create a project, a bridge and an ACL with a rule for each of the two published names, three
  volumes and three instances with their addresses. It was **never applied**, so nothing about those three apps running is claimed. The border router's page also asks
  for `--network=host`, `--cap-add=NET_ADMIN` and IP forwarding switched on in the host first; none has a translation, and the stack leaves them out.
- **Zigbee2MQTT's broker**: its page puts the broker (`mqtt://...`) beside it. The broker is in the `home` stack, on another bridge whose ACL admits only the ingress
  helper, so Zigbee2MQTT in `radios` could not reach it (not tested; it follows from the ACL). An app that talks to a broker goes in the broker's stack.
- **Backup**: every volume says `none` ("a throwaway fixture"). The first herd's `vault` is the only one with a real policy.
- **The failing check** (`ha` signed in, #223).
