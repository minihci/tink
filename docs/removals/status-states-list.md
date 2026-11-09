# Changed: the status document's three lists are one list, `states`

**For an agent (or a person) who finds a helper that still publishes the old status document.** The helper's status document (`user.tink.outcome.helper.status`, protocol 2) had three lists, `skipped`, `failing` and `reconciles`. Protocol 3 has one, `states`, and a list of `events` ([the states list](../helper.md#the-states-list)). tink does no conversion: a document it cannot use is read for what it says, and the helper's next write replaces it.

Read the whole page before changing anything. Ask the owner before acting on a server that is in use.

**Status: written with the change and not run on a live server.** The commands below are the ones the change makes necessary; none has been run against a real helper.

## What changed

| Old (protocol 2) | New (protocol 3) |
|---|---|
| `skipped[]`: `volume`, `reason` | a `states` record with `what` `backup`, `resource` the volume, `state` `skipped`, the `reason`, and a `since` (the old entry had none) |
| `failing[]`: `volume`, `target`, `count`, `since`, `failed-at` | a `states` record with `what` `backup`, `resource` the volume, `target`, `state` `failing`, `since`, `failed-at` and `failures` (the old `count`) |
| `reconciles[]`: `what`, `site`, `state`, `reason`, `since` | `states` records with the same fields; `what` is `dns` or `derived` and `resource` names a derived file (the old `what` was `derived/NAME`) |
| the words `changed` and `no changes` (never stored; a rewrite was said in the log only) | `converged`; a rewrite is a `healed` event |

## What this does to a server

- **A helper that is still running the old build publishes protocol 2.** The new `tink helper status` reads it, but finds no `states`, so it cannot see that a volume is skipped or a copy is failing: **it reads as healthy where the old tink read it as degraded.** Until the helper is upgraded, look at its document by hand (below). This is the one that matters.
- **A script that reads `tink helper status --json` by the old names** (`.status.skipped`, `.status.failing`, `.status.reconciles`) gets `null`, and no error. A monitor that treats a missing value as "nothing to report" goes on saying the helper is well. Read `.status.states` and select the records you mean.

## Find the affected helpers

```sh
tink helper status --json | jq -r '.[] | "\(.project)/\(.name)\tstatus protocol \(.status.proto)"'
```

A helper that says `2` is on the old document. A helper whose status says nothing (`null`) has not published one yet.

## Fix

Upgrade the helper (it is drained first, and a running job is waited for): `tink helper upgrade`. The new daemon publishes protocol 3 on its first pass, and replaces the old document. Do not edit the old document into the new shape.

## Test the result

```sh
tink helper status --json | jq '.[0].status | {proto, states, events}'
tink helper status
```

`proto` is `3`. `states` lists every volume that is skipped and every copy that is failing with a `since`, and a record for each thing a reconcile keeps if the daemon runs one. A helper that was degraded before the upgrade for a skipped volume or a failing copy is degraded after it, for the same reason. Restart the daemon (or the helper's instance): the `since` of every state that is still true, and the `events`, are the same as before it.
