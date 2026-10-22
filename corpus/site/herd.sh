#!/bin/sh
# Brings the herd test's fixture site and its stacks up on a local Incus server, checks them, and takes them down. See README.md.
#
#   TINK=./tink ./herd.sh up      # the site (dns, sign-on, ingress), the six stacks, then the site again
#   ./herd.sh test                # the checks of README.md, against what is up
#   TINK=./tink ./herd.sh backup  # a copy of the vault stack's volume to another pool, and a restore check of the snapshot and of the copy
#   ./herd.sh down                # removes everything it made
#
# It touches the host in one place only, the firewall: on a host running firewalld it puts the platform bridge and every bridge named tink-*
# in the trusted zone of the running firewall (not permanently), so that a new bridge gets DHCP and DNS from the host. Nothing else outside Incus.
set -eu

TINK=${TINK:-tink}
HERE=$(cd "$(dirname "$0")" && pwd)
SUDO=sudo
[ -n "${SUDO_ASKPASS:-}" ] && SUDO="sudo -A"
STACKS="vault status blog shop media metrics"

# name:address:port of each published app, for the checks
APPS="vault:198.51.100.10:80 status:203.0.113.10:3001 blog:203.0.113.42:80 shop:203.0.113.74:80 sonarr:203.0.113.106:8989 qbittorrent:203.0.113.107:8080 metrics:203.0.113.138:3000"

# fw add|remove INTERFACE puts an interface in firewalld's trusted zone, or takes it out, for the running firewall. firewalld often makes the change and
# then does not answer in time (a D-Bus "did not receive a reply"), so its answer is not believed: the zone is asked, before and after, and the change
# is tried again if the zone does not read as wanted. Each call has a time limit, because firewalld can also stop answering altogether. It fails, saying what to run, if the zone never does; `down` goes on regardless.
fw() {
	command -v firewall-cmd >/dev/null 2>&1 && systemctl is-active --quiet firewalld 2>/dev/null || return 0
	want=yes
	[ "$1" = remove ] && want=no
	n=0
	while [ "$n" -lt 4 ]; do
		[ "$(timeout 30 firewall-cmd --zone=trusted --query-interface="$2" 2>/dev/null || true)" = "$want" ] && return 0
		$SUDO timeout 60 firewall-cmd --zone=trusted --"$1"-interface="$2" >/dev/null 2>&1 || true
		n=$((n + 1))
	done
	[ "$(timeout 30 firewall-cmd --zone=trusted --query-interface="$2" 2>/dev/null || true)" = "$want" ] && return 0
	echo "herd.sh: firewalld's trusted zone does not read as wanted for $2 after 4 tries; run: sudo firewall-cmd --zone=trusted --$1-interface=$2" >&2
	return 1
}

exists() { "$@" >/dev/null 2>&1; }

files() {
	f="$HERE/site.yaml"
	for s in $STACKS; do f="$f $HERE/$s.yaml"; done
	echo "$f"
}

up() {
	fw add herd0
	fw add 'tink-+'
	"$TINK" plan apply "$HERE/site.yaml"
	for s in $STACKS; do "$TINK" plan apply "$HERE/$s.yaml"; done
	# the site's proxy, sign-on and dns files hold what the stacks publish, read from the server, so the site is applied again once they are
	"$TINK" plan apply "$HERE/site.yaml"
}

ok() { printf '  ok    %s\n' "$1"; }
bad() { printf '  FAIL  %s\n' "$1"; fail=1; }

# test: the herd's checks, by hand until the derive stage and a heartbeat exist.
test() {
	fail=0
	J=$(mktemp)
	R="--resolve auth.home.example.org:443:127.0.0.1"
	for a in $APPS; do R="$R --resolve ${a%%:*}.home.example.org:443:127.0.0.1"; done
	curl -sk -m 10 $R -c "$J" -H 'Content-Type: application/json' \
		-d '{"username":"tester","password":"herd-test-password","keepMeLoggedIn":false}' https://auth.home.example.org/api/firstfactor >/dev/null

	for a in $APPS; do
		name=${a%%:*}
		rest=${a#*:}
		addr=${rest%%:*}
		url="https://$name.home.example.org/"
		echo "$name"
		[ "$(dig +short +time=3 +tries=1 @192.0.2.10 "$name.home.example.org")" = 192.0.2.3 ] && ok "the name answers" || bad "the name answers"
		out=$(curl -sk -m 15 $R -o /dev/null -w '%{http_code} %{redirect_url}' "$url")
		case "$out" in "302 https://auth.home.example.org"*) ok "no session: sent to sign-on" ;; *) bad "no session: sent to sign-on ($out)" ;; esac
		out=$(curl -sk -m 20 $R -b "$J" -o /dev/null -w '%{http_code} %{redirect_url}' "$url")
		case "$out" in
		"200 "* | "30"[1237]" "*)
			case "$out" in *auth.home.example.org*) bad "signed in: sent to sign-on again ($out)" ;; *) ok "signed in: the app answers (${out%% *})" ;; esac ;;
		*) bad "signed in: the app answers ($out)" ;;
		esac
		code=$(curl -s -m 4 -o /dev/null -w '%{http_code}' "http://$addr:${rest#*:}/" || true)
		[ "$code" = 000 ] && ok "not reachable from the host directly" || bad "the app answered the host ($code)"
	done

	echo "the stacks"
	if incus exec --project herd-site sso -- wget -q -O /dev/null -T 4 http://198.51.100.10/ </dev/null 2>/dev/null; then bad "another platform instance reached an app"; else ok "another platform instance does not reach an app"; fi
	for p in herd-blog herd-shop; do
		[ "$(incus list --project $p db -c s -f csv </dev/null)" = RUNNING ] && ok "$p: db is running" || bad "$p: db is running"
	done
	incus exec --project herd-media sonarr -- sh -c 'echo shared > /downloads/herd-check' </dev/null 2>/dev/null &&
		[ "$(incus exec --project herd-media qbittorrent -- cat /downloads/herd-check </dev/null 2>/dev/null)" = shared ] &&
		ok "media: the two apps share /downloads" || bad "media: the two apps share /downloads"
	rm -f "$J"
	return $fail
}

# backup: the vault stack's volume declares snapshots, a copy to another pool on this server and a restore check. Takes a snapshot by hand (the
# schedule would), makes the copy, and restores both the snapshot and the copy to a scratch volume to run the check against them.
backup() {
	v=vaultwarden-vw-data
	incus storage volume snapshot create default "$v" herd-snap --project herd-vault </dev/null
	"$TINK" backup run "$v" -f "$HERE/vault.yaml"
	"$TINK" backup verify "$v" -f "$HERE/vault.yaml" --snapshot herd-snap
	"$TINK" backup verify "$v" -f "$HERE/vault.yaml" --from vmpool
}

down() {
	for p in $(for s in $STACKS; do echo "herd-$s"; done) herd-site; do
		exists incus project show "$p" || continue
		for i in $(incus list --project "$p" -c n -f csv </dev/null); do incus delete --project "$p" "$i" -f </dev/null; done
		for pool in $(incus storage list -f csv -c n </dev/null); do
			for v in $(incus storage volume list "$pool" --project "$p" -c n -f csv </dev/null 2>/dev/null); do
				incus storage volume delete "$pool" "$v" --project "$p" </dev/null >/dev/null 2>&1 || true
			done
		done
		for f in $(incus image list --project "$p" -c f -f csv </dev/null); do incus image delete --project "$p" "$f" </dev/null; done
		incus project delete "$p" </dev/null
	done
	for s in $STACKS; do
		exists incus network show "tink-$s" && incus network delete "tink-$s" </dev/null
		exists incus network acl show "tink-$s-in" && incus network acl delete "tink-$s-in" </dev/null
	done
	exists incus network show herd0 && incus network delete herd0 </dev/null
	fw remove herd0 || true
	fw remove 'tink-+' || true
	return 0
}

case "${1:-}" in
up) up ;;
test) test ;;
backup) backup ;;
down) down ;;
*) echo "usage: TINK=./tink $0 up|test|backup|down" >&2; exit 2 ;;
esac
