#!/bin/bash
# Started by start_incus.sh in Claude Code cloud sessions (see the comment there).
# With this sandbox's hybrid cgroup layout, LXC 7 neither unfreezes nor kills a container on stop, so a stop never ends and `incus delete -f`
# hangs. Thaw anything frozen, and SIGKILL the init of an instance whose stop is still running after one more pass (about three seconds).
seen=
while :; do
	for f in /sys/fs/cgroup/freezer/lxc.payload.*/freezer.state; do
		[ -e "$f" ] && [ "$(cat "$f" 2>/dev/null)" = FROZEN ] && echo THAWED >"$f" 2>/dev/null
	done
	now=
	for r in $(incus operation list --all-projects -f json 2>/dev/null | jq -r '.[] | select(.description=="Stopping instance" and .status=="Running") | .resources.instances[0]' 2>/dev/null); do
		now="$now $r"
		case " $seen " in
		*" $r "*)
			name=${r#/1.0/instances/}
			name=${name%%\?*}
			proj=${r##*project=}
			pid=$(incus info "$name" --project "$proj" 2>/dev/null | sed -n 's/^PID: //p')
			[ -n "$pid" ] && [ "$pid" != 0 ] && kill -9 "$pid" 2>/dev/null
			;;
		esac
	done
	seen=$now
	sleep 3
done
