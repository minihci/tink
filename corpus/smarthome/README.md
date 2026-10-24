# The smart-home herd

A second set of stacks for the herd test (`../herd.md`), applied to the same site as the first herd (`../site/`): the apps a Home Assistant install
grows around it. Plan and checks: #207. So far (slices 1 and 2) one stack, `home`; `esphome`, `music` and the plan-only `radios` follow.

```
TINK=./tink HERD_SET=corpus/smarthome corpus/site/herd.sh up      # the site if it is not there, the stacks of this set, then the site again
HERD_SET=corpus/smarthome corpus/site/herd.sh test                # the per-name checks and the checks in set.sh
HERD_SET=corpus/smarthome corpus/site/herd.sh down                # removes this set's stacks only; the site and the other herd stay
```

`HERD_SET` names a directory with a `set.sh` (`STACKS`, `APPS`, `set_checks`). Its `*.yaml` and `files/*` get the same `@DNS_IP@`, `@SSO_IP@` and
`@INGRESS_IP@` placeholders as the site's, filled in from the platform bridge. Run with no `HERD_SET`, `herd.sh` is the first herd, as before. `down`
with no set removes everything, the site and every set beside it.

## What is in it

| File | What |
|---|---|
| `home.yaml` | the stack `home`: Home Assistant, Mosquitto, a MariaDB recorder, Node-RED, the Matter server and the Wyoming voice trio (Piper, Whisper, openWakeWord), a project and a bridge of their own with an ACL that admits only the ingress helper. Starts from what `tink import` writes for compose files made of the corpus pages; Node-RED is published as `nodered` for admins |
| `files/mosquitto.conf` | the broker's configuration: Mosquitto answers only on localhost without a file, so the stack places one before its first start |
| `files/ha-configuration.yaml` | Home Assistant's default configuration plus an `http:` block that trusts the ingress helper |
| `set.sh` | the names to check and the checks of the stack |

Home Assistant's page asks for host networking, which `tink import` refuses (`docs/docker-to-tink.md`). The stack leaves it out, so the app runs on its
stack's bridge, and the herd does **not** measure what host networking is for (discovery by multicast). A NIC on a real LAN bridge waits for #206.

## The checks

For each published name (`ha`, `nodered`), the four of the first herd. For the stack, 14 more, 22 in all. On the run that wrote this, 21 passed and `ha` signed in failed; on a fresh `up` Whisper's `describe` fails too (below):

- the ingress helper reaches the port it fronts (the control for the denials below);
- the broker answers from Home Assistant and from Node-RED; MariaDB and the Matter server's web socket answer from Home Assistant;
- Piper, Whisper and openWakeWord each answer a Wyoming `describe` from Home Assistant;
- the platform's ingress and sign-on helpers do not reach the broker, MariaDB or the Matter server.

A second `tink plan` of the rendered `home.yaml` and of the site says no changes.

## What it found

- **`herd.sh` globbed `files/*` from the working directory**, so `corpus/site/herd.sh up` failed when run from the repository root, as the first
  herd's README says to. Fixed: the glob is taken in the script's own directory.
- **A site that is already there cannot be applied first once stacks have left it.** tink refuses to empty a derived file ("nothing is published"),
  which is right; so `up` applies the site first only when it does not exist, and the stacks come before the final apply.
- **Home Assistant behind the ingress helper answers 400.** Caddy adds `X-Forwarded-For`, and Home Assistant refuses it until its `http:` block trusts the
  proxy (`platform.md`, `trust-proxy`). The block is in `files/ha-configuration.yaml`, `check_config` accepts it and shows it, and the page still
  answers 400 ("your HTTP integration is not set-up for reverse proxies", Home Assistant 2026.10.3). Not resolved: in this version the
  `http` component reads its settings from a store and migrates YAML into it once (`components/http/config.py`), and no `.storage/http` file was ever
  written here, so the YAML may be ignored. The signed-in check for `ha` fails until that is understood (#223).
- **Two checks fail, and are left failing on purpose.** `ha` signed in (above, #223). And Whisper's `describe` on a fresh `up`: Whisper downloads its model on
  first start, and the stack cannot reach the outside, because the derived bridge has an ACL and no default egress action, so Incus rejects outbound traffic
  (#225). With `security.acls.default.egress.action=allow` set on the bridge by hand the model downloaded and the check passed; the key was then unset. The stack
  will say `egress: internet` when that exists.
- **`tink import` did what the fixture needed.** A compose service with `user: "1000:1000"` (Node-RED, the Matter server) gets a volume owned by that user
  (`initial.uid`, `initial.gid`: #176), and `entrypoint:` with `command:` (Piper, Whisper) becomes one `oci.entrypoint` that keeps the image's own entrypoint.
  The same line given to `tink run` without the entrypoint replaces it, and the instance never starts.
- **The apps of a stack are not isolated from each other**, as in the first herd: Node-RED reaches the broker, and so would anything else on the bridge.
