# Backups on a schedule: the daemon's scheduler and jobs

`tink daemon run --jobs DIR` adds two workers to the daemon (beside the ingress reconcile loop, which `--no-ingress` leaves out):

- a **scheduler**: every `--scheduler-interval` (default a minute) it lists the volumes on the server that carry a **copy policy**, and queues a backup job in
  `--jobs` if at least one of their copies is **due**, and no backup job is already queued or running;
- an **executor**: it runs the queued jobs one at a time, oldest first, through the same code as `tink backup run`.

It runs under any supervisor: a systemd unit, OpenRC, or by hand. [The helper](helper.md) is an Incus instance that runs exactly this, which is the way to
run it without writing a unit; `daemon install` is not retired yet.

```
tink plan apply tink.yaml                                         # puts each volume's copy policy on the volume
tink daemon run --jobs /var/lib/tink/jobs --timezone America/Denver --no-ingress
tink daemon jobs --jobs /var/lib/tink/jobs                        # is it alive, and what has it done
tink daemon jobs --jobs /var/lib/tink/jobs --log <ID>             # one job: its status and log
tink daemon enqueue --jobs /var/lib/tink/jobs lib                 # run a backup now, in the daemon, surviving this command
tink daemon cancel <ID> --jobs /var/lib/tink/jobs
```

## Where the daemon gets its work

From the volumes themselves. `tink plan apply` writes each storage volume's copy policy (its `backup:` block's `copies` and `verify`, with the
`kind: backup-target` each copy names resolved into it) to one volume config key, `user.tink.backup.policy`, and the scheduler lists volumes
to find it. There is no stack stored anywhere for the daemon, so nothing can fall behind the repository without `tink plan` saying so: a volume
whose policy differs from the YAML shows as an update. See [volume-backup.md](volume-backup.md#where-the-policy-lives).

The listing covers **every project and every pool** the daemon's Incus connection can see, in one call per pool. A volume is skipped, and the problem logged once, when
its policy cannot be understood (another protocol, an unknown field, a schedule that cannot be read) or when its pool cannot be listed; the others carry
on. A volume that is a restore point, or a copy still being made, is never scheduled, whatever config it carries.

## When a copy is due

The same decision `tink backup run --due` makes: the copy has never run, or the first scheduled time after its last success has passed, **and** the
backoff after any recent failures has elapsed (a failing copy waits `5 min x 2^(failures-1)`, never longer than its own interval; see
[volume-backup.md](volume-backup.md)). So a copy that cannot succeed is retried slowly, not every tick. A missed run (the daemon was down) is caught up
**once**, not once per missed slot. Schedules are evaluated in `--timezone` (default: the machine's), the zone Incus uses for its own snapshot schedules
when the daemon runs on the server.

The scheduler logs a problem (a policy that does not read, a volume that cannot be read, Incus unreachable) **once**, when it starts and when it goes away,
not every minute.

## Jobs

A job is a directory under `--jobs`:

```
<id>/request.json   what to do (proto, kind, origin, arguments)
<id>/READY          created LAST: nothing is read before it exists
<id>/status.json    queued | running | succeeded | failed | cancelled, times, the error, and a summary of what was done
<id>/log            what `tink backup run` printed (secrets scrubbed, at most 1 MiB)
<id>/cancel         create it to ask the job to stop
```

`READY` exists because the Incus file API writes files in place: a reader can see a half-written file. A job is read only once `READY` says its writer has
finished, and it is written after the whole request. `proto` (now 1) is the version of this layout, and what a client and the daemon must agree on.

A job works from the volumes' policies, found when the job runs. Nothing but the request is sent with it, so what a job does cannot differ from what the volumes
say, and a checkout of a branch with a shortened `retain` cannot prune real restore points: a policy only changes when `tink plan apply` writes it. To run one
stack's copies, use `tink backup run -f FILE`, which runs where you start it.

Earlier versions of tink could also send a stack with a job (`daemon enqueue -f`, with its files in a `bundle/` directory). That is gone, and a request that still
carries one is **refused** (the job fails and says so), not run as an ordinary job, which would copy something other than what was asked for.

- **A bad job never stops the executor.** A request that is not JSON, a protocol it does not speak, an unknown kind, an error and even a panic each become
  that job's `failed` status. Jobs left `running` by a process that died are marked `failed (interrupted)` when the daemon starts; the schedule retries the work.
- **Cancel** is checked before a job starts and between its copies: a copy already under way finishes. A job that finished is `succeeded` even if the cancel
  arrived a moment too late.
- **Retention:** the newest 50 finished jobs, and any finished in the last 14 days, whichever keeps more; jobs that are not finished are never removed. An upload
  that never got its `READY` is removed after an hour.
- **Concurrency:** one job at a time, so a long copy to a slow target delays copies to others, and the scheduler queues no second backup job while one is
  waiting or running (the one that runs finds everything that is due). The same volume to the same target never runs twice at once.

## Staying alive

Each worker is **isolated and supervised**: one that crashes or panics is logged and restarted (after 5 seconds, doubling to a minute), and does not stop the
others. The scheduler writes a **heartbeat** (`<jobs>/heartbeat`: time, pid, tink version, time zone) every tick, which `daemon jobs` shows, so a daemon that
stopped can be told from one with nothing to do even when its logs are gone. A helper also publishes a **status document** on its own instance, which is
what `tink plan` and `tink helper status` read ([helper.md](helper.md#the-status-document)); the heartbeat file is the daemon's own liveness, on the machine
that has the directory.

## Not here yet

Starting a run from another machine and following it: `daemon enqueue`, `jobs` and `cancel` work where the jobs directory is, which for the helper is inside its
instance ([helper.md](helper.md)). `daemon install` still prints a unit for the old ingress-only invocation; for this, write the unit's `ExecStart` with the
flags above, or use the helper.
