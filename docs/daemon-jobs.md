# Backups on a schedule: the daemon's scheduler and jobs

`tink daemon run --stacks DIR --jobs DIR` adds two workers to the daemon (beside the ingress reconcile loop, which `--no-ingress` leaves out):

- a **scheduler**: every `--scheduler-interval` (default a minute) it looks at each stack synced into `--stacks`, and queues a backup job in
  `--jobs` if at least one of the stack's copies is **due**, and none is already queued or running for that stack;
- an **executor**: it runs the queued jobs one at a time, oldest first, through the same code as `tink backup run`.

It runs under any supervisor: a systemd unit, OpenRC, or by hand. (The planned [helper](https://github.com/minihci/tink/pull/16) is a container that
runs exactly this, and replaces `daemon install`.)

```
tink daemon sync home tink.yaml --stacks /var/lib/tink/stacks     # store the stack; the scheduler picks it up on its next tick
tink daemon run --stacks /var/lib/tink/stacks --jobs /var/lib/tink/jobs --timezone America/Denver --no-ingress
tink daemon jobs --jobs /var/lib/tink/jobs                        # is it alive, and what has it done
tink daemon jobs --jobs /var/lib/tink/jobs --log <ID>             # one job: its status and log
tink daemon enqueue --jobs /var/lib/tink/jobs --stack home lib    # run a backup now, in the daemon, surviving this command
tink daemon cancel <ID> --jobs /var/lib/tink/jobs
```

## When a copy is due

The same decision `tink backup run --due` makes: the copy has never run, or the first scheduled time after its last success has passed, **and** the
backoff after any recent failures has elapsed (a failing copy waits `5 min x 2^(failures-1)`, never longer than its own interval; see
[volume-backup.md](volume-backup.md)). So a copy that cannot succeed is retried slowly, not every tick. A missed run (the daemon was down) is caught up
**once**, not once per missed slot. Schedules are evaluated in `--timezone` (default: the machine's), the zone Incus uses for its own snapshot schedules
when the daemon runs on the server.

The scheduler logs a problem (a stack that does not load, a volume that cannot be read, Incus unreachable) **once**, when it starts and when it goes away,
not every minute.

## Stacks

`daemon sync NAME FILE...` loads the stack as an operator would, packs it with every file it reads (`source_path`) keeping their relative layout, and makes
that the active version of the stack `NAME`. It becomes active only if it **loads where it lands**: a stack that does not parse never replaces one that does.
Activation swaps a symlink, so a scheduler tick sees the old stack or the new one, never a mixture. The two latest versions are kept.

Each stack is loaded on its own: one that breaks later (a disk problem, a hand edit) is reported and skipped, and the others carry on.

A stack that arrives from elsewhere is **data, not code**: it cannot make tink read a file outside the directory it was delivered in (paths, and symlinks,
are checked). Image `source` files are not shipped (a helper has no use for them); one outside the stack's directory tree is an error.

## Jobs

A job is a directory under `--jobs`:

```
<id>/request.json   what to do (proto, kind, origin, the stack's name or its bundled files, arguments)
<id>/bundle/        optional: stack files sent with this request only
<id>/READY          created LAST: nothing is read before it exists
<id>/status.json    queued | running | succeeded | failed | cancelled, times, the error, and a summary of what was done
<id>/log            what `tink backup run` printed (secrets scrubbed, at most 1 MiB)
<id>/cancel         create it to ask the job to stop
```

`READY` exists because the Incus file API writes files in place: a reader can see a half-written file, or a directory with only some of its files. A job is
read only once `READY` says its writer has finished. `proto` (now 1) is the version of this layout, and what a client and the daemon must agree on.

- **A bad job never stops the executor.** A request that is not JSON, a protocol it does not speak, an unknown kind, an error and even a panic each become
  that job's `failed` status. Jobs left `running` by a process that died are marked `failed (interrupted)` when the daemon starts; the schedule retries the work.
- **Cancel** is checked before a job starts and between its copies: a copy already under way finishes. A job that finished is `succeeded` even if the cancel
  arrived a moment too late.
- **Retention:** the newest 50 finished jobs, and any finished in the last 14 days, whichever keeps more; jobs that are not finished are never removed. An upload
  that never got its `READY` is removed after an hour.
- **Concurrency:** one job at a time, so a long copy to a slow target delays copies to others. The same volume to the same target never runs twice at once.

## Staying alive

Each worker is **isolated and supervised**: one that crashes or panics is logged and restarted (after 5 seconds, doubling to a minute), and does not stop the
others. The scheduler writes a **heartbeat** (`<jobs>/heartbeat`: time, pid, tink version, time zone) every tick, which `daemon jobs` shows, so a daemon that
stopped can be told from one with nothing to do even when its logs are gone.

## Not here yet

The container that runs this (`tink helper`), triggering a run from another machine, and `plan` reading the heartbeat. `daemon install` still prints a unit for
the old ingress-only invocation; for this, write the unit's `ExecStart` with the flags above.
