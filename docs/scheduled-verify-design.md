# Scheduled restore checks as a separate identity

**Design for review, 2026-10-14. Nothing here is built, and nothing was run to write it.** Tags: **[code]** read in this tree; **[docs]** stated in `docs/` or the author's platform repository (`authz-lab/`); **[lab]** measured on a throwaway Incus 7.5.1 server; **[proposed]** this design's choice; **[to confirm]** a lab question the design depends on. Sizes: S under a day, M one to three days, L about a week.

## Where this starts

- Nothing schedules a restore check. `volbackup.Verify` has one caller, `tink backup verify` **[code]**; a policy's `verify: weekly` only sets how stale `verified-at` may get before `plan` warns **[code]**.
- A check needs a scratch volume, a throwaway instance, the default profile and an edit right on the source for the stamp (edit includes delete). The daemon helper's real work needs none of it: 10 rules, no instance rights, and `tink backup verify` is refused under them **[lab]**.
- "Create instances" alone lets a restricted certificate make a privileged instance with a host path mounted, unless the project is `restricted=true`. A check in a separate restricted, NIC-less project, the scratch volume copied across and mounted read-only, works **[lab]**; `--check-project` for `backup verify` is being built elsewhere.
- Settled: scheduled checks are not added to the controller. They are a separate identity.

**Decisions.** (1) A second container, the *verify helper*: the same binary, `tink daemon run --workers verify`, a `kind: helper` with `role: verify`. (2) A check is due at 75% of `verify.every`, one at a time, 30 minute deadline, never beside a copy of the same volume. (3) The verifier writes a **ledger** on its own volume; the daemon helper stamps the sources from it; `plan` keeps reading `user.tink.backup.verified-at`. (4) Eight rule groups, none an edit right on a source; checks run in the restricted `tink-verify` project from an image prepared in advance. (5) A failure is a stamp, a `plan` warning and a degraded status; the only automatic reaction is a slower retry.

## 1. Shape

**DEFAULT.** A second container running `tink daemon run --workers verify --check-project tink-verify`. Without `--workers` the daemon starts what it starts today, so no existing helper changes **[proposed]**. The verify set: a verify scheduler, the job executor (one kind, `verify-run`, one check per job), a sweeper for leftovers, the status publisher. No backup scheduler, no ingress. In a platform stack it is a second `kind: helper`:

```yaml
kind: helper
role: verify               # default: controller, the helper that exists today
name: verify-helper
project: tink-verifier     # not the check project: the helper needs a NIC for the host API, the check project must have none
check_project: tink-verify
```

What the kind needs: **(a)** a role marker, `user.tink.helper.role`. A server has one helper today: install refuses a second and the trust name `tink-helper` is a constant **[code]**; the trust name becomes per role (`tink-verify-helper`) and callers that look for "the helper" (`backup run` hand-off, `plan`, `helper status`) pick by role. **(b)** It is born restricted-ready: no `proxy` device, the host API over the network, status on a custom volume's config, so it holds no edit right on any instance **[lab, Part 3f]**; the controller needs the same (P1, P2 below). **(c)** The check project and profile are declared in the same stack (section 4), not created by install, so `plan` shows them; the worker re-checks the boundary on every pass. **(d)** `helper.md` says splitting the helper "would isolate nothing unless their credentials were scoped separately" **[docs]**. Here they are, so "one container is enough" becomes "one container per credential".

**REASON.** Credential: a check needs instance rights, which are safe only inside a restricted project, and three of the controller's six volumes on tron are in `default`, which cannot be restricted **[docs]**. Queue: the executor runs one job at a time, oldest first, so a 20 minute check would hold up every copy behind it, and an upgrade drain waits up to 15 minutes for it **[docs]**. Exposure: it runs a user-declared image and command over restored data.

**ALTERNATIVE.** Stay human-triggered. Nothing to build or attack, but every restore check on tron so far was run by hand **[docs]**, so a backup is tested when someone remembers. As a stopgap, `tink backup verify --check-project tink-verify` from cron or launchd with the operator's certificate: no new code, but a root-equivalent credential on a schedule on a machine that sleeps.

## 2. What triggers a check

**DEFAULT.**

- **Input:** the volume's policy, `verify.every` and `verify.check` **[code]**. No `every`: on demand only, as today. No `check`: the worker only restores (no instance, no image **[code]**), recorded `with: restore`.
- **Last verified** = the newer of the source's `verified-at` and the ledger's last success, so a hand run counts and an unstamped result is not forgotten.
- **Due** at age 0.75 x `every` (24 h, 7 d, 31 d, the values `plan` uses **[code]**) plus a fixed per-volume offset, `hash(label) mod min(0.1 x every, 2 h)`. The margin lets a healthy site finish before `plan` warns at 1.0 x. A hash, not a dice roll: a restart does not re-roll.
- **First run:** never verified is due at once, no offset, one per pass in label order. No usable snapshot or restore point yet: `skipped`, not failed.
- **Backoff:** 30 min x 2^(n-1) after a failure, at most `every`/8; a success resets it. (Copies start at 5 min **[code]**; a check is heavier.)
- **One at a time:** one executor and at most one pending `verify-run` job, the most overdue volume, as `Store.Pending(kind)` does for backups **[code]**; plus `limits.instances: 1` on the check project **[to confirm]**.
- **Deadline:** 30 min for the whole check, 10 for the command. `ExecInGuest` has none today **[code]**, so a hung check would wedge the verifier. On timeout: cancel the exec, delete instance and scratch volume, class `timeout`.
- **Disk guard:** pool free space at least 1.25 x the source's used size, else `deferred: low space`, retried hourly, not a failure. Pessimistic on copy-on-write pools, by design. `limits.disk` on the project is the hard second layer **[to confirm]**.
- **Never with a copy:** the offset is the random delay, but any random start can collide, so also a hard rule: no start within 30 minutes either side of a copy schedule of the same volume (`backupmeta.NextRun` **[code]**). And skip transient `tink-copy-*` snapshots: `pickSnapshot` takes the newest of all **[code]** and the copy deletes its own when done, so a check could restore a snapshot that vanishes.
- **Restores** the newest non-transient local snapshot; if the volume has none (copies without a snapshot policy), the newest restore point on a target that is a pool of this server. Remote targets are not checked in v1 (they need the remote in the verifier's client configuration and pull the data through it).

**REASON.** The cadence-and-stamp model exists, `plan` already judges it, and it survives downtime: a missed check is made up once, like a missed copy **[docs]**.

**ALTERNATIVE.** A cron `verify.schedule` per policy: an explicit time, but a new policy field (a protocol bump the helper status must advertise) and the collision problem stays.

## 3. Where the result is recorded

| | Verifier's right on sources | `plan` reads | Weakness |
|---|---|---|---|
| **(a) ledger; the daemon stamps** | view only | the source's `verified-at`, unchanged | one scheduler tick of lag; stamps stop if the daemon helper is down |
| **(b) verifier stamps** | `can_edit` on every source | unchanged | edit means delete and any config key, the policy included (`stamp` is a whole-config PUT **[code]**): the identity that runs user-declared images could delete or re-point every backed-up volume |
| **(c) verifier's status only** | view only | the verifier's status document, found by role | two places for one fact; the volume stops describing itself; `VerifyWarning` takes a second input |

**DEFAULT: (a).**

- **The ledger** is one key, `user.tink.verify.ledger`, one-line JSON, on the config of the verifier's data volume beside its status (the P1 move). One entry per source: `volume` (project/pool/name), `at`, `outcome`, `class`, `snapshot`, `with`, `from`, `image` (fingerprint), `seconds`, `n` (failures in a row), `since`. No error text **[docs]**. Entries for volumes without `verify` are dropped. Value size limit **[to confirm]**.
- **The daemon helper** applies it in `Tick`, only if the volume's policy has `verify`, `at` is not in the future, it is newer than the source's stamp, and the strings are plain names. A pass writes the four existing stamps (`verified-at`, `verified-snapshot`, `verified-with`, `verified-from`) and clears the failure stamps; a failure writes the new ones. Idempotent, one PUT per changed volume, no new right: it already has view by type and edit on every policy-bearing source **[lab]**.
- **What `plan` reads, exactly:** `user.tink.backup.verified-at` for staleness and `verified-with` for the "check declared, only the restore proven" rule, the keys `VerifyWarning` reads today **[code]**; a scheduled result looks like a hand run. New, for failure: `user.tink.backup.verify-failed-at` and `verify-failed-n`, mirroring `copy.<target>.fail.at` and `.fail.n` **[code]**.
- **Limits:** with the daemon helper down, results wait and `plan` over-warns (the safe direction); the verifier's status says `unapplied: N` when an entry is over 10 minutes newer than its source's stamp. A lying verifier can claim a false pass, which (b) allows too; (a) bounds the damage to that.

**REASON.** The most exposed identity gets the least power, and the volume stays the record: `incus storage volume show` tells the story and `plan` has no new reader.

**ALTERNATIVE.** (c), if the daemon helper must not change: simplest, but it splits one fact across two stores. (b) is rejected, not deferred.

## 4. The verifier's rights, project and image

**Identity.** A restricted certificate, enrolled first, then the class `tls-restricted` routed to the scriptlet. One scriptlet serves the class, so the generated text gains a second identity block keyed on this fingerprint (`details.Username`); the daemon's block is unchanged and any other restricted certificate still gets nothing **[lab]**.

| Object | Entitlements | For |
|---|---|---|
| `server:incus` | `can_view` | every request asks first **[lab]** |
| `project:tink-verify` | `can_view_operations`, `can_create_storage_volumes`, `can_create_instances` | scratch volumes, throwaway instances **[lab in-project; cross-project to confirm]** |
| `profile:tink-verify/default` | `can_view` | whether the check instance needs a root disk **[lab]** |
| prefix `storage_volume:tink-verify/` | `can_edit` | create, scrub, delete `<volume>-verify-<UTC>`; the project holds nothing else **[proposed]** |
| prefix `instance:tink-verify/` | `can_view`, `can_edit`, `can_update_state`, `can_exec` | the throwaway `tink-verify-<volume>-<UTC>` **[lab]**; edit reaches its devices, bounded by the restricted project |
| `storage_volume:tink-verifier/<pool>/custom/tink-verifier-data` | `can_edit` | ledger and status on its config **[proposed]** |
| `project:<each source project>` | `can_view_operations` | the cross-project copy's operation **[to confirm]** |
| prefixes `project:`, `storage_pool:`, `storage_volume:` | `can_view` | listings, policies and stamps, pool free space, reading the source snapshot **[lab for listings; the copy to confirm]** |

Absent on purpose: any edit or snapshot right on a source, any right on an instance outside `tink-verify`, file access. The unrestricted `default` project gets view and operation-watching only. The table does not change when a volume is added. **First lab question:** does copying a snapshot across projects ask for more than view on the source? If it asks for edit, (a) loses its point; the fallback is that the daemon helper (which has the source rights) makes the scratch copy and the verifier only runs the check.

**Project recipe**, as stack YAML so `plan` shows it:

```yaml
kind: project
name: tink-verify
config:
  features.profiles: "true"      # own profiles: required to be restricted [lab]
  features.images: "true"        # own image store, for the prepared check images [proposed]
  restricted: "true"             # privileged, host-path disk, raw.lxc, nesting refused, admin too [lab]
  restricted.devices.nic: block  # no NIC can be added; the lab only left the profile without one [proposed]
  limits.instances: "1"          # limits.*: [proposed, to confirm]
  limits.cpu: "2"
  limits.memory: 2GiB
  limits.processes: "512"
  limits.disk: 200GiB
---
kind: profile
name: default
project: tink-verify
devices:
  root: {type: disk, path: /, pool: default}   # root disk only [lab]
```

**Image: pinned and prepared.** A `remote:ref` image is resolved through the caller's image-server configuration **[code]**, so as written the verifier needs registry egress, a tag can move between two checks, and a registry outage shows up as a failed check of a healthy backup. The NIC-less check instance takes no part in a pull; the Incus server does it over the host's network **[to confirm for OCI remotes]**. **DEFAULT: the verifier never pulls.** An admin runs `tink verify prepare` (later also from `plan apply`): it resolves each `check.image` to a digest, imports it into the check project's image store under the alias `tink-check-<hash of the reference>`, and prints the digest. The verifier creates instances only from that local alias (`resolveLocalImage` takes an alias or fingerprint **[code]**) and records the fingerprint in the ledger. An unprepared image is `skipped: image not prepared`, not a failure; a moved tag changes nothing until `prepare` runs again. Whether creating from a local image needs an `image:` view right is **[to confirm]**. **ALTERNATIVE:** put `@sha256:` in the declaration and let the server pull on each check: no extra command, but a registry dependency in every check, and digest support in Incus's OCI remote is **[to confirm]**.

## 5. Failure and alerts

- **Classes.** `check` (restored, command non-zero: the signal that matters), `restore`, `timeout`, `cleanup` (passed, something left behind). Not failures, shown as skipped or deferred with a fixed phrase: no snapshot, image not prepared, low space, policy unreadable, check project not as the recipe says.
- **Status document** (one-line JSON, same heartbeat, no error text; detail stays in the job log, scrubbed, 1 MiB **[docs]**): `role`, `current`, `last`, `failing` (volume, class, n, since), `skipped`, `unapplied`, `leftovers`, `boundary`, `ledger_proto`. `tink helper status` calls it **degraded** for any failing item, an item skipped over a day, unapplied or leftover results, or a boundary that is not the recipe (the worker then runs no checks); **down** if stopped, silent past 2.5 heartbeats, or its certificate is revoked **[docs]**.
- **In `plan`**, beside the unchanged staleness warning: `warning: the last scheduled restore check failed (2 in a row, the last 3h ago); last good check 6d ago -- see tink helper log, or run tink backup verify lib`. Shown when `verify-failed-at` is later than `verified-at`; the next success, scheduled or by hand, clears it.
- **NOT done automatically:** no notification (there is no delivery channel; `helper status --check` is what a monitor polls **[docs]**); no retry faster than the backoff; no re-copy; no pruning, deleting or demoting of any restore point; no pausing of copies; no change to a policy or check; no touching `verified-at` on failure. Worth its own decision later: pruning never removes the last restore point known good, which `verified-snapshot` now makes possible.

## 6. tink changes

| # | Change | Files | Size | Depends on |
|---|---|---|---|---|
| 1 | A deadline for a check: `Verify` takes a timeout, `ExecInGuest` can be cancelled | `volbackup/volbackup.go`, `incusapi/exec.go` | M | none |
| 2 | `pickSnapshot` skips transient `tink-copy-*` | `volbackup/volbackup.go` | S | none |
| 3 | Check in another project; "do not stamp, return the result" | `volbackup/volbackup.go`, `cmd/tink/backup.go` | S | the `--check-project` work in progress |
| 4 | Disk guard; sweeper (anything in the check project older than twice the deadline) | `volbackup/` (new) | M | 3 |
| 5 | Ledger format; `VerifyDue` (75%, offset, backoff, avoid window); stamps `verify-failed-at/n`; `VerifyFailureWarning` | `backupmeta/`, new `verifyledger/` | M | none |
| 6 | Worker mode: `--workers`, scheduler, `verify-run` handler, boundary preflight | `daemon/`, `cmd/tink/` | L | 1 to 5 |
| 7 | Controller applies the ledger in `Tick`; `backuprun.DiscoverVerify` (verify-only policies are not items today **[code]**) | `daemon/helper.go`, `backuprun/` | M | 5 |
| 8 | `plan` failure warning; flip "nothing runs verify on a schedule yet" | `resolve/volumebackup.go`, docs | S | 5 |
| 9 | Role-aware helper: marker, trust name, `Find` by role, status `verify` section, `kind: helper role: verify`, install with no proxy device | `helper/`, `resolve/helperkind.go`, `cmd/tink/helper*.go` | L | P1, P2 |
| 10 | `tink verify prepare`; the verifier creates from the local alias | `cmd/tink/`, `volbackup/` | M | 3 |
| P1 | Shared with the controller: status on a custom volume's config **[lab, 3f]** | `helper/publish.go`, `status.go`, `find.go` | M | none |
| P2 | Shared with the controller: host API over the network, no `proxy` device **[lab, 3f]** | `helper/install.go` | M | none |
| 11 | Outside tink: the scriptlet generator's second identity block; stack YAML for project, profile, helper | platform repository | M | 9 |

(Paths are under `internal/` unless they start with `cmd/`.) Each change updates its docs (`helper.md`, `volume-backup.md`, `daemon-jobs.md`, a new `verify-helper.md`). **Order:** 1, 2, 3 (useful by hand today); P1, P2 (the controller needs them anyway); 5, 4; then 6, 9, 10; then 7, 8. The controller's rights do not change.

## 7. Risks and test plan

**Risks.** (1) The cross-project copy may need more than view on the source: the lab decides the design. (2) The check image and command are user-declared: contained by an unprivileged, NIC-less, read-only, resource-limited instance, a deadline and a one-instance project; the job log keeps the check's output, so a check should print counts, not content. (3) A volume on a TrueNAS 25.10 pool cannot restore its own snapshots **[docs]**, so its scheduled check fails every time: accurate, noisy; leave `every` off for it. (4) Version skew between the helpers: the ledger carries `proto`, the status `ledger_proto`; an entry the daemon cannot read is skipped visibly, as an unreadable policy is **[docs]**. (5) A verifier killed mid-check leaves a volume and an instance: the sweeper, safe because the project is dedicated. (6) The scriptlet is version-fragile **[docs]**: generated, and rehearsed at every Incus upgrade. (7) A snapshot expiring under a restore fails for a reason that is not the backup's: one immediate retry on "not found", not counted.

**Test plan.** Unit tests with the existing fakes: due arithmetic (first run, 75%, offset, backoff cap and reset, avoid window), ledger validation (future time, unknown volume, no `verify`, unknown proto), apply idempotence, `plan` wording, status judging, sweeper, snapshot filter, disk guard. Then a **lab rehearsal** on the nested server (same Incus version as tron, as in `authz-lab/`): create the project from the recipe, enrol a restricted certificate, drive the real `tink daemon run --workers verify` under the generated scriptlet, and find the minimal rights with the refuse-and-add loop. Positive: a pass, a failing check, a timeout, a killed verifier, a full pool, an unprepared image, sources in `tink` and in the unrestricted `default` project, the ledger applied by a restricted daemon helper and read by `plan`, an upgrade drain during a check. Negative, each refused: a privileged instance, a host-path disk, a NIC, `raw.lxc`, an edit or delete on a source, exec or files in another project, an instance in `default`, server config, a new trusted client. Nothing touches tron until it passes; rollout follows `gen-helper-scriptlet.md`.

## 8. Open questions (with my default)

1. **Build it, or stay human-triggered with the cron stopgap?** Build, in the order above, after the by-hand `--check-project` ships.
2. **What to restore.** Newest non-transient local snapshot, else the newest restore point on a local-pool target; no remote targets in v1.
3. **Image policy.** Prepared in advance; the verifier never pulls.
4. **Names.** Instance `verify-helper` in project `tink-verifier`, checks in `tink-verify`, trust name `tink-verify-helper`.
5. **Where results live.** (a), stamped by the daemon helper; the daemon helper must then run for `plan` to see scheduled results.
6. **Margins.** Due at 75% of `every`, offset up to 10% (at most 2 h), avoid window 30 minutes, backoff 30 minutes doubling to `every`/8.
7. **Limits.** 30 minute deadline (10 for the command), 1 instance, 2 CPU, 2 GiB, 512 processes, 200 GiB scratch, disk guard 1.25 x.
8. **Production volumes in `default`.** Include them (only view is needed there), once the first lab question is answered.
9. **Should a failing check protect the last good restore point from pruning?** Not now; a separate decision.
10. **Order of the shared prerequisites.** P1 and P2 first: the controller's restricted certificate needs them whether or not the verifier is built.
