# The helper

The helper is an Incus instance that runs `tink daemon run` next to the data: the backup scheduler, the job executor and the ingress
reconcile. The design is in `docs/helper-design.md` (PR #16); this page covers what exists. **Today that is the status document and the
commands that read it.** `tink helper install` and the image are later slices of the same phase.

## The status document

A daemon started with `--status-instance NAME` (and `--status-project` if that instance is not in the connection's project) publishes what it
is doing on that instance's own config, under `user.tink.helper.status`:

```
tink daemon run --jobs /var/lib/tink/jobs --status-instance helper --status-project tink-helper
```

It is one line of JSON, written by a PATCH of just that key (so it cannot conflict with, or overwrite, anything an operator edits on the
instance):

| Field | Meaning |
|---|---|
| `version`, `tz`, `started` | which tink, which time zone schedules run in, since when |
| `job_proto`, `policy_proto` | the job-directory protocol it speaks, and the **newest copy-policy protocol it can read** |
| `tick`, `heartbeat_seconds` | when it was written, and how often it is written when nothing changes |
| `skipped` | volumes it will **not** copy, and why: a policy of a protocol it does not read, one that does not parse, a pool it cannot list, a volume it cannot read |
| `failing` | copies that have failed since their last success: volume, target, how many in a row, since when |
| `last_job` | the newest finished job: id, state, when |
| `ingress` | the last ingress reconcile: ok or not, when, how many warnings (only if this daemon runs ingress) |

It holds **no error text**. Errors from Incus and its drivers can echo credentials, so the reasons it gives for something Incus refused are fixed
phrases, and the detail stays in the job's log.

**When it is written.** When something in it changes, and every `--status-heartbeat` (default 10 minutes) when nothing has. Every write is
one lifecycle event in the log of anything listening and about 90 ms of the Incus daemon's CPU, so it is not written on every tick. The daemon
offers it to the publisher every 10 seconds; the publisher compares and usually does nothing. It marks the instance with `user.tink.helper` on
the first write, so a daemon started by hand is found by the commands below as well.

**Why instance config.** Reading it costs no event (reading a file out of an instance does), and it is still there when the helper has
stopped, which is the case that matters.

## `tink helper status`

```
tink helper status [--check] [--json] [--instance NAME] [--project P]
```

Finds the helper (any instance marked `user.tink.helper`, in any project), reads its document, and judges it from that and from the state of
its instance:

| | |
|---|---|
| **healthy** (exit 0) | running, heard from recently, nothing skipped, nothing failing |
| **degraded** (exit 1) | running and heard from, but skipping a volume, failing a copy, failing an ingress reconcile, or saying nothing useful (no document yet, or one it cannot read) |
| **down** (exit 2) | the instance is stopped, there is no helper, or it has been silent for more than **2.5 heartbeats** |

`--check` prints one line per helper and sets the exit status, so cron, a monitor or Home Assistant can poll it. Without `--check` it prints the
details and exits 0 whatever it found (unless it could not look). `--json` prints everything.

```
$ tink helper status --check
degraded: tink-helper/helper: 1 volume(s) skipped: tenant/lib (user.tink.backup.policy was written for protocol 2; this tink speaks 1)
$ echo $?
1
```

**"Down" is the one the helper cannot report for itself**, which is why it is judged from outside, from a document that is still on the instance
after the helper has gone. Nothing here sends a notification: this is what a notifier would run. Nothing in tink does that yet.

**Under `--remote`.** It works from anywhere that can reach the server: it reads instance config. `daemon run` itself accepts a remote (the
helper runs as a client of its own host): see `docs/remote.md` for what it needs for its ingress half.
