# The helper

The helper is an Incus instance that runs `tink daemon run` next to the data: the backup scheduler, the job executor and (later) the ingress
reconcile. The design is in `docs/helper-design.md` (PR #16); this page covers what exists: **`tink helper install` and `remove`, the status
document, and the commands that read it.** The published image, `upgrade`, the ingress half and `plan` reading the status are later slices.

## Installing it

```
tink helper install --binary ./tink-linux --timezone America/Denver      # a linux tink binary, in a stock alpine image
tink helper install --image ghcr.io/.../tink-helper@sha256:...            # an image that has tink at /usr/local/bin/tink
```

It creates, in a project of its own (`tink-helper`): an OCI app container named `helper` that runs `tink daemon run --remote host ...`; two volumes,
`tink-helper-config` (its Incus client configuration, mounted where the client looks for it) and `tink-helper-data` (its jobs); a NIC; and a **proxy
device** that gives it the host's HTTPS API on its own loopback (`127.0.0.1:8443` inside the container), so the API is not exposed to anything it was
not already exposed to. It starts the instance, enrols it, and waits for the first status document.

**The helper's own certificate.** The helper is a client of its own host, with a certificate of its own. `install` does not hand it a credential:
it mints a **single-use trust token** and runs `tink remote add host ... --token-file -` **inside the instance** with the token on standard input
(never on a command line, never on disk). The key pair is generated inside, and the private key stays there (mode 0600 in `tink-helper-config`).
The host verifies nothing secret until the helper has verified the host, by the fingerprint the token carries.

- **Revocable:** `tink helper remove`, or `incus config trust remove` on the `tink-helper` entry, ends its access at once.
- **Auditable:** what the helper does to your data reaches Incus as `tls` with the certificate's fingerprint as the user, not as `unix`/`root`:
  checked on Incus 7.5.1 for a volume update (a copy stamp), a snapshot create and delete, and an instance update. **One exception:** the helper's
  status write is an instance PATCH, and the lifecycle event for a PATCH carries no requestor at all, so those writes (one on change and one
  per heartbeat) are not attributed. They are bookkeeping on the helper's own instance; the operations that move data are attributed.
- **Not confined.** The certificate is unrestricted: it has the reach of root on the host. A restricted certificate is not a boundary on current Incus
  (see `docs/helper-design.md`, "Security"), and nothing here claims one.

**When it is "down", and how fast you hear it.** `tink helper status` also checks the host's trust store (if it may read it): a helper whose
certificate has been revoked is reported **down at once**, not after its last document has aged. Without that check the helper would look healthy for
25 minutes, because it can no longer write.

**It does not claim health it has not earned.** The helper publishes nothing until its scheduler has completed a pass over the volumes; before that a
"nothing skipped, nothing failing" would be about volumes nobody had looked at. A pass that could not look (the proxy to the host's API comes up a
moment after the helper's process does) is retried in 5 seconds, not after the full minute.

**What it needs:** the host's API listening (`core.https_address`; the loopback address is enough, and `tink deploy` sets one), and a network for the NIC
(`--network`, else the default profile's, else `incusbr0`). It refuses, before creating anything, if either is missing, or if another helper exists on
the server.

**Running it again** is safe: what exists is left alone, and an enrolled helper is not enrolled twice. `--reissue` enrols it again with a fresh key pair
and removes the old certificate. `--binary` puts the binary in the instance, which is also how a helper runs where an image cannot be pulled.

```
tink helper remove [--purge]
```

Revokes the certificate, stops and deletes the instance. The volumes stay (the job history; the client configuration, which holds the keys for any
remote backup target) unless `--purge`, which removes them and the project if that leaves it empty.

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
