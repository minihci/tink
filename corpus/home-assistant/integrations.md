# What the popular Home Assistant integrations ask of a host

`survey.py` ranks Home Assistant's integrations by how many installations run them and reads each one's manifest to say what it asks of the machine
it runs on. It is the **demand side** of the corpus for one ecosystem: the other pages say what an app's install instructions need, this says what the
things people add to an app will need, so that tink is built against what is asked and not against what is guessed (the roadmap's demand gate,
`docs/roadmap.md`). It measures nothing about tink.

## Run it

```sh
python3 corpus/home-assistant/survey.py                 # the top 200, manifests from home-assistant/core@dev
python3 corpus/home-assistant/survey.py --top 300 --core-ref 2026.10.0
python3 corpus/home-assistant/survey.py --self-test     # the classification on sample manifests; fetches nothing
```

Standard library only; it writes to standard output and nothing else. It fetches the two statistics files (about 9 MB) and one small manifest per
integration. **A result is a dated snapshot, so it is not committed**: the counts change every day. Each run that is worth keeping goes on the tracking
issue (#201) as a comment, with the date the output prints.

## What it reads

- Usage: `https://analytics.home-assistant.io/data.json` (installations, installation types, how many installations run each integration) and
  `addons.json` (how many installations run each add-on). Both are published by the project and are opt-in for the installations that report.
- Needs: `homeassistant/components/NAME/manifest.json` in `home-assistant/core`, for each ranked integration.

## What the output says

| Line | Meaning |
|---|---|
| `statistics last updated`, `home-assistant/core@REF` | which data the run used; a snapshot is only comparable with another of the same refs |
| `integration_type`, `iot_class`, `quality_scale` | how many of the ranked integrations carry each value, as the manifests write them. `quality_scale` is the project's own tier for how well an integration is built (`bronze` to `platinum`; `internal` for the project's own, `legacy` for old ones; no value if it has none) |
| `cloud` | `iot_class` starts with `cloud`: it needs the internet and nothing else |
| `local` | `iot_class` starts with `local`: it talks to something local. For a built-in integration that is not a device on the LAN, so read it with the integration type |
| `mdns` | it declares `zeroconf` or `homekit` discovery, or depends on the zeroconf integration: it must hear multicast DNS on the LAN |
| `ssdp` | it declares `ssdp` discovery or depends on the ssdp integration: it must hear SSDP/UPnP multicast |
| `dhcp` | it declares `dhcp` discovery or depends on it: it finds devices by watching DHCP requests, so it must see the LAN's broadcasts |
| `usb` | it declares `usb` discovery or depends on it: a USB radio or serial device |
| `bluetooth` | it declares `bluetooth` discovery or depends on the Bluetooth integrations: the host's Bluetooth adapter |
| `mqtt` | it depends on, or discovers through, MQTT: a broker has to exist |
| the table of device, hub and service integrations | the top 100 that need more than the internet, with what they ask for, most-installed first |
| the add-on ranking | what people run beside Home Assistant; for a container install, which has no add-ons, this is the list of companions to rebuild by hand |

`mdns`, `ssdp` and `dhcp` are the shapes `discover-mdns`, `discover-ssdp` and `discover-dhcp` in `../shapes.md`; `usb` is `device`; `bluetooth` is
`bluetooth` (and `host-socket`, for the D-Bus mount); `mqtt` means a sibling broker. The first three share one need that a port does not describe: a
presence on the LAN, which a bridge that translates addresses does not give (`docs/docker-to-tink.md`; #206).

The `default_config` bundle is left out of the counts. It lists the discovery integrations as dependencies, so counting it would add one to each.

## What it cannot say

- **Who is counted.** The statistics are opt-in: roughly four in five installations report their integrations. About four in five installations are the
  appliance image (Home Assistant OS) and about one in six is the container, and the integration counts are **not broken down by installation type**, so
  the mix a container user has is not known.
- **What `local` means.** See above; it is a statement about where the integration talks, not about the LAN.
- **What an add-on needs.** The add-on ranking says how many installations run each; what a companion needs of the host is in its own install page
  (the companions are added to this corpus one by one, #204), not in the statistics.
- **Declared against actual.** A manifest says how an integration can find a device, not whether a given install uses it.
- **The host's refusal of Python.** The statistics host answers 403 to Python's default user agent; the script sends its own.
- **A changed format.** If either statistics file changes shape the script fails with an error; it does not guess.

## Keeping a snapshot

The first one is the comment "Survey snapshot, 2026-10-23" on #201. A new one is a new comment (the old ones are left as they are), with the printed
date and refs in its first line, so that the history of the demand is the comment trail and none of it ages in the repository.
