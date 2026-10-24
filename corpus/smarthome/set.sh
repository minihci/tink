# The smart-home herd: a stack set for ../site/herd.sh (HERD_SET=corpus/smarthome). See README.md.
# This file is read by herd.sh and may use its functions (ok, bad, addr_of).

STACKS="home esphome music"

# declared and planned, never applied: they need a radio this host has not (README.md)
PLAN_ONLY="radios"

APPS="ha:herd-home:homeassistant:8123 nodered:herd-home:node-red:1880 esphome:herd-esphome:esphome:6052 music:herd-music:music-assistant:8095"

# reach PROJECT INSTANCE ADDRESS PORT succeeds when INSTANCE can open a TCP connection to ADDRESS:PORT. busybox nc exits 0 on a connection and 1 on a
# timeout; the call has its own limit because an address whose ACL drops packets is waited out by the kernel otherwise.
reach() { timeout 25 incus exec --project "$1" "$2" -- sh -c "nc -w 3 $3 $4 </dev/null >/dev/null 2>&1" </dev/null 2>/dev/null; }

# reach_py PROJECT INSTANCE ADDRESS PORT is reach for an image that has python3 and no nc (ESPHome, Music Assistant).
reach_py() {
	timeout 25 incus exec --project "$1" "$2" -- python3 -c "import socket,sys; socket.create_connection((sys.argv[1],int(sys.argv[2])),4)" "$3" "$4" </dev/null >/dev/null 2>&1
}

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

	# ESPHome and Music Assistant cannot reach the home stack's broker. Both stacks say egress: open, so their traffic can leave, and the control shows it: each
	# reaches the platform's dns helper, on a bridge with no ACL. The denial is then the home stack's ACL.
	for pair in esphome:herd-esphome:esphome music-assistant:herd-music:music-assistant; do
		name=${pair%%:*}
		rest=${pair#*:}
		reach_py "${rest%%:*}" "${rest#*:}" "$DNS_IP" 53 && ok "stacks: $name reaches the platform's dns helper (the control: egress is open)" || bad "stacks: $name reaches the platform's dns helper (the control: egress is open)"
	done
	for pair in esphome:herd-esphome:esphome music-assistant:herd-music:music-assistant; do
		name=${pair%%:*}
		rest=${pair#*:}
		reach_py "${rest%%:*}" "${rest#*:}" "$broker" 1883 && bad "stacks: $name reached the home stack's broker" || ok "stacks: $name does not reach the home stack's broker"
	done

	# radios is only planned; what the plan derives from its two publishes is checked here, because it is all the stack can show
	plan=$("$TINK" plan "$SET_DIR/radios.yaml" 2>&1) || plan=
	case "$plan" in *"ingress rule(s) derived from the publishes z2m, zwave"*) ok "radios: the plan derives the ACL rules for z2m and zwave" ;; *) bad "radios: the plan derives the ACL rules for z2m and zwave" ;; esac
	case "$plan" in *"zigbee2mqtt: address "*"zwave-js-ui: address "*) ok "radios: the plan allocates their addresses" ;; *) bad "radios: the plan allocates their addresses" ;; esac
}
