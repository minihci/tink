# The smart-home herd: a stack set for ../site/herd.sh (HERD_SET=corpus/smarthome). See README.md.
# This file is read by herd.sh and may use its functions (ok, bad, addr_of).

STACKS="home"

APPS="ha:herd-home:homeassistant:8123"

# reach PROJECT INSTANCE ADDRESS PORT succeeds when INSTANCE can open a TCP connection to ADDRESS:PORT. busybox nc exits 0 on a connection and 1 on a
# timeout; the call has its own limit because an address whose ACL drops packets is waited out by the kernel otherwise.
reach() { timeout 25 incus exec --project "$1" "$2" -- sh -c "nc -w 3 $3 $4 </dev/null >/dev/null 2>&1" </dev/null 2>/dev/null; }

set_checks() {
	broker=$(addr_of herd-home mosquitto)
	ha=$(addr_of herd-home homeassistant)
	[ -n "$broker" ] && [ -n "$ha" ] || { bad "home: the broker and Home Assistant have addresses"; return 0; }
	reach herd-site ingress "$ha" 8123 && ok "home: the ingress helper reaches the port it fronts (the control for the two below)" || bad "home: the ingress helper reaches the port it fronts"
	reach herd-home homeassistant "$broker" 1883 && ok "home: the broker answers from Home Assistant" || bad "home: the broker answers from Home Assistant"
	reach herd-site ingress "$broker" 1883 && bad "home: the ingress helper reached the broker" || ok "home: the ingress helper does not reach the broker"
	reach herd-site sso "$broker" 1883 && bad "home: another platform instance reached the broker" || ok "home: another platform instance does not reach the broker"
}
