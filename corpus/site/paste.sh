#!/bin/sh
# Stands in for the derive stage (#130, #177) in the herd test: runs `tink join` over the site and every stack, and writes its renderings into
# the places they belong. In a finished tink these are planned changes; here they are a paste, so the work is visible and can be counted.
#   - files/apps.caddy: the `caddy` rendering
#   - files/configuration.yml, between its BEGIN/END markers: the `sso` rendering
#   - each stack's network-acl: the `acl` rendering
# The dns records are not here: `tink join apply dns` writes those.
set -eu
TINK=${TINK:-tink}
HERE=$(cd "$(dirname "$0")" && pwd)
STACKS="vault status blog shop media metrics"
FILES="$HERE/site.yaml"
for s in $STACKS; do FILES="$FILES $HERE/$s.yaml"; done

# shellcheck disable=SC2086
"$TINK" join $FILES --only caddy 2>/dev/null | grep -v '^==' > "$HERE/files/apps.caddy.new"
{ printf '%s\n' "# The proxy routes for the published apps: the \`caddy\` rendering of \`tink join\`, written by paste.sh until the derive stage writes it."; cat "$HERE/files/apps.caddy.new"; } > "$HERE/files/apps.caddy"
rm "$HERE/files/apps.caddy.new"

"$TINK" join $FILES --only sso 2>/dev/null | grep -v '^==' > "$HERE/files/sso.frag"
"$TINK" join $FILES --only acl 2>/dev/null | grep -v '^==' > "$HERE/files/acl.frag"

python3 -I - "$HERE" "$STACKS" <<'PY'
import re, sys, os
here, stacks = sys.argv[1], sys.argv[2].split()

# sso rules between the markers
cfg = os.path.join(here, "files/configuration.yml")
s = open(cfg).read()
frag = open(os.path.join(here, "files/sso.frag")).read().rstrip("\n")
begin = "    # BEGIN tink join sso (written by paste.sh until the derive stage writes it)\n"
end = "    # END tink join sso\n"
i, j = s.index(begin) + len(begin), s.index(end)
s = s[:i] + "".join("    " + l + "\n" for l in frag.splitlines()) + s[j:]
open(cfg, "w").write(s)

# acl rules into each stack's network-acl document
acls = {}
cur = None
for line in open(os.path.join(here, "files/acl.frag")).read().splitlines():
    m = re.match(r"# network-acl (\S+) \(network", line)
    if m:
        cur = m.group(1)
        acls[cur] = []
    elif cur and line.strip() and line != "ingress:":
        acls[cur].append(line)
for st in stacks:
    path = os.path.join(here, st + ".yaml")
    docs = open(path).read().split("\n---\n")
    for k, d in enumerate(docs):
        m = re.search(r"^kind: network-acl.*\nname: (\S+)\n", d, re.M)
        if not m:
            continue
        lines = d.split("\n")
        keep = []
        for l in lines:
            if l.startswith("ingress:") or l.startswith("  - {"):
                continue
            keep.append(l)
        body = "\n".join(keep).rstrip("\n")
        rules = acls.get(m.group(1), [])  # an ACL nothing is published through says "none" (`ingress: []`): leaving the direction out would leave a removed publish's rule in place
        docs[k] = body + ("\ningress:\n" + "\n".join(rules) if rules else "\ningress: []")
    open(path, "w").write("\n---\n".join(d.rstrip("\n") for d in docs) + "\n")
PY
rm -f "$HERE/files/sso.frag" "$HERE/files/acl.frag"
