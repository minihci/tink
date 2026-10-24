# The smart-home herd: a stack set for ../site/herd.sh (HERD_SET=corpus/smarthome). See README.md.
# This file is read by herd.sh and may use its functions (ok, bad, addr_of).

STACKS="home"

APPS="ha:herd-home:homeassistant:8123 nodered:herd-home:node-red:1880"

# reach PROJECT INSTANCE ADDRESS PORT succeeds when INSTANCE can open a TCP connection to ADDRESS:PORT. busybox nc exits 0 on a connection and 1 on a
# timeout; the call has its own limit because an address whose ACL drops packets is waited out by the kernel otherwise.
reach() { timeout 25 incus exec --project "$1" "$2" -- sh -c "nc -w 3 $3 $4 </dev/null >/dev/null 2>&1" </dev/null 2>/dev/null; }

# reach_node ADDRESS PORT is reach for Node-RED's image, which has node and no nc.
reach_node() {
	timeout 25 incus exec --project herd-home node-red -- node -e "setTimeout(()=>process.exit(1),4000);require('net').connect($2,'$1').on('connect',()=>process.exit(0)).on('error',()=>process.exit(1))" </dev/null >/dev/null 2>&1
}

# describe ADDRESS PORT asks a Wyoming server for its info from inside Home Assistant (its image has python3), as its Wyoming integration would.
describe() {
	timeout 40 incus exec --project herd-home homeassistant -- python3 -c '
import socket, sys, json
s = socket.create_connection((sys.argv[1], int(sys.argv[2])), 5)
s.settimeout(10)
s.sendall(b"{\"type\":\"describe\"}\n")
b = b""
while b"\n" not in b:
    d = s.recv(4096)
    if not d:
        break
    b += d
sys.exit(0 if json.loads(b.split(b"\n")[0]).get("type") == "info" else 1)
' "$1" "$2" </dev/null >/dev/null 2>&1
}

set_checks() {
	broker=$(addr_of herd-home mosquitto)
	ha=$(addr_of herd-home homeassistant)
	db=$(addr_of herd-home mariadb)
	matter=$(addr_of herd-home matter-server)
	for a in "$broker" "$ha" "$db" "$matter"; do
		[ -n "$a" ] || { bad "home: every instance has an address"; return 0; }
	done
	reach herd-site ingress "$ha" 8123 && ok "home: the ingress helper reaches the port it fronts (the control for the denials below)" || bad "home: the ingress helper reaches the port it fronts"

	reach herd-home homeassistant "$broker" 1883 && ok "home: the broker answers from Home Assistant" || bad "home: the broker answers from Home Assistant"
	reach_node "$broker" 1883 && ok "home: the broker answers from Node-RED" || bad "home: the broker answers from Node-RED"
	reach herd-home homeassistant "$db" 3306 && ok "home: the recorder database answers from Home Assistant" || bad "home: the recorder database answers from Home Assistant"
	reach herd-home homeassistant "$matter" 5580 && ok "home: the Matter server's web socket answers from Home Assistant" || bad "home: the Matter server's web socket answers from Home Assistant"
	for pair in piper:10200 whisper:10300 openwakeword:10400; do
		name=${pair%%:*}
		addr=$(addr_of herd-home "$name")
		[ -n "$addr" ] && describe "$addr" "${pair#*:}" && ok "home: $name answers a Wyoming describe from Home Assistant" || bad "home: $name answers a Wyoming describe from Home Assistant"
	done

	for target in "mosquitto:$broker:1883" "mariadb:$db:3306" "matter-server:$matter:5580"; do
		name=${target%%:*}
		rest=${target#*:}
		for from in ingress sso; do
			reach herd-site "$from" "${rest%%:*}" "${rest#*:}" && bad "home: the platform's $from helper reached $name" || ok "home: the platform's $from helper does not reach $name"
		done
	done
}
