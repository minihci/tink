# Backups on a schedule: the daemon's scheduler and jobs

`tink daemon run --jobs DIR` adds two workers to the daemon (beside the ingress reconcile loop, which `--no-ingress` leaves out):

- a **scheduler**: every `--scheduler-interval` (default a minute) it lists the volumes on the server that carry a **copy policy**, and queues a backup job in
  `--jobs` if at least one of their copies is **due**, and no backup job is already queued or running;
- an **executor**: it runs the queued jobs one at a time, oldest first, through the same code as `tink backup run`.

It runs under any supervisor: a systemd unit, OpenRC, or by hand. (The planned [helper](https://github.com/minihci/tink/pull/16) is a container that
runs exactly this, and replaces `daemon install`.)

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
<id>/request.json   what to do (proto, kind, origin, the stack files it carries if any, arguments)
<id>/bundle/        optional: stack files sent with this request only
<id>/READY          created LAST: nothing is read before it exists
<id>/status.json    queued | running | succeeded | failed | cancelled, times, the error, and a summary of what was done
<id>/log            what `tink backup run` printed (secrets scrubbed, at most 1 MiB)
<id>/cancel         create it to ask the job to stop
```

`READY` exists because the Incus file API writes files in place: a reader can see a half-written file, or a directory with only some of its files. A job is
read only once `READY` says its writer has finished. `proto` (now 1) is the version of this layout, and what a client and the daemon must agree on.

A job works from the volumes' policies, found when the job runs. A job that carries a **bundle** (`tink daemon enqueue -f FILE`) instead runs the copies that
stack declares, for this job only: it never changes what the scheduler does, so a checkout of a branch with a shortened `retain` cannot prune real restore
points on the next tick. A stack that arrives this way is **data, not code**: it cannot make tink read a file outside the directory it was delivered in
(paths, and symlinks, are checked). Image `source` files are not shipped (a helper has no use for them); one outside the stack's directory tree is an error.

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
stopped can be told from one with nothing to do even when its logs are gone.

## Not here yet

The container that runs this (`tink helper`), triggering a run from another machine, and `plan` reading the heartbeat. `daemon install` still prints a unit for
the old ingress-only invocation; for this, write the unit's `ExecStart` with the flags above.
