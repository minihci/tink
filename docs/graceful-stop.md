# Graceful stop: how an app that must save is asked to, before Incus stops it

**Status: a design note, 2026-10-17. Nothing here is built.** The measurements are from a real Incus 7.5.1 host and the real Rust server image; the design is proposed. Claims are marked **[lab]**, **[code]** or **[proposed]**.

## The problem

`incus stop` is how every tink operation and every host shutdown ends an instance, and for an OCI app container it is not a graceful stop. An app that has to write something before it exits (a game server saving its world, a database flushing) loses that write, with no error anywhere. The Rust server is the case that found it; the same is true of anything with state in memory.

## What was measured [lab]

All on the VPS, Incus 7.5.1, 2026-10-17.

| Question | Seen |
|---|---|
| What does an OCI app container run as PID 1? | Incus's own `incusd` init. The app is a child (pid 23 in the test). |
| What does `incus stop` send the app, and for how long? | `SIGTERM`, then the container is torn down about **one second** later. A shell script that traps `TERM` and keeps running for five seconds was killed mid-trap, with `Terminated` in the console log. Delaying the trap by 1, 5 or 40 s made no difference, and neither did `incus stop --timeout 20`: the call returns after about 2 s either way. |
| Is there a setting for the signal or the grace time? | Not in 7.5.1. The server's own list of instance keys (`/1.0/metadata/configuration`) has `boot.host_shutdown_action`, `boot.host_shutdown_timeout` (default 30) and `boot.stop.priority`, and under `oci.` only `cwd`, `dns.*`, `entrypoint`, `gid` and `uid`. No stop signal and no per-app grace. |
| What does the Rust server do on `SIGTERM`, given time? | `kill -TERM` sent to `RustDedicated` inside the container: it shut down in about **4 s** ("EasyAntiCheat Server Shutting Down … Okay") and wrote **no save**: no `Saved N ents`, no `.sav` file. |
| What does RCON `quit` do? | It saves (`Saved 381 ents … Saving complete`, `proceduralmap.….sav` written) and the server exits by itself in about **2 s**. With `boot.autorestart: "false"` the instance then stays stopped. |
| What can run inside the Rust image? | `bash`, `perl`, `nc`, `openssl`. No `curl`, `wget`, `python`, `node` or `websocat`. A client for the server's web RCON (a WebSocket) is about fifteen lines of Perl and ran inside the container with `incus exec`. |
| Who stops the instances at host shutdown? | `incus-startup.service`: `ExecStop=/opt/incus/lib/systemd/incus-startup stop`, timeout 600 s. Once it has started, `incus exec` answers `Error: Incus is shutting down`. |
| Does an `After=incus-startup.service` unit run its `ExecStop` before that? | **No.** Stopped together with it, the journal showed `Stopping incus-startup.service` first, and the hook's `incus exec` was refused. |
| Does `Requires=incus-startup.service` plus `After=incus-startup.service`? | **Yes.** `systemctl stop incus-startup.service` stopped the hook unit first: the hook asked the server to quit at 21:24:55.29, the server saved and stopped itself by 21:24:57.5, and only then did `incus-startup` start stopping (done at 21:24:58.7). The world file had `Saved 381 ents`. |

So the app gets one second of warning, which is less than a save takes, and the setting that would give it more time does not exist. The one reliable moment to ask for a save is **before** Incus is asked to stop, and the only place to hook that for a host shutdown is outside Incus.

## What would not work

- **A wrapper entrypoint that traps `TERM` and then sends `quit`.** The container is torn down about a second after the signal, whatever the wrapper does; the trap tests above are exactly this.
- **Raising `boot.host_shutdown_timeout`.** It bounds how long Incus waits for the stop to complete, and the stop does not wait for the app.
- **A helper instance with a higher `boot.stop.priority` that asks the server to quit.** It would itself get one second, and Incus moves on to the next instance when it exits, not when the quit has finished.
- **Running Rust in a system container** (a real init, `SIGPWR`, a service with `ExecStop`). It would work, but the server image is an OCI image, and building and keeping a system image for one app gives up the reason to use the image.

## Design [proposed]

The shape follows the rest of tink: the **fact** about an instance (how to make it save) is stated with the instance and stamped on it; a verb reads the stamp; the host hook is a thin caller of that verb.

1. **A `stop` field on `kind: instance`**, stamped as `user.tink.stop` (compact JSON, like the publish stamps):

   ```yaml
   kind: instance
   name: rust
   stop:
     exec: [perl, /rcon.pl, quit]    # run inside the instance, with its environment (so $RCON_PASSWORD is there and is not in the stamp)
     timeout: 90s                   # how long to wait for the instance to stop by itself before a plain stop
   ```

   The command is a fact about the app, not a policy about the site, so it lives in the app's stack. The script it names is delivered by a `kind: file`, which tink already puts in place ([first-start-files-design.md](first-start-files-design.md)).

2. **A verb: `tink stop <instance>…` and `tink graceful-stop --all`.** For each instance that is running and has the stamp: run `exec`, then poll the instance's state until it is stopped or `timeout` passes, then fall back to the ordinary stop. Instances run in parallel, so several apps share one wait. An instance with no stamp gets the ordinary stop, as today. A failing or missing command is reported and does not block the fallback.

3. **The same verb is used by tink itself** wherever `plan apply` stops an instance on purpose: replacing one on an image change (`on_image_change`), or any key that needs a restart. Today those kill the app the same way.

4. **A host hook, installed by an explicit command** (`tink host install-stop-hook`, which prints the unit before writing it; tink does not change the host on its own):

   ```ini
   [Unit]
   Description=Ask tink-managed apps to save and stop before Incus stops instances
   Requires=incus-startup.service
   After=incus-startup.service
   [Service]
   Type=oneshot
   RemainAfterExit=yes
   ExecStart=/bin/true
   ExecStop=/usr/local/bin/tink graceful-stop --all
   TimeoutStopSec=300
   ```

   `Requires=` as well as `After=` is what makes it run first (measured above). It reads the stamps from the server, so it needs no stack files on the host.

5. **Backups can ask for the same quiesce.** A volume backup that wants a consistent copy of an app that has a `stop` could call it first; for Rust it is not needed (see below), so this is a note for [volume-backup-design.md](volume-backup-design.md), not part of this change.

## What it does not cover

- **Power loss, a crash, an OOM kill, or someone typing `incus stop` or `incus delete --force` by hand.** All of them kill the app. The Rust server keeps rolling saves (`proceduralmap.<size>.<seed>.<ver>.sav`, `.sav.1`, `.sav.2` about ten minutes apart) and its player data is SQLite with a write-ahead log, so a crash costs at most the time since the last save, and crash-consistent volume snapshots are fine **[lab, earlier the same day]**. `server.saveinterval` sets that bound.
- **An app that wedges.** `timeout` ends the wait, a plain stop follows, and the unit's `TimeoutStopSec` bounds the whole shutdown.
- **A host that does not run systemd,** or whose Incus is not started by `incus-startup.service`. The verb works anywhere; only the hook is systemd-shaped.

## Open questions

- **A field, or only a stamp?** A first-class `stop:` is checked at plan time (the command is a list, the timeout parses, a `kind: file` delivers the script it names). A bare `user.tink.stop` key would be free but unchecked. The first is the better fit.
- **Where the RCON client lives.** Fifteen lines of Perl in the stack's files is enough for the Rust image. Whether tink should ship one for Rust, or the app stack carries it, is the platform-recipe question again ([platform-recipe.md](platform-recipe.md)).
- **How `tink stop` interacts with `boot.autorestart`.** The measured quit leaves the instance stopped only with `boot.autorestart: "false"`; with it on, Incus restarts the server straight after a save. The verb should say so when it finds both.
- **Rust's own pre-restart warning to players** (`restart 60`-style countdowns) is a different feature: it is a command the operator chooses to run, not part of stopping.

## Prototype, kept for reference

The client used for the measurements (run with `incus exec <instance> -- perl /rcon.pl quit`, reading `RCON_PASSWORD` and optionally `RCON_PORT` from the instance's environment):

```perl
use strict; use IO::Socket::INET;
my ($port, $pw, $cmd) = ($ENV{RCON_PORT} || 28016, $ENV{RCON_PASSWORD}, $ARGV[0]);
my $s = IO::Socket::INET->new(PeerAddr => "127.0.0.1:$port", Timeout => 5) or die "connect: $!\n";
$s->autoflush(1);
print $s "GET /$pw HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n";
my $buf = ""; sysread($s, $buf, 4096);
die "handshake refused: " . substr($buf, 0, 40) . "\n" unless $buf =~ /^HTTP\/1\.1 101/;
my $msg = qq({"Identifier":7,"Message":"$cmd","Name":"tink"});
my @m = map { int(rand(256)) } 1 .. 4; my $i = 0; my $p = "";
$p .= chr($_ ^ $m[$i++ % 4]) for unpack("C*", $msg);
print $s chr(0x81) . chr(0x80 | length($msg)) . pack("C4", @m) . $p;
local $SIG{ALRM} = sub { exit 0 }; alarm 30;
my $r; 1 while sysread($s, $r, 4096);
```
