#!/bin/sh
# Brings the herd test's fixture site and its stacks up on a local Incus server, checks them, and takes them down. See README.md.
#
#   TINK=./tink ./herd.sh up      # the site (dns, sign-on, ingress), the six stacks, then the site again
#   ./herd.sh test                # the checks of README.md, against what is up
#   TINK=./tink ./herd.sh backup  # a copy of the vault stack's volume to another pool, and a restore check of the snapshot and of the copy
#   ./herd.sh down                # removes everything it made
#   ./herd.sh preflight           # what `up` checks first: the tools, that a container starts, and a warning for br_netfilter (changes nothing)
#
# A stack set is a directory with a set.sh (see ../smarthome/set.sh): its stacks go onto the same site. HERD_SET=DIR chooses one; with none, the first herd
# (the six stacks of this directory) is the set. `up` and `test` act on the chosen set and on the site; `down` with a set removes that set's stacks and leaves
# the site and the other sets, and `down` with none removes everything this script and the sets beside it made.
#
# It touches the host in one place only, the firewall: on a host running firewalld it puts the platform bridge and every bridge named tink-*
# in the trusted zone of the running firewall (not permanently), so that a new bridge gets DHCP and DNS from the host. Nothing else outside Incus.
set -eu

TINK=${TINK:-tink}
HERE=$(cd "$(dirname "$0")" && pwd)
SUDO=sudo
[ -n "${SUDO_ASKPASS:-}" ] && SUDO="sudo -A"

# The stack set this run acts on. A set says: STACKS (the names of its stack files), SET_DIR (where they are), APPS (name:project:instance:port of each
# published app, for the checks; the address is read from the server, addr_of, because tink picks it) and set_checks (the checks of the set's stacks, run after
# the per-name ones). The first herd is the default set and is defined here.
SET=
if [ -n "${HERD_SET:-}" ]; then
	SET=$(cd "$HERD_SET" 2>/dev/null && pwd) || { echo "herd.sh: HERD_SET='$HERD_SET' is not a directory" >&2; exit 2; }
	[ -f "$SET/set.sh" ] || { echo "herd.sh: $SET has no set.sh" >&2; exit 2; }
	SET_DIR=$SET
	STACKS=
	APPS=
	set_checks() { :; }
	. "$SET/set.sh"
else
	SET_DIR=$HERE
	STACKS="vault status blog shop media metrics"
	APPS="vault:herd-vault:vaultwarden:80 status:herd-status:uptime-kuma:3001 blog:herd-blog:wordpress:80 shop:herd-shop:wordpress:80 sonarr:herd-media:sonarr:8989 qbittorrent:herd-media:qbittorrent:8080 metrics:herd-metrics:grafana:3000"
	set_checks() { first_herd_checks; }
fi

# The platform bridge is picked by Incus, so no address of it is in the repository: the helpers' addresses are placeholders (@DNS_IP@, @SSO_IP@,
# @INGRESS_IP@) in site.yaml and files/, filled in by render() from the subnet the bridge was given and written to .rendered/ (gitignored).
RENDERED=$HERE/.rendered

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

# The checks talk to this host's own bridges and loopback, never through an HTTP proxy that the environment may set ($HTTPS_PROXY would send the
# https:// requests to the proxy, and make "not reachable from the host" say 000 for the wrong reason).
curl() { command curl --noproxy '*' "$@"; }

# ensure_bridge creates the platform bridge with a subnet of Incus's choosing, if there is none, so that its subnet can be read before tink applies the site
# (site.yaml says `ipv4.address: auto`, which tink treats as satisfied by whatever Incus chose).
ensure_bridge() {
	exists incus network show herd0 && return 0
	incus network create herd0 ipv4.address=auto ipv4.nat=true ipv6.address=none </dev/null
}

# platform sets DNS_IP, SSO_IP and INGRESS_IP from the platform bridge's subnet: the .10, .11 and .3 of it. Incus picks a /24.
platform() {
	cidr=$(incus network get herd0 ipv4.address </dev/null 2>/dev/null) || cidr=
	case "$cidr" in
	*.*.*.1/24) ;;
	*) echo "herd.sh: the platform bridge herd0 has no /24 IPv4 address ('$cidr'); run 'up' first" >&2; return 1 ;;
	esac
	net=${cidr%.*}
	DNS_IP=$net.10
	SSO_IP=$net.11
	INGRESS_IP=$net.3
}

# render writes site.yaml and files/ to .rendered/ with the placeholders filled in.
render() {
	platform
	rm -rf "$RENDERED"
	mkdir -p "$RENDERED/files"
	for f in site.yaml $(cd "$HERE" && echo files/*); do
		sed -e "s|@DNS_IP@|$DNS_IP|g" -e "s|@SSO_IP@|$SSO_IP|g" -e "s|@INGRESS_IP@|$INGRESS_IP|g" "$HERE/$f" >"$RENDERED/$f"
	done
	# a set's own yaml and files get the same placeholders (Home Assistant has to be told which proxy to trust), and its stacks are applied from there
	if [ -n "$SET" ]; then
		mkdir -p "$RENDERED/set/files"
		for f in $(cd "$SET" && echo *.yaml files/*); do
			[ -f "$SET/$f" ] || continue
			sed -e "s|@DNS_IP@|$DNS_IP|g" -e "s|@SSO_IP@|$SSO_IP|g" -e "s|@INGRESS_IP@|$INGRESS_IP|g" "$SET/$f" >"$RENDERED/set/$f"
		done
		SET_DIR=$RENDERED/set
	fi
}

# addr_of PROJECT INSTANCE prints the instance's IPv4 address.
addr_of() {
	incus list --project "$1" "$2" -c 4 -f csv </dev/null | sed -n '1s/ .*//p'
}

files() {
	f="$HERE/site.yaml"
	for s in $STACKS; do f="$f $SET_DIR/$s.yaml"; done
	echo "$f"
}

# drop_probe NAME deletes the preflight's probe container. A launch that failed can still be finishing when this is called, so a refusal is tried again
# a few times; if it will not go, say so, so that nothing is left behind unnoticed.
drop_probe() {
	exists incus info "$1" </dev/null || return 0
	n=0
	while [ "$n" -lt 5 ]; do
		timeout 60 incus delete -f "$1" </dev/null >/dev/null 2>&1 && return 0
		n=$((n + 1))
		sleep 2
	done
	echo "herd.sh: preflight: could not delete the probe container $1 (a stop that hangs?); delete it by hand: incus delete -f $1" >&2
}

# preflight says what is missing on this host before `up` spends minutes finding out, and changes nothing but a probe container it deletes. It fails
# on what cannot work (a missing tool, a container that does not start) and only warns on what some stacks trip over (#196). HERD_PREFLIGHT=0 skips it.
preflight() {
	bad=0
	for c in incus curl dig timeout sed; do
		command -v "$c" >/dev/null 2>&1 || { echo "herd.sh: preflight: '$c' is not installed" >&2; bad=1; }
	done
	command -v "$TINK" >/dev/null 2>&1 || { echo "herd.sh: preflight: TINK='$TINK' is not a program (build it: go build -o tink ./cmd/tink; run with TINK=./tink)" >&2; bad=1; }
	[ "$bad" = 0 ] && ! incus info >/dev/null 2>&1 </dev/null && { echo "herd.sh: preflight: the Incus server does not answer (incus info)" >&2; bad=1; }
	[ "$bad" = 0 ] || return 1
	# A container has to start: the probe is a small image, and its cleanup is bounded because a stop can hang on a host whose cgroup layout LXC does not support.
	probe=herd-preflight-$$
	if ! out=$(timeout 180 incus launch images:alpine/3.21 "$probe" </dev/null 2>&1); then
		drop_probe "$probe"
		echo "herd.sh: preflight: a container does not start on this server: $(echo "$out" | tail -n 1)" >&2
		echo "herd.sh: preflight: check the image server is reachable and that LXC supports this host's cgroup layout (it needs a pure cgroup v2 host)" >&2
		return 1
	fi
	drop_probe "$probe"
	if [ "$(cat /proc/sys/net/bridge/bridge-nf-call-iptables 2>/dev/null || echo 0)" = 1 ]; then
		echo "herd.sh: preflight: warning: net.bridge.bridge-nf-call-iptables is 1, so the ACL of a stack also applies between its own apps: blog and shop will answer 500 until this is 0 or #196 is fixed" >&2
	fi
	echo "preflight: ok"
}

# apply FILE is `tink plan apply FILE`, tried again after a pause when the registry's anonymous pull limit refused an image (it comes and goes). Any other
# failure stops at once. An apply that failed part-way is resumed by the next one. HERD_PULL_RETRIES (default 3) and HERD_RETRY_WAIT (default 30 s) set it.
apply() {
	tries=0
	while :; do
		log=$(mktemp)
		# set -e is in force in the pipeline's subshell too, so a failing tink must not be a command of its own: it is the left side of `||`, which records its status
		{ "$TINK" plan apply "$1" 2>&1 || echo $? >"$log.rc"; } | tee "$log"
		rc=$(cat "$log.rc" 2>/dev/null || echo 0)
		limited=no
		grep -q toomanyrequests "$log" && limited=yes
		rm -f "$log" "$log.rc"
		[ "$rc" = 0 ] && return 0
		[ "$limited" = yes ] && [ "$tries" -lt "${HERD_PULL_RETRIES:-3}" ] || return "$rc"
		tries=$((tries + 1))
		echo "herd.sh: the registry refused a pull (rate limit); trying $1 again in ${HERD_RETRY_WAIT:-30} s ($tries of ${HERD_PULL_RETRIES:-3})" >&2
		sleep "${HERD_RETRY_WAIT:-30}"
	done
}

up() {
	[ "${HERD_PREFLIGHT:-1}" = 0 ] || preflight
	fw add herd0
	fw add 'tink-+'
	ensure_bridge
	render
	# A site that is already there is not applied first: its proxy, sign-on and dns files hold the names of stacks that may have been taken down since
	# (`down` with a set leaves them), and tink refuses to empty a derived file, so applying it with fewer stacks than it remembers is blocked. The stacks
	# come first, and the site follows them below.
	exists incus project show herd-site || apply "$RENDERED/site.yaml"
	for s in $STACKS; do apply "$SET_DIR/$s.yaml"; done
	# the site's proxy, sign-on and dns files hold what the stacks publish, read from the server, so the site is applied again once they are
	apply "$RENDERED/site.yaml"
}

ok() { printf '  ok    %s\n' "$1"; }
bad() { printf '  FAIL  %s\n' "$1"; fail=1; }

# set_checks of the first herd: what is true of its stacks beyond their names.
first_herd_checks() {
	# wget's -T does not bound the connect to an address whose ACL drops packets (it waited out the kernel's retries, about five minutes), so the call has its own limit
	if timeout 15 incus exec --project herd-site sso -- wget -q -O /dev/null -T 4 "http://$(addr_of herd-vault vaultwarden)/" </dev/null 2>/dev/null; then bad "another platform instance reached an app"; else ok "another platform instance does not reach an app"; fi
	for p in herd-blog herd-shop; do
		[ "$(incus list --project $p db -c s -f csv </dev/null)" = RUNNING ] && ok "$p: db is running" || bad "$p: db is running"
	done
	incus exec --project herd-media sonarr -- sh -c 'echo shared > /downloads/herd-check' </dev/null 2>/dev/null &&
		[ "$(incus exec --project herd-media qbittorrent -- cat /downloads/herd-check </dev/null 2>/dev/null)" = shared ] &&
		ok "media: the two apps share /downloads" || bad "media: the two apps share /downloads"
}

# test: the herd's checks, by hand until the derive stage and a heartbeat exist.
test() {
	fail=0
	platform
	J=$(mktemp)
	R="--resolve auth.home.example.org:443:127.0.0.1"
	for a in $APPS; do R="$R --resolve ${a%%:*}.home.example.org:443:127.0.0.1"; done
	# `up` ends as soon as the sign-on helper is restarted for its derived rules, and it takes several seconds to listen again: wait for it, up to a minute
	n=0
	while [ "$n" -lt 30 ]; do
		case "$(curl -sk -m 5 $R -o /dev/null -w '%{http_code}' https://auth.home.example.org/ || true)" in 2* | 3*) break ;; esac
		n=$((n + 1))
		sleep 2
	done
	curl -sk -m 10 $R -c "$J" -H 'Content-Type: application/json' \
		-d '{"username":"tester","password":"herd-test-password","keepMeLoggedIn":false}' https://auth.home.example.org/api/firstfactor >/dev/null || bad "signed in as tester"

	for a in $APPS; do
		name=${a%%:*}
		rest=${a#*:}
		project=${rest%%:*}
		rest=${rest#*:}
		inst=${rest%%:*}
		port=${rest#*:}
		addr=$(addr_of "$project" "$inst")
		url="https://$name.home.example.org/"
		echo "$name"
		[ "$(dig +short +time=3 +tries=1 @"$DNS_IP" "$name.home.example.org")" = "$INGRESS_IP" ] && ok "the name answers" || bad "the name answers"
		out=$(curl -sk -m 15 $R -o /dev/null -w '%{http_code} %{redirect_url}' "$url")
		case "$out" in "302 https://auth.home.example.org"*) ok "no session: sent to sign-on" ;; *) bad "no session: sent to sign-on ($out)" ;; esac
		out=$(curl -sk -m 20 $R -b "$J" -o /dev/null -w '%{http_code} %{redirect_url}' "$url")
		case "$out" in
		"200 "* | "30"[1237]" "*)
			case "$out" in *auth.home.example.org*) bad "signed in: sent to sign-on again ($out)" ;; *) ok "signed in: the app answers (${out%% *})" ;; esac ;;
		*) bad "signed in: the app answers ($out)" ;;
		esac
		[ -n "$addr" ] || bad "$name has no address on the server"
		code=$(curl -s -m 4 -o /dev/null -w '%{http_code}' "http://$addr:$port/" || true)
		[ "$code" = 000 ] && ok "not reachable from the host directly" || bad "the app answered the host ($code)"
	done

	echo "the stacks"
	set_checks
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

# stacks_of DIR prints the stack names of the set in DIR (read in a subshell, so its functions and variables do not reach this one).
stacks_of() { ( STACKS=; . "$1/set.sh"; echo "$STACKS" ); }

# down_stacks NAMES removes the projects, networks and ACLs of those stacks.
down_stacks() {
	for p in $(for s in $1; do echo "herd-$s"; done); do
		exists incus project show "$p" || continue
		# a delete is refused now and then while a stop is still ending ("Instance is running"), so it is tried once more
		for i in $(incus list --project "$p" -c n -f csv </dev/null); do incus delete --project "$p" "$i" -f </dev/null || { sleep 5; incus delete --project "$p" "$i" -f </dev/null; }; done
		for pool in $(incus storage list -f csv -c n </dev/null); do
			for v in $(incus storage volume list "$pool" --project "$p" -c n -f csv </dev/null 2>/dev/null); do
				incus storage volume delete "$pool" "$v" --project "$p" </dev/null >/dev/null 2>&1 || true
			done
		done
		for f in $(incus image list --project "$p" -c f -f csv </dev/null); do incus image delete --project "$p" "$f" </dev/null; done
		incus project delete "$p" </dev/null
	done
	for s in $1; do
		exists incus network show "tink-$s" && incus network delete "tink-$s" </dev/null
		exists incus network acl show "tink-$s-in" && incus network acl delete "tink-$s-in" </dev/null
	done
}

# down with a set removes that set's stacks and nothing else, so the site and the other sets stay. Without one it removes the first herd, the stacks of
# every set in a directory beside this one, and the site.
down() {
	if [ -n "$SET" ]; then
		down_stacks "$STACKS"
		return 0
	fi
	all=$STACKS
	for d in "$HERE"/../*/; do
		[ -f "$d/set.sh" ] && all="$all $(stacks_of "$d")"
	done
	# the site is the project herd-site, so it goes the way a stack does (it has no tink-site network, which down_stacks skips)
	down_stacks "$all site"
	exists incus network show herd0 && incus network delete herd0 </dev/null
	rm -rf "$RENDERED"
	fw remove herd0 || true
	fw remove 'tink-+' || true
	return 0
}

case "${1:-}" in
up) up ;;
test) test ;;
backup) backup ;;
preflight) preflight ;;
down) down ;;
*) echo "usage: TINK=./tink $0 up|test|backup|down|preflight" >&2; exit 2 ;;
esac
