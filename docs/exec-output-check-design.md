# Design: an exec check that compares live output with a declared file

**Status: a design note, 2026-10-13. Built** (#79) and **verified against a throwaway Headscale** with the four lab checks below. It closes the gap the Headscale lab found in getting a policy live ([identity-projector-design.md](identity-projector-design.md), "Experiments"). Claims are marked **[lab]**, **[code]**, **[proposed]** or **[verify]**.

## The gap

tink can place a policy file in a Headscale instance (`kind: file`) and the instance can apply it itself: in `database` mode, `headscale policy set -f FILE` run inside the container is validated, applies at once, needs no API key, and leaves the previous policy in force when the file is bad **[lab]**. What is missing is a way to say, in a stack, "run that when the file changed, and only then".

A `kind: exec` must declare exactly one of two guards **[code]**:

- **`check`**: a command whose exit status says whether the work is already done. The code documents this as the preferred guard, because it "re-derives 'already done' from live reality every time and so self-heals" **[code: resource.go]**.
- **`triggers`**: literal strings, hashed and remembered on the instance as `user.tink.exec.<name>.trigger-hash`. It exists for effects that leave nothing live to inspect (a signal) **[code]**.

Neither fits. A `check` is a command that exits 0 or not, and the Headscale image has no shell or `diff` to compare two things with **[lab]**. `triggers` are literal strings in the YAML, so the policy's content would have to be copied into them by hand, and a remembered hash is blind to the case that matters most: someone setting a different policy by hand.

But the policy case does have something live to inspect: `headscale policy get` prints what is in force. It prints the same bytes as the file that was set, plus one trailing newline (340 bytes against 339 in the lab) **[lab]**.

## The shape **[proposed]**

A third guard, `check_output`: the work is done when a command's output equals the declared content of a file resource.

```yaml
kind: file
name: headscale-policy
instance: headscale
path: /etc/headscale-policy.hujson
source_path: policy.hujson
---
kind: exec
name: headscale-policy-set
instance: headscale
command: [/ko-app/headscale, -c, /etc/headscale.yaml, policy, set, -f, /etc/headscale-policy.hujson]
check_output:
  command: [/ko-app/headscale, -c, /etc/headscale.yaml, policy, get]
  equals: headscale-policy        # the file resource whose declared content the output must equal
```

Rules:

1. **Converged** when the check command exits 0 and its standard output equals the declared content of the named file resource, after trimming trailing whitespace from both. **Not converged** otherwise, including when the command fails: `policy get` before any policy is set exits non-zero ("acl policy not found") **[lab]**, and that is exactly "not done yet". This is the existing convention for `check`: non-zero means run the command.
2. **Exactly one** of `check`, `check_output` and `triggers`. The loader enforces it, as it does today for two.
3. **`equals` must name a `kind: file` resource** in the stack, on the same instance, with declared content (`content` or `source_path`). Anything else is a load error. It also **implies the ordering edge**: the exec runs after that file is placed.
4. **After running `command`, check again.** If the output still differs, the apply fails with a message that says so, instead of reporting a change that did not take effect. (The existing `check` does not re-check; here the comparison is cheap and the failure is silent otherwise.)
5. **`plan` never prints the content.** It says `would run: the live output of 'policy get' differs from file/headscale-policy` (or that the check failed, and why).
6. **Bounded.** Output over 1 MiB is an error, not a comparison. A policy is a few kilobytes.
7. **Like the other execs:** an instance that does not exist, or a guest that cannot run commands yet, is the lenient "would run" in `plan` and an error in `apply`.

## Why this and not the alternatives

- **`triggers` that name a file** (re-run when the file's declared content changes). Small, and it fits the stored-hash mechanism. But it only remembers that the command ran: if the live policy is changed by hand it stays wrong, and a failed first run is hard to tell from a success. It is the guard the code says to avoid where a live one is possible.
- **A shell script as the check.** The image has no shell. Adding one is not tink's call.
- **A `kind: policy` for Headscale.** Engine-specific. The mechanism is general (any engine with a get/set pair: a proxy's loaded config, a directory's seeded content), and a role-level kind can wrap it later, as the second step.
- **Sending a signal** (file mode). Needs a process on the host that can signal; tink under `--remote` has none **[lab]**.

## What it buys

- `plan` shows the whole chain: `file/headscale-policy: would update`, then `exec/headscale-policy-set: would run`.
- A policy edited by hand in Headscale is noticed on the next `plan` and put back by the next `apply`.
- A bad policy fails the **apply**, with Headscale's own message and its previous policy still in force **[lab]**, instead of a log line nobody reads.
- The first policy is set by the same mechanism, which matters because Headscale is allow-all until then **[lab]**.

## Where it goes in the code **[proposed]**

`internal/resolve`: a `CheckOutput` field on `Resource` (command and the name of the file resource); parsing in `yaml.go`; the exactly-one rule and the `equals` rule in `validate.go`; the comparison in `exec.go` (`planExec`, `execConverged`, `runExec` with the re-check); the implied edge where dependencies are collected. A page in the exec documentation.

## Tests

Table tests for the comparison (trailing whitespace, a non-zero check, output over the bound, a different byte), for the loader rules, and for planning and applying with a fake server, including the re-check failing. Then the lab, below.

## Lab checks, run 2026-10-13 against Headscale 0.29.4 in database mode **[lab]**

All four passed as written. In order: (1) a changed policy showed the file update and `would run … (the output of […policy get] differs from file/…)`, the apply made it live, the next plan was quiet; (2) a different policy set by hand was noticed by `plan` (only the exec) and put back by `apply`; (3) an invalid declared policy failed the apply (`1 failed`, exit 1) with Headscale's own `unknown field "grantz"`, and `policy get` still returned the previous policy; (4) on a Headscale rebuilt from nothing, `policy get` exited non-zero, the exec ran the first set, the re-check passed and the next plan was quiet. The exec also has to wait for the **config** file (whose restart brings Headscale up), not only the policy file: `depends_on: [headscale-config]`.

The fourth needed a second apply, because of a race that is not in this change: tink starts a new OCI instance before the file it needs at start has been pushed, so the app exits and is restarted in a loop, and tink's calls that land in a gap fail. See [first-start-files-design.md](first-start-files-design.md).

The four checks as they were planned:

1. Change the policy file: `plan` shows the file update and `would run`; `apply` makes the live policy equal; the next `plan` says `no changes`.
2. Set a different policy by hand with the CLI: `plan` says `would run`; `apply` puts the declared one back.
3. Declare an invalid policy: `apply` fails with Headscale's message, and `policy get` still returns the previous policy.
4. A fresh Headscale with no policy set: `plan` says `would run`; `apply` sets the first policy.

## Not in this slice

A `policy check` before the set: in database mode the set itself validates and is atomic, so it matters only for file mode. A role-level kind for policy. Normalising anything other than trailing whitespace (CRLF, key order): the declared file is compared as written.

## Open questions

1. The field name: `check_output`, or a `check` that takes either a list (today) or a map.
2. Whether the re-check after `command` should apply to the existing `check` as well.
