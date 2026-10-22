#!/bin/sh
# Brings the herd test's fixture site up on a local Incus server, and takes it down. See README.md.
#
#   TINK=./tink ./herd.sh up      # the site (dns, sign-on, ingress), then the vault stack
#   ./herd.sh test                # the checks of README.md, against what is up
#   ./herd.sh down                # removes everything it made
#
# It touches the host in one place only, the firewall: on a host running firewalld it puts the two bridges in the trusted zone for the
# running firewall (not permanently). Nothing else outside Incus.
set -eu

TINK=${TINK:-tink}
HERE=$(cd "$(dirname "$0")" && pwd)
SUDO=sudo
[ -n "${SUDO_ASKPASS:-}" ] && SUDO="sudo -A"

fw() { # fw add|remove BRIDGE
	if command -v firewall-cmd >/dev/null 2>&1 && systemctl is-active --quiet firewalld 2>/dev/null; then
		$SUDO firewall-cmd --zone=trusted --"$1"-interface="$2" >/dev/null
	fi
}

exists() { "$@" >/dev/null 2>&1; }

up() {
	exists incus project show herd-site || incus project create herd-site -c features.profiles=false
	exists incus network show herd0 || incus network create herd0 ipv4.address=192.0.2.1/24 ipv4.nat=true ipv6.address=none \
		--description "the herd test's platform bridge"
	fw add herd0

	exists incus project show herd-vault || incus project create herd-vault -c features.profiles=false
	exists incus network acl show tink-vault-in || incus network acl create tink-vault-in --description "only the ingress helper may reach the vault app" </dev/null
	exists incus network show tink-vault || incus network create tink-vault ipv4.address=198.51.100.1/24 ipv4.nat=false ipv6.address=none \
		security.acls=tink-vault-in --description "the vault stack's bridge"
	fw add tink-vault

	"$TINK" plan apply --adopt "$HERE/site.yaml"
	"$TINK" join apply dns "$HERE/site.yaml" "$HERE/vault.yaml"
	"$TINK" plan apply --adopt "$HERE/vault.yaml"
}

# test: the herd's checks for the vault stack, by hand until the derive stage and a heartbeat exist.
test() {
	R="--resolve vault.home.example.org:18443:127.0.0.1 --resolve auth.home.example.org:18443:127.0.0.1"
	J=$(mktemp)
	fail=0
	ok() { printf '  ok    %s\n' "$1"; }
	bad() { printf '  FAIL  %s\n' "$1"; fail=1; }

	[ "$(dig +short +time=3 +tries=1 @192.0.2.10 vault.home.example.org)" = 192.0.2.3 ] && ok "the name answers (dns helper)" || bad "the name answers (dns helper)"
	code=$(curl -sk -m 10 $R -o /dev/null -w '%{http_code}' https://vault.home.example.org:18443/)
	[ "$code" = 302 ] && ok "no session: sent to sign-on ($code)" || bad "no session: sent to sign-on ($code)"
	curl -sk -m 10 $R -c "$J" -H 'Content-Type: application/json' \
		-d '{"username":"tester","password":"herd-test-password","keepMeLoggedIn":false}' https://auth.home.example.org:18443/api/firstfactor >/dev/null
	code=$(curl -sk -m 10 $R -b "$J" -o /dev/null -w '%{http_code}' https://vault.home.example.org:18443/)
	[ "$code" = 200 ] && ok "signed in: the app answers ($code)" || bad "signed in: the app answers ($code)"
	code=$(curl -s -m 5 -o /dev/null -w '%{http_code}' http://198.51.100.10/ || true)
	[ "$code" = 000 ] && ok "the app is not reachable except through the ingress helper (host: no answer)" || bad "the app answered the host ($code)"
	incus exec --project herd-site ingress -- wget -q -O /dev/null -T 4 http://198.51.100.10/ </dev/null && ok "the ingress helper reaches the app" || bad "the ingress helper reaches the app"
	if incus exec --project herd-site sso -- wget -q -O /dev/null -T 4 http://198.51.100.10/ </dev/null; then bad "another platform instance reached the app"; else ok "another platform instance does not reach the app"; fi
	rm -f "$J"
	return $fail
}

down() {
	for p in herd-vault herd-site; do
		exists incus project show "$p" || continue
		for i in $(incus list --project "$p" -c n -f csv </dev/null); do incus delete --project "$p" "$i" -f </dev/null; done
		for v in $(incus storage volume list default --project "$p" -c n -f csv </dev/null 2>/dev/null); do
			incus storage volume delete default "$v" --project "$p" </dev/null >/dev/null 2>&1 || true
		done
		for f in $(incus image list --project "$p" -c f -f csv </dev/null); do incus image delete --project "$p" "$f" </dev/null; done
		incus project delete "$p" </dev/null
	done
	if exists incus network show tink-vault; then fw remove tink-vault; incus network delete tink-vault </dev/null; fi
	exists incus network acl show tink-vault-in && incus network acl delete tink-vault-in </dev/null
	if exists incus network show herd0; then fw remove herd0; incus network delete herd0 </dev/null; fi
}

case "${1:-}" in
up) up ;;
test) test ;;
down) down ;;
*) echo "usage: TINK=./tink $0 up|test|down" >&2; exit 2 ;;
esac
