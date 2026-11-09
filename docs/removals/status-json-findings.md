# Changed: `tink helper status --json` has `findings`, not `reasons`

**For an agent (or a person) who finds a script or monitor that reads `reasons` from `tink helper status --json`.** The list of strings `reasons` is gone. Each object now has `findings`: a list of objects with `severity`, `reason`, `subject` and `detail` ([health](../helper.md#health-what-the-document-says)). tink keeps no old key beside it.

Read the whole page before changing anything. Ask the owner before acting on a server that is in use.

**Status: written with the change and not run on a live server.** The commands below are the ones the change makes necessary; none has been run against a real helper.

## What changed

| Old | New |
|---|---|
| `reasons[]`: one string for each thing wrong | `findings[]`: `severity` (`warning` for degraded, `error` for down), `reason` (a fixed phrase for the kind of problem), `subject` (what it is about, left out when it is the helper as a whole) and `detail` (the old string, for a person) |
| the lines for skipped volumes and failing copies: one string for all of them (`2 volume(s) skipped: a (x), b (y)`) | one finding for each volume and each copy (`a is skipped: x`; `the copy of a to nas is failing, 2 in a row`) |

## What this does to a server

A script that reads `.reasons` gets `null`, and no error. A monitor that treats a missing list as "nothing to report" goes on saying the helper is well, so look for it. The `health` and `exit-code` keys are unchanged, and so is what `--check` exits with for every case that was judged before.

## Find the affected scripts

Search whatever polls the helper (cron jobs, a monitor, Home Assistant) for the name:

```sh
grep -rn 'reasons' /etc/cron.d /etc/cron.daily "$HOME/bin" 2>/dev/null | grep -i 'helper'
```

## Fix

Read `.findings` instead, and take what you need from each:

```sh
tink helper status --json | jq -r '.[] | .findings[]? | "\(.severity)\t\(.reason)\t\(.detail)"'
```

To group by kind of problem, use `reason`: it is the same phrase for every volume or file it is about. Prefer `exit-code` and `health` for a decision, as before.

## Test the result

```sh
tink helper status --json | jq '.[0] | {health, "exit-code", findings}'
tink helper status --check; echo $?
```

A healthy helper has no `findings` key at all. A degraded one has at least one finding with severity `warning`, and a down one at least one with `error`.
