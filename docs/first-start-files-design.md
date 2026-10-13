# Design: put an instance's files in place before its first start

**Status: a design note, 2026-10-13. Built** (#78) and measured in the lab (see "Measured"). It fixes a race found in the Headscale lab ([what-tink-is.md](what-tink-is.md), "Where it is strong, and where it is weak"). Claims are marked **[lab]**, **[code]**, **[proposed]** or **[verify]**.

## The problem

When tink creates an instance, it creates it, applies its config, and **starts it**; only afterwards do the `kind: file` resources that target it push their files **[code: `createInstance`, then the file level]**. A service that needs one of those files at start has a window in which the file is not there yet.

Seen on a throwaway Headscale 0.29.4 (it needs `/etc/headscale.yaml`) **[lab]**:

- The app exits within about a second (`FTL … error loading config file /etc/headscale.yaml`), the container is `RUNNING` for roughly eleven seconds and then `STOPPED`. In that window, 3 of 15 file reads failed.
- On the first rebuild from nothing, tink's own calls landed in it: `file/headscale-policy: reading current content … bad file descriptor`, then `Failed to retrieve PID of executing child process`. The apply failed. A second apply converged, because every step is idempotent.
- It is a race, so it is not reliable in either direction: the first creation of the same Headscale a few hours earlier, and of Authelia, went through.

The same first-start sequence is how any OCI service with a config file is brought up with tink: Authelia, Mosquitto, Headscale, the helper's own configuration.

## What Incus allows **[lab]**

An instance that was created and never started accepts file pushes (the call tink already uses) and gives them back, and the first start then finds them: a throwaway Headscale, created with `init`, given its config while `STOPPED`, started once, came up cleanly. So the ordering "create, place the files, start" is available; it is only not what tink does.

## The shape **[proposed]**

**When tink creates an instance, it places the instance's files before starting it.** Inside `createInstance`: create, apply config and devices, push the files that belong to this instance, start. Nothing else about the graph changes.

It composes with what is already there, because `applyOne` plans each resource immediately before acting on it **[code]**: when the file resource's own turn comes, the planner reads the content, finds it equal, and says `no changes`. No second push, and no restart (a file's `restart: true` only fires when it pushes). The only visible difference is that those files say `no changes` in the apply that created their instance, and the instance's line names them.

### Which files

A file is placed with its instance when:

1. its `instance:` is an instance this stack declares, **and that instance is a container**. A virtual machine needs its agent running to take a file **[code]**, so it keeps today's behaviour;
2. its declared content is known now (`content` or `source_path`, read at load): always true for `kind: file`;
3. everything it waits for is the instance or another such file. A file that `depends_on` an exec, or on anything else that must have happened first, is left to its own turn. This is worked out to a fixed point: removing one file can make another ineligible.

Files are placed in declaration order.

### What a failure to place means

**Placing is an ordering improvement, never a new way to fail.** If a push into the stopped instance fails (a directory that is not there, say), the instance is still started and the file resource fails in its own turn, with the message it has always had. The alternative, failing the creation, would leave a created-but-stopped instance and a new class of half-done state for no benefit.

### What is said

`instance/headscale: created; placed 2 file(s) before the first start (headscale-policy, headscale-config)`, and only for files that were placed. `plan` is unchanged: it still says `would create` for each.

## Alternatives considered

- **Split the instance into "create" and "started" steps in the graph**, with the start waiting for every file. The most general, and the biggest change: every resource that needs the instance running (execs) would have to be re-pointed at the second step, and the level order would change for all stacks. Placing at creation gets the same result for the case that matters, with no graph change.
- **Retry the transient errors** (`bad file descriptor`, `Failed to retrieve PID`). Treats the symptom: the app still crashes and restarts, and `restart: true` files would restart it again. It also depends on guessing which errors are transient.
- **Make the application wait** (an entrypoint that polls for the file). Not tink's call, and the image has no shell.
- **Do nothing; apply twice.** What the lab did. Every step is idempotent, so it converges, but "apply it again" is a poor first impression for a first install.

## Tests

- The selection (`firstStartFiles`): a container's files are chosen; a VM's are not; a file that waits for an exec is not; the fixed point (a file that waits for an ineligible file is not); a file for an instance not in the stack is not; two instances with the same name in different projects do not mix.
- The order, with fakes: create, config, each file in declaration order, start. A failed push does not stop the start. A failed create stops everything and places nothing.
- The reporting: the instance says which files were placed; a file that failed to place is not named.
- The existing file planning already covers "content equal means `no changes`"; one test pins that a file placed at creation then plans as `no changes`.
- Live: rebuild the lab Headscale from nothing. The first apply must converge with no failures, the log must not contain `error loading config file`, and the process must have started once.

## Measured, 2026-10-13 **[lab]**

The lab Headscale was rebuilt from nothing (instance and volume deleted, one `plan apply`), counting the first applies that converge:

| Build | Converged on the first apply | Failed |
|---|---|---|
| without the fix | 8 of 12 | 4 (the same message every time: `file/headscale-policy: reading current content of /etc/headscale-policy.hujson on headscale: bad file descriptor`) |
| with the fix | 11 of 11 | 0 |

With the fix each apply said `placed 2 file(s) before its first start`, the files then planned `no changes`, the exec set the first policy, and the plan afterwards was clean. If the true failure rate without the fix were a third, 11 successes in a row would happen about one time in 80. Caveats: one control batch passed 4 of 4 and the next 4 of 8, so a handful of runs misleads; one host and one service; and the check "no error in the application's log" was dropped because `incus console --show-log` keeps only recent lines.

## Not in this slice

A virtual machine's files; files that wait for an exec; an option to leave an instance stopped; changing when an existing instance is started.

## Open questions

1. Whether `plan` should say that a file will be placed with its instance. It says `would create` today, which is true; the difference is only in the apply.
2. Whether the same placing should apply when an existing but *stopped* instance is started by an apply. Today that case already works, since a stopped instance takes the push.
