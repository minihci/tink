#!/bin/bash
# Started by start_incus.sh in Claude Code cloud sessions (see the comment there).
# With this sandbox's hybrid cgroup layout, LXC 7 neither unfreezes nor kills a container on stop, so a stop never ends and `incus delete -f`
# hangs. Thaw anything frozen, and SIGKILL the init of an instance whose stop has been running for five passes in a row (about fifteen seconds): a
# stop that ends is left alone, because killing an app mid-way through a graceful stop makes Incus report the instance as still running.
h1= h2= h3= h4=
while :; do
	for f in /sys/fs/cgroup/freezer/lxc.payload.*/freezer.state; do
		[ -e "$f" ] && [ "$(cat "$f" 2>/dev/null)" = FROZEN ] && echo THAWED >"$f" 2>/dev/null
	done
	now=
	for r in $(incus operation list --all-projects -f json 2>/dev/null | jq -r '.[] | select(.description=="Stopping instance" and .status=="Running") | .resources.instances[0]' 2>/dev/null); do
		now="$now $r"
		case " $h1 | $h2 | $h3 | $h4 " in
		*" $r "*" | "*" $r "*" | "*" $r "*" | "*" $r "*)
			name=${r#/1.0/instances/}
			name=${name%%\?*}
			proj=default
			case "$r" in *project=*) proj=${r##*project=} ;; esac
			pid=$(incus info "$name" --project "$proj" 2>/dev/null | sed -n 's/^PID: //p')
			[ -n "$pid" ] && [ "$pid" != 0 ] && kill -9 "$pid" 2>/dev/null
			;;
		esac
	done
	h4=$h3 h3=$h2 h2=$h1 h1=$now
	sleep 3
done
