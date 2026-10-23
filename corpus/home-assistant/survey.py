#!/usr/bin/env python3
"""Rank Home Assistant's integrations by use and say what each asks of the host.

Sources, all public: the opt-in usage statistics (https://analytics.home-assistant.io/data.json and addons.json) and each integration's
manifest.json in home-assistant/core. A manifest states how the integration finds its devices (zeroconf, ssdp, dhcp, usb, bluetooth),
whether it talks to a device locally or to a cloud (iot_class), which other integrations it needs, and its quality-scale tier.
Nothing here measures tink; it is the demand side: what the popular integrations will ask of an Incus host. integrations.md says how
to read it and what it cannot say.

usage:
  survey.py [--top N] [--core-ref REF]     rank the top N integrations (default 200) with manifests from REF (default dev); stdlib only,
                                           fetches the two statistics files (about 9 MB) and N small manifests; writes only to stdout
  survey.py --self-test                    check the classification on sample manifests; fetches nothing

Counts change daily, so a result is a dated snapshot: it is kept as a comment on the tracking issue (#201), not committed.
"""
import argparse
import collections
import concurrent.futures as cf
import datetime
import json
import sys
import urllib.request

STATS = "https://analytics.home-assistant.io/%s.json"
RAW = "https://raw.githubusercontent.com/home-assistant/core/%s/homeassistant/components/%s/manifest.json"

# A bundle depends on the integrations it installs; counting it would add one to every discovery count.
BUNDLES = {"default_config"}

# The questions asked of every manifest, in the order they are printed.
ASKS = ("cloud", "local", "mdns", "ssdp", "dhcp", "usb", "bluetooth", "mqtt")


def fetch(url, tries=2):
    # The statistics host refuses Python's default user agent (403), and the data file is about 8 MB.
    req = urllib.request.Request(url, headers={"User-Agent": "tink-corpus-survey/1"})
    for attempt in range(tries):
        try:
            with urllib.request.urlopen(req, timeout=120) as r:
                return json.load(r)
        except Exception:
            if attempt == tries - 1:
                raise


def manifest(ref, dom):
    try:
        return dom, fetch(RAW % (ref, dom))
    except Exception as e:  # a missing manifest is reported in the output, never hidden
        return dom, {"_error": str(e)}


def asks(m):
    """What a manifest says its integration asks of the host. An integration can ask for several things."""
    deps = set(m.get("dependencies", [])) | set(m.get("after_dependencies", []))
    iot = m.get("iot_class", "")
    return {
        "cloud": iot.startswith("cloud"),
        "local": iot.startswith("local"),  # talks to something local; for a built-in integration that is nothing on the LAN
        "mdns": bool(m.get("zeroconf") or m.get("homekit")) or "zeroconf" in deps,
        "ssdp": bool(m.get("ssdp")) or "ssdp" in deps,
        "dhcp": bool(m.get("dhcp")) or "dhcp" in deps,
        "usb": bool(m.get("usb")) or "usb" in deps,
        "bluetooth": bool(m.get("bluetooth")) or bool(deps & {"bluetooth", "bluetooth_adapters"}),
        "mqtt": "mqtt" in deps or bool(m.get("mqtt")),
    }


def self_test():
    cases = [
        ("a cloud service", {"iot_class": "cloud_polling"}, {"cloud"}),
        ("a LAN device found by mDNS and DHCP", {"iot_class": "local_push", "zeroconf": ["_x._tcp.local."], "dhcp": [{"macaddress": "AA*"}]}, {"local", "mdns", "dhcp"}),
        ("a USB radio", {"iot_class": "local_polling", "usb": [{"vid": "10C4"}]}, {"local", "usb"}),
        ("depends on the discovery integrations", {"dependencies": ["zeroconf", "ssdp"], "after_dependencies": ["usb", "bluetooth_adapters"]}, {"mdns", "ssdp", "usb", "bluetooth"}),
        ("needs a broker", {"iot_class": "local_push", "dependencies": ["mqtt"]}, {"local", "mqtt"}),
        ("homekit counts as mDNS", {"homekit": {"models": ["x"]}}, {"mdns"}),
        ("calculated, nothing asked", {"iot_class": "calculated"}, set()),
    ]
    bad = 0
    for name, m, want in cases:
        got = {k for k, v in asks(m).items() if v}
        if got != want:
            bad += 1
            print("FAIL %s: got %s, want %s" % (name, sorted(got), sorted(want)))
    print("%d cases, %d failed" % (len(cases), bad))
    return 1 if bad else 0


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--top", type=int, default=200)
    ap.add_argument("--core-ref", default="dev")
    ap.add_argument("--self-test", action="store_true")
    a = ap.parse_args()
    if a.self_test:
        return self_test()

    data = fetch(STATS % "data")["current"]
    addons = fetch(STATS % "addons")
    ints = data["integrations"]
    top = [k for k, _ in sorted(ints.items(), key=lambda kv: -kv[1])[: a.top]]
    with cf.ThreadPoolExecutor(12) as ex:
        ms = dict(ex.map(lambda d: manifest(a.core_ref, d), top))

    when = datetime.datetime.fromtimestamp(data["last_updated"] / 1000, datetime.timezone.utc).strftime("%Y-%m-%d")
    print("statistics last updated %s; manifests from home-assistant/core@%s" % (when, a.core_ref))
    print("active installations %d; reports with integrations %d; installation types %s" % (data["active_installations"], data["reports_integrations"], data["installation_types"]))
    print("integrations in the statistics: %d; ranked here: %d; manifests missing: %s" % (len(ints), len(top), [k for k, v in ms.items() if "_error" in v]))

    rows = [(d, ints[d], ms[d], asks(ms[d])) for d in top if "_error" not in ms[d] and d not in BUNDLES]
    print("\nintegration_type", dict(collections.Counter(r[2].get("integration_type", "?") for r in rows)))
    print("iot_class", dict(collections.Counter(r[2].get("iot_class", "-") for r in rows)))
    print("quality_scale", dict(collections.Counter(r[2].get("quality_scale", "-") for r in rows)))
    print("\n(the bundle %s is left out of the counts below)" % ", ".join(sorted(BUNDLES)))
    print("what the top %d ask of the host (an integration can ask for several):" % len(rows))
    for key in ASKS:
        sel = [r for r in rows if r[3][key]]
        print("  %-9s %3d   %s" % (key, len(sel), ", ".join("%s(%dk)" % (r[0], r[1] // 1000) for r in sel[:8])))

    print("\ndevice, hub and service integrations in the top 100 that need more than the internet:")
    for dom, n, m, f in rows[:100]:
        if m.get("integration_type") in ("helper", "system", "entity"):
            continue
        needs = [k for k in ASKS[1:] if f[k]]
        if needs:
            print("  %4dk %-26s %-8s %-14s q=%-9s %s" % (n // 1000, dom, m.get("integration_type", "?"), m.get("iot_class", "-"), m.get("quality_scale", "-"), ",".join(needs)))

    print("\nthe add-ons people run next to it (the companions a container install must rebuild), by installs:")
    for k, v in sorted(addons.items(), key=lambda kv: -kv[1].get("total", 0))[:30]:
        print("  %7d %s" % (v.get("total", 0), k))
    return 0


if __name__ == "__main__":
    sys.exit(main())
