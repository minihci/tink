# `tink plan explain`: what the passes made of a stack

**Status: built (#129); unit-tested, and run on a local Incus 7.5.1 server on 2026-10-22 (L-2026-10-21b)** against the herd test's site and a stack that says `network: own`: it listed the bridge, the ACL, the address and the ACL rules before the stack was applied, and the routes, rules and records of the site's three derived files after.

A stack is facts, and the passes work out the rest: its defaults, a bridge of its own, the addresses, the ACL rules, the proxy's routes, the sign-on helper's rules, the dns helper's records. So what `plan` is about to do is not in the files. `explain` is where to read it, and which pass made each part. It is the debug and audit view of the derive stage, and what replaces the printing of `tink join`.

```
tink plan explain [FILE...]           # the same files and flags as tink plan: --socket, --offline, --secrets, --identity
```

It runs the passes `plan` runs before it plans anything, and plans nothing. It writes nothing, anywhere.

```
stack vault, as the passes expanded it. Each pass lists the lines it reported and what it made (+) or changed (~).

network: own
  stack vault: network: own: bridge tink-vault 10.107.175.0/24 (new), ACL tink-vault-in
  instance/vaultwarden: address 10.107.175.2 on tink-vault allocated
  ~ instance/vaultwarden (project herd-vault)
      devices.eth0.ipv4.address: 10.107.175.2
      devices.eth0.network: tink-vault
      ...
  + network/tink-vault
      config.ipv4.address: 10.107.175.1/24
      config.ipv4.dhcp.ranges: 10.107.175.65-10.107.175.254
      ...

ingress rules
  network-acl/tink-vault-in: 1 ingress rule(s) derived from the publishes vault
  ~ network-acl/tink-vault-in
      ingress: [{"action":"allow", ... "destination_port":"80", "source":"192.0.2.3/32" ...}]

derived files
  file/ingress-routes: caddy derived for 1 published name(s): vault
  ~ file/ingress-routes (project herd-site)
      content: (the caddy region, below)
        | @vault host vault.home.example.org
        | handle @vault {
        | 	import sso
        | 	reverse_proxy 10.107.175.2:80 { ...
```

For each pass that did something, in the order the passes run (`internal/prepare`):

- the lines the pass reported, which say what a comparison cannot: that an address was kept and not chosen, which publishes a rule covers, which subnet was taken and why;
- each resource it **made** (`+`, with its fields) or **changed** (`~`, with only the fields the pass made or altered, and the old value when there was one);
- for a file that says `derive:`, the **region** it filled in, line by line (the whole file for `derive: mesh`, which is JSON and has no region). This is the view the `tink join --only caddy|sso|dns` commands printed.

A stack that needs nothing derived says so: `nothing was derived; it plans as it is written`.

## No server

The passes that work out the rest from the site read the server (the platform's helpers, and the stacks' publishes). With none to reach, `explain` still runs: what does not need one is shown, a line on stderr says there is no server, and the first pass that does need one stops with a message that says so, after the passes before it have been shown. Images are left as written.

## Nothing secret is printed

The secrets pass is a count (`N secret reference(s) expanded; the values are not shown`). A value whose key looks like a secret, or was expanded from one, is `(value hidden)`. A file's content is never printed whole: only the region a derive filled in, or its size. The output goes through the same redactor as `plan`.

## How provenance is kept

Not by the passes. `prepare` tells a recorder what each pass was handed and what it left (`prepare.Tracer`), and the recorder compares the two: a field that differs was made by that pass. So there is no second copy of any resource in normal use (only a command that traces takes the snapshots), no provenance in the resources, and a new pass is explained without being changed. The cost is that provenance is per pass and per field, not per reason; the reasons are the lines each pass reports.

## Limits

- It shows what the passes made, not the whole expanded stack as a file you could apply. `tink export` is that for live objects; an output that is valid stack YAML (the eject for the recipe, #131) is not built.
- The derive passes see the server as it is now. A file that derives from the stamps follows the stacks that have been *applied*, and `explain` shows exactly that.
