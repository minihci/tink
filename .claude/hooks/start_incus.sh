#!/bin/bash
# Start the Incus daemon in Claude Code cloud sessions. The environment's setup
# script installs Incus, but a running daemon doesn't survive the cached snapshot.
[ "$CLAUDE_CODE_REMOTE" = "true" ] || exit 0
if ! pgrep -x incusd >/dev/null; then
  mkdir -p /var/lib/incus /run/incus
  nohup /usr/libexec/incus/incusd --group root >/tmp/incusd.log 2>&1 &
fi
for _ in $(seq 1 30); do
  incus info >/dev/null 2>&1 && break
  sleep 1
done
# First session on a fresh VM: create the default storage pool, bridge and profile.
incus info >/dev/null 2>&1 && ! incus storage show default >/dev/null 2>&1 && incus admin init --minimal
exit 0
