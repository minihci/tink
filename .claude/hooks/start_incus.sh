#!/bin/bash
# Start the Incus daemon in Claude Code cloud sessions. The environment's setup
# script installs Incus, but a running daemon doesn't survive the cached snapshot.
[ "$CLAUDE_CODE_REMOTE" = "true" ] || exit 0
if ! pgrep -x incusd >/dev/null; then
  mkdir -p /var/lib/incus /run/incus
  nohup /opt/incus/lib/systemd/incusd --group root >/tmp/incusd.log 2>&1 &
fi
for _ in $(seq 1 30); do
  incus info >/dev/null 2>&1 && break
  sleep 1
done
# First session on a fresh VM: create the default storage pool, bridge and profile.
incus info >/dev/null 2>&1 && ! incus storage show default >/dev/null 2>&1 && incus admin init --minimal

# Workarounds for this sandbox's kernel and cgroup setup. They cannot be fixed from inside (the hybrid cgroup v1/v2 layout is chosen at boot), and
# LXC 7 (Incus 7) supports only a pure cgroup v2 host, so on this layout:
#  - a new cpuset cgroup starts empty and a process cannot enter it (ENOSPC on start): have children inherit their parent's CPUs and memory nodes;
#  - LXC cannot mount the container's /sys/fs/cgroup ("unsupported cgroup layout"): ask for a cgroup2 mount, which it does accept on a hybrid host;
#  - a stop never ends, because the freezer is not thawed and the init not killed: incus_reaper.sh does both for a stop that hangs.
# Gated on the hybrid layout, so a sandbox that gets a pure v2 layout skips all three.
if incus info >/dev/null 2>&1 && [ -d /sys/fs/cgroup/unified ] && [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
  [ -e /sys/fs/cgroup/cpuset/cgroup.clone_children ] && echo 1 >/sys/fs/cgroup/cpuset/cgroup.clone_children
  [ "$(incus profile get default raw.lxc 2>/dev/null)" = "lxc.mount.auto=cgroup2" ] || incus profile set default raw.lxc=lxc.mount.auto=cgroup2
  pgrep -f '[i]ncus_reaper.sh' >/dev/null || setsid nohup bash "$(dirname "$0")/incus_reaper.sh" >/tmp/incus-reaper.log 2>&1 </dev/null &
fi
# Bridged traffic is passed through netfilter here (net.bridge.bridge-nf-call-iptables=1), which makes the ACL tink derives for `network: own`
# reject traffic between the apps of one stack (a WordPress cannot reach its own database). Turn it off until tink allows a stack's own subnet (minihci/tink#196).
for k in iptables ip6tables; do
  [ -e /proc/sys/net/bridge/bridge-nf-call-$k ] && echo 0 >/proc/sys/net/bridge/bridge-nf-call-$k
done
exit 0
