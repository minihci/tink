#!/bin/bash
# Start the Incus daemon in Claude Code cloud sessions. The environment's setup
# script installs Incus, but a running daemon doesn't survive the cached snapshot.
[ "$CLAUDE_CODE_REMOTE" = "true" ] || exit 0
pgrep -x incusd >/dev/null && exit 0
mkdir -p /var/lib/incus /run/incus
nohup /usr/libexec/incus/incusd --group root >/tmp/incusd.log 2>&1 &
for _ in $(seq 1 30); do
  incus info >/dev/null 2>&1 && exit 0
  sleep 1
done
exit 0
