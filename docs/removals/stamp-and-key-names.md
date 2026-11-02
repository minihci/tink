# Renamed: the keys and fields tink writes (time words, key classes, underscore keys)

**For an agent (or a person) who finds a server that was managed by a tink from before these renames.** tink now writes every key under `user.tink.` by its class, names every time in a stamp or document with the glossary's words, and no longer reads any of the old names. This page says what moved, what that does to a server that still has the old names, and how to fix it by hand. There is no migration code, on purpose: do the steps below, check each one, and do not write code to carry the old names forward.

**Status: written from the code and not yet run against a live Incus.** The renaming expression below was checked against the key names the code now writes, using a port of it in another language (jq was not installed where this was written, so the jq text itself has not been run); no command on this page has been run on a real server. Run it first on a disposable host that has stamped objects, a helper and restore points, and fix this page with what really worked.

Read the whole page before changing anything. Ask the owner before acting on a server that is in use. The one step that touches backups is [step 2](#2-re-mark-the-volumes-and-the-restore-points); do it before the first backup run after the change.

## What moved

Three sub-issues of #304 land in one release, so a server crosses all of them at once: the time vocabulary (#305), the key classes (#306) and the underscore keys (#307, [below](#the-underscore-keys-307)). The glossary has the rules ([Time words](../glossary.md#time-words), [Key classes](../glossary.md#key-classes)).

### Key classes (#306)

A key under `user.tink.` is a fact (a person writes it), intent (tink wrote it from the declaration) or an outcome (whatever ran an operation wrote it); the class is the prefix. The facts are unchanged: `user.tink.role`, `user.tink.leg.<nic>`, `user.tink.offer.<name>.*`, `user.tink.dns.zone`. Everything else moved:

| Old key | New key | Where it is |
|---|---|---|
| `user.tink.stack` | `user.tink.intent.stack` | every object tink created for a named stack (and an image's properties) |
| `user.tink.site` | `user.tink.intent.site` | instances of a stack with `site:` |
| `user.tink.publish.<name>` | `user.tink.intent.publish.<name>` | instances |
| `user.tink.derive.<file>` | `user.tink.intent.derive.<file>` | instances; `user.tink.derive.ingress` on a network ACL |
| `user.tink.backup.policy` | `user.tink.intent.backup.policy` | volumes with copies or verification |
| `user.tink.backup.copy.<target>.at` | `user.tink.outcome.backup.copy.<target>.copied-at` | the source volume (the old name before the time vocabulary) |
| `user.tink.backup.copy.<target>.volume` | `user.tink.outcome.backup.copy.<target>.volume` | the source volume |
| `user.tink.backup.copy.<target>.fail.at` | `user.tink.outcome.backup.copy.<target>.failed-at` | the source volume, while a copy fails |
| `user.tink.backup.copy.<target>.fail.n` | `user.tink.outcome.backup.copy.<target>.failures` | the source volume, while a copy fails |
| (new) | `user.tink.outcome.backup.copy.<target>.failing-since` | written by the first failure of a run; nothing to carry over |
| `user.tink.backup.copy-of` | `user.tink.outcome.backup.copy-of` | **restore points** (the marker that makes a volume one) |
| `user.tink.backup.copy-at` | `user.tink.outcome.backup.copied-at` | restore points |
| `user.tink.backup.copy-target` | `user.tink.outcome.backup.copy-target` | restore points |
| `user.tink.backup.copy-snapshot` | `user.tink.outcome.backup.copy-snapshot` | restore points |
| `user.tink.backup.copy-server` | `user.tink.outcome.backup.copy-server` | restore points |
| `user.tink.backup.copy-partial-of` | `user.tink.outcome.backup.copy-partial-of` | a copy that was cut off |
| `user.tink.backup.copy-partial-at` | `user.tink.outcome.backup.copy-partial-started-at` | a copy that was cut off |
| `user.tink.backup.verified-at`, `-snapshot`, `-with`, `-from` | the same names under `user.tink.outcome.backup.` | the source volume |
| `user.tink.exec.<id>.trigger-hash` | `user.tink.outcome.exec.<id>.trigger-hash` | instances that a `kind: exec` with `triggers:` ran on |
| `user.tink.run.command` | `user.tink.outcome.run.command` | instances made by `tink run` |
| `user.tink.helper` (the marker) | `user.tink.outcome.helper` | the helper instance |
| `user.tink.helper.pool`, `.remote`, `.image`, `.image-fingerprint`, `.status-volume` | the same names under `user.tink.outcome.helper.` | the helper instance |
| `user.tink.helper.status` | `user.tink.outcome.helper.status` | the helper's status volume (an older helper: the helper instance) |

Two intent keys are named and not written yet: `user.tink.intent.resource` and `user.tink.intent.applied`. A server will not have them.

### Time words (#305)

Already decided and written by the time vocabulary change; they are here because the same release carries them, and the key ones are in the table above (the `copied-at`, `failed-at`, `failures` and `copy-partial-started-at` rows).

| Old | New |
|---|---|
| status document `tick` | `written-at` |
| status document `started` | `started-at` |
| status document `heartbeat_seconds` (a number) | `heartbeat` (a Go duration string, `10m0s`) |
| status document `last_job.finished` | `finished-at` |
| status document `failing[].since` (it held the last attempt) | `failed-at`; `since` now means when the run of failures began |
| jobs documents `created`, `started`, `finished` | `created-at`, `started-at`, `finished-at` |
| the helper's heartbeat file `time` | `written-at` |

The status document is protocol 2 and the jobs directory protocol 2. A document with no write time is now **degraded**, whoever wrote it.

### The underscore keys (#307)

tink now spells every key it defines in a JSON document with hyphens, never an underscore ([JSON keys](../glossary.md#json-keys)). These are the ones that changed:

| Document | Old key | New key |
|---|---|---|
| the helper's status document (`user.tink.outcome.helper.status`, and the `status` member of `tink helper status --json`) | `job_proto` | `job-proto` |
| | `policy_proto` | `policy-proto` |
| | `last_job` | `last-job` |
| | `running_jobs` | `running-jobs` |
| | `queued_jobs` | `queued-jobs` |
| `tink helper status --json` (one object per helper) | `instance_state` | `instance-state` |
| | `exit_code` | `exit-code` |
| | `status_volume` | `status-volume` |
| a backup run's summary (`summary` in a job's `status.json`) | `other_servers` | `other-servers` |
| | `restore_point` | `restore-point` |
| a backup-run job's arguments (`args` in its `request.json`) | `dry_run` | `dry-run` |

(The status document's `heartbeat_seconds` is in the time table above.) **Left as they are**, because another tool owns the spelling: `access_token` in a container registry's token response, and `display_name` in the Headscale API; and Incus's own words (`created_at`, `expires_at`). The underscore keys of the stack language (`depends_on`, `on_image_change`, ...) are YAML that a person writes, and are not part of this change.

**What this does to a server.** Nothing on the Incus server is renamed by hand for these: the only stored document with such keys is the helper's status document, and the upgrade in step 3 replaces it. Three things follow:

- **A script that reads `tink helper status --json` by an old name gets no value, and no error.** `jq '.[0].exit_code'` prints `null`. A cron job, a monitor or a Home Assistant sensor that treats a missing value as zero, or as "nothing to report", goes on saying the helper is well while it is not. This is the one that matters; fix it before the script is put back.
- **The status document the old helper left is read as degraded**, as step 3 says (it has no write time under the new name), and the upgrade in step 3 replaces it. Do not edit the old document into the new shape.
- **Jobs from the old tink.** A queued request is protocol 1 and fails with "unsupported protocol" (its schedule retries it, so a `dry_run` request is simply gone). The `summary` in the `status.json` of a job that already finished keeps the old names; nothing reads it, and the job directory drops it by its retention (the newest 50 finished jobs, and any from the last 14 days). Leave those files alone.

#### Find the readers

Do this after the new tink is installed and before step 6's last item puts a host cron entry or unit back. The readers are text on the host, not objects on the server. Search the places a script or a unit would live, and the instances that run a monitor, for the old names:

```sh
OLD='instance_state|exit_code|status_volume|last_job|job_proto|policy_proto|running_jobs|queued_jobs|other_servers|restore_point|dry_run'
# the host: files that mention tink and an old name (add your own script, unit and config directories)
grep -rlE 'tink' /etc/cron.d /etc/cron.daily /etc/cron.hourly /etc/systemd/system /usr/local/bin ~ 2>/dev/null | xargs -r grep -nE "$OLD"
crontab -l 2>/dev/null | grep -nE "$OLD"
# an instance that polls the helper (a Home Assistant config, a monitoring agent): name it, then look
incus exec MONITOR --project PROJECT -- grep -rnE "$OLD" /config /etc 2>/dev/null
```

`exit_code` is an ordinary word in other tools, so read every hit: only the ones that read the output of `tink helper status --json`, a status document or a job file are to be changed. A hit in a file you did not expect is a reason to ask the owner, not to edit it.

#### Fix them

For each reader, take a copy, change the key, and run the reader once by hand. A key written in quotes is a plain replacement; a key written after a dot has to take brackets (a hyphen is not a name in `jq` or in a Home Assistant template):

| Reader | Old | New |
|---|---|---|
| `jq` | `.[0].exit_code` | `.[0]["exit-code"]` |
| `jq` | `.[0].status.last_job.state` | `.[0].status["last-job"].state` |
| a Home Assistant template | `value_json[0].exit_code` | `value_json[0]['exit-code']` |
| Python, JavaScript, a quoted key in any language | `"exit_code"` | `"exit-code"` |

For the quoted form in a file you have looked at and decided to change, a loop does it (it touches only `"key"` and `'key'`, and `FILE.orig` keeps the old text):

```sh
FILE=/usr/local/bin/helper-check
cp -p "$FILE" "$FILE.orig"
for k in instance_state exit_code status_volume last_job job_proto policy_proto running_jobs queued_jobs other_servers restore_point dry_run; do
  new=$(echo "$k" | tr _ -)
  sed -i "s/\"$k\"/\"$new\"/g; s/'$k'/'$new'/g" "$FILE"
done
diff "$FILE.orig" "$FILE"
```

Read the `diff`: it is the whole change. The dotted forms in the table are not touched by it; do those by hand.

#### Check

- The find above prints nothing that reads tink's output.
- After step 3, the helper's output has no underscore key. Neither command should print anything (the second after the helper's first heartbeat, which can take up to its `heartbeat`):

  ```sh
  tink helper status --json | grep -E '"[a-z]+_[a-z_]+":'
  incus storage volume get POOL VOLUME user.tink.outcome.helper.status --project PROJECT | grep -E '"[a-z]+_[a-z_]+":'
  ```
- Run each fixed reader once by hand and compare what it prints with `tink helper status` (a healthy helper gives `exit-code` 0, `instance-state` `Running`). Then, for a monitor, make it see a failure on purpose (for example stop the helper's instance for a minute, outside the hours copies run) and check that it reports one, because a reader that silently gets `null` looks the same as a working one until then.

## What happens if nothing is done

- **Objects stamped under the old names read as unstamped.** `tink plan` ends with its line about declared objects that have no stamp, and the old keys are listed as "not in the stack" (they are undeclared keys under `user.tink.` that belong to no class).
- **The publish, site and derive stamps are not read from the old names.** `tink join --from-server` and the daemon's reconcile of derived files see no publishes until the stamps are written again by `tink plan apply`. `join apply dns --from-server` refuses on zero publishes rather than write an empty zone, but a derived file is not kept until then.
- **A helper installed before the change is not found**, because its marker key moved: `tink helper status` says there is none, and `tink backup run` runs in the foreground instead of handing off.
- **The copy policy is not found.** Nothing is scheduled for a volume whose policy is under the old name, so **copies silently stop** until the policy is renamed or the stack is applied.
- **Restore points are not recognised.** tink only ever touches a volume that carries a restore point's marker, and the marker moved. After the change an existing restore point is not deleted, but restore does not list it, pruning does not manage it, and the newest backup looks like none. They are recognised again as soon as the markers are renamed. Nothing is lost by waiting, but a backup run before they are renamed makes new restore points that carry the new markers and leaves the old ones outside the retention, so **do the rename before the first backup run**.
- **An exec with `triggers:` runs once more** at the next apply, because its hash is not found.
- The helper's status document in the old shape is read as degraded until the helper is upgraded.

## Order of work

Install the new tink on the host first, **and run none of `tink plan apply`, `tink backup run` or a `tink helper` command from it until the step that says to.** If a host cron entry or unit runs one, stop it before step 1 and put it back after step 6.

1. Stop the old helper, so nothing writes an old name while you rename.
2. Re-mark the volumes and the restore points.
3. Rename what the helper instance carries, then upgrade the helper.
4. Write the intent stamps with `tink plan apply --adopt`, then an ordinary `tink plan apply`.
5. Remove the old keys that are left.
6. Check.

Step 0 below is the reference the others use.

Every command below is meant to be read before it is run. The ones that change something are printed first, written to a file, and run from the file.

### 0. The renaming expression and how to find what is left

Save this as `rename.jq` in the directory you work from. It gives the new name of an old key and nothing for any other key:

```jq
def new:
  if . == "user.tink.stack" or . == "user.tink.site" or test("^user\\.tink\\.(publish|derive)\\.") then
    sub("^user\\.tink\\."; "user.tink.intent.")
  elif . == "user.tink.backup.policy" then "user.tink.intent.backup.policy"
  elif . == "user.tink.backup.copy-at" then "user.tink.outcome.backup.copied-at"
  elif . == "user.tink.backup.copy-partial-at" then "user.tink.outcome.backup.copy-partial-started-at"
  elif test("^user\\.tink\\.backup\\.copy\\.") then
    sub("^user\\.tink\\.backup\\."; "user.tink.outcome.backup.")
    | sub("\\.fail\\.at$"; ".failed-at") | sub("\\.fail\\.n$"; ".failures") | sub("\\.at$"; ".copied-at")
  elif test("^user\\.tink\\.backup\\.") then sub("^user\\.tink\\.backup\\."; "user.tink.outcome.backup.")
  elif test("^user\\.tink\\.(exec|run|helper)(\\.|$)") then sub("^user\\.tink\\."; "user.tink.outcome.")
  else empty end;
```

Find every object that still has an old key (this prints nothing on a server that has none, and is the check at the end):

```sh
# instances, profiles, projects, networks, network ACLs (all projects where the kind has them)
incus list --all-projects -f json | jq -r -L . 'include "rename"; .[] | . as $o | .config | keys[] | select(new) | "instance \($o.project)/\($o.name): \(.)"'
incus profile list --all-projects -f json | jq -r -L . 'include "rename"; .[] | . as $o | .config | keys[] | select(new) | "profile \($o.project)/\($o.name): \(.)"'
incus project list -f json | jq -r -L . 'include "rename"; .[] | . as $o | .config | keys[] | select(new) | "project \($o.name): \(.)"'
incus network list -f json | jq -r -L . 'include "rename"; .[] | . as $o | .config | keys[] | select(new) | "network \($o.name): \(.)"'
incus network acl list --all-projects -f json | jq -r -L . 'include "rename"; .[] | . as $o | .config | keys[] | select(new) | "acl \($o.project)/\($o.name): \(.)"'
# images (the stamp is in the properties)
incus image list --all-projects -f json | jq -r -L . 'include "rename"; .[] | . as $o | .properties | keys[] | select(new) | "image \($o.fingerprint): \(.)"'
# volumes: every project, every pool, on this server and on each remote that is a backup target (REMOTE: with the colon; "" for this server)
for remote in "" ; do
  for pool in $(incus storage list ${remote} -f json | jq -r '.[].name'); do
    for project in $(incus project list ${remote} -f json | jq -r '.[].name'); do
      incus storage volume list ${remote}${pool} --project ${project} -f json | jq -r -L . --arg id "${remote}${pool}/${project}" \
        'include "rename"; .[] | select(.type == "custom") | . as $o | .config | keys[] | select(new) | "volume \($id)/\($o.name): \(.)"'
    done
  done
done
```

A network ACL's `--all-projects` and an image's list may differ on your Incus; if one of them errors, list per project (`--project NAME`). A remote that is a backup target needs `remote` set to `REMOTE:` in the loop; run the loop once with `""` and once per remote.

### 1. Stop the old helper

Skip this step if the server has no helper. Find it (the marker is still the old key until step 3), and note its name and project:

```sh
incus list --all-projects -f json | jq -r '.[] | select(.config | has("user.tink.helper")) | "\(.project)/\(.name)\t\(.status)\tmarker=\(.config["user.tink.helper"])"'
```

A copy that is running now would finish with old names after you have renamed. A volume with `user.tink.backup.copy-partial-of` is a copy in progress or one cut off. Stop the helper's instance, and rerun the find in step 0 after step 2 to catch whatever the old helper wrote last:

```sh
incus stop HELPER --project HELPER_PROJECT
```

(Stopping it is safe: the work queue is a directory that survives, a job that was running is marked failed and its schedule retries it. Do it outside the hours when copies run.)

### 2. Re-mark the volumes and the restore points

This is the careful step. Restore points are recognised by their markers; tink never touches a volume without one. This loop renames, on every custom volume, every old tink key that has a new name: the restore-point markers, the stamps `backup run` and `backup verify` left on the source, and the copy policy (without which the scheduler stops copying). It sets the new key before it removes the old one, so a volume is never without its marker. It does not touch `user.tink.stack`, which step 4 writes.

First print what it would do and read it, and compare the count of restore points with what you expect:

```sh
: > rename-volumes.sh
for remote in "" ; do                       # "" = this server; repeat with REMOTE: for each remote that is a backup target
  for pool in $(incus storage list ${remote} -f json | jq -r '.[].name'); do
    for project in $(incus project list ${remote} -f json | jq -r '.[].name'); do
      incus storage volume list ${remote}${pool} --project ${project} -f json | jq -r -L . \
        --arg pool "${remote}${pool}" --arg project "${project}" \
        'include "rename"; .[] | select(.type == "custom") | . as $v | .config | to_entries[]
         | select(.key | startswith("user.tink.backup.")) | select(.key | new)
         | "incus storage volume set \($pool) \($v.name) \(.key | new) \(.value | @sh) --project \($project) && incus storage volume unset \($pool) \($v.name) \(.key) --project \($project)"' \
        >> rename-volumes.sh
    done
  done
done
wc -l rename-volumes.sh; grep -c 'copy-of ' rename-volumes.sh   # lines, and restore points among them
less rename-volumes.sh
```

Then run it and check that every restore point has the new marker and none has the old one:

```sh
sh -e rename-volumes.sh
# no old backup key may be left on any volume (the find in step 0, volumes only, must print nothing)
# and each restore point carries the new marker: count them
for pool in $(incus storage list -f json | jq -r '.[].name'); do
  for project in $(incus project list -f json | jq -r '.[].name'); do
    incus storage volume list ${pool} --project ${project} -f json | jq -r --arg id "${pool}/${project}" \
      '.[] | select(.type == "custom") | select(.config | has("user.tink.outcome.backup.copy-of")) | "\($id)/\(.name)\t\(.config["user.tink.outcome.backup.copy-of"])\t\(.config["user.tink.outcome.backup.copied-at"])"'
  done
done
```

Do the same for each remote that is a backup target (the `${remote}` of the loops). The count must be the number of restore points you had before; a restore point that appears in neither list lost its markers some other way, and tink will not recognise it: stop and ask the owner.

### 3. Rename what the helper instance carries

The helper is found by its marker, so rename the marker and its siblings on the instance, and the status document on its status volume if you want `tink helper status` to show something before the upgrade. The instance and, if it has one, its status volume (`user.tink.helper.status-volume` names it as `POOL/VOLUME`):

```sh
HELPER=helper; PROJECT=tink-helper          # your helper's name and project
incus config show ${HELPER} --project ${PROJECT} | grep 'user.tink.helper'      # see what is there
for k in "" .pool .remote .image .image-fingerprint .status-volume; do
  v=$(incus config get ${HELPER} "user.tink.helper${k}" --project ${PROJECT}) || continue
  [ -n "$v" ] || continue
  incus config set ${HELPER} "user.tink.outcome.helper${k}" "$v" --project ${PROJECT} && incus config unset ${HELPER} "user.tink.helper${k}" --project ${PROJECT}
done
# the status document: on the volume the pointer names, else (an older helper) on the instance itself
incus storage volume get POOL VOLUME user.tink.helper.status --project ${PROJECT}
```

The status document is protocol 1, so a new tink reads it as degraded (it has no write time under the new name) until the helper is upgraded; there is no reason to copy it. If you do want it visible meanwhile, `incus storage volume set POOL VOLUME user.tink.outcome.helper.status "$(incus storage volume get POOL VOLUME user.tink.helper.status --project ${PROJECT})" --project ${PROJECT}`. The marker's value (the job protocol the helper was installed with) is kept as it is: only its presence is read.

Then upgrade the helper, which replaces its tink, keeps its volumes, certificate and key, and starts it. A queued job from the old tink fails with "unsupported protocol" and its schedule retries it. Read `tink helper upgrade --help` first.

```sh
tink helper status      # now finds the helper; degraded or "speaks job protocol 1" is expected
tink helper upgrade     # a release build: to the image published for its own version
tink helper status      # healthy, the new job protocol, after its first heartbeat
```

The upgraded helper writes its status under `user.tink.outcome.helper.status`. The old status key is left on the status volume (or the instance); remove it in step 5.

### 4. Write the intent stamps

The owner stamp, site, publish and derive stamps are written by `tink plan apply`, not renamed:

```sh
tink plan apply --adopt STACK.yaml    # once per stack: stamps the declared objects that exist and read as unstamped, restarts nothing
tink plan apply STACK.yaml            # writes the publish, site and derive stamps under the new names
tink plan STACK.yaml                  # no changes, and no line about unstamped objects
```

Run it for every stack the server holds. An object that is stamped nowhere, because no stack file declares it, keeps the old key until you remove it in step 5, or rename it by hand with the expression above (`incus config set NAME user.tink.intent.stack VALUE`) if you want it to keep an owner. An exec with `triggers:` runs once more at this apply.

### 5. Remove the old keys that are left

Run the find in step 0. What it lists now is the old stamps that were written under the new names in step 4, the helper's old status key, and any old key on an object no stack declares. Check that each has a new counterpart, and then unset it. For one object:

```sh
incus config unset NAME user.tink.stack user.tink.publish.web --project PROJECT        # an instance, several keys at once
incus storage volume unset POOL VOLUME user.tink.stack --project PROJECT
incus profile unset NAME user.tink.stack --project PROJECT
incus project unset NAME user.tink.stack
incus network unset NAME user.tink.stack
incus network acl unset NAME user.tink.derive.ingress --project PROJECT
incus image unset-property FINGERPRINT user.tink.stack
incus storage volume unset POOL HELPER-STATUS-VOLUME user.tink.helper.status --project PROJECT
```

An exec's old `user.tink.exec.<id>.trigger-hash` and a run command's `user.tink.run.command` are the same: if you want the exec not to run once more, rename the hash to the new name first (`incus config set NAME user.tink.outcome.exec.<id>.trigger-hash VALUE`), then unset the old one. If the stack declares one of these keys in `config:` itself, remove it from the stack too: the new tink refuses every intent and outcome key in a declaration, and the next load says which.

### 6. Check

- The find in step 0 prints nothing.
- `tink plan STACK.yaml` says no changes for every stack, with no line about unstamped objects and no "not in the stack" list naming a `user.tink.` key.
- **Every restore point is listed and restorable.** For each volume with copies, ask for a restore point that does not exist: the error names the ones that do. `--from` names a `kind: backup-target` in the stack, so give the stack with `-f`.

  ```sh
  tink backup restore VOLUME --from TARGET -f STACK.yaml --snapshot none --as check-listing
  ```

  The ones it lists must be the ones you counted in step 2. Then restore the newest into a scratch volume, look at it, and delete it:

  ```sh
  tink backup restore VOLUME --from TARGET -f STACK.yaml --as check-restore
  incus storage volume show POOL check-restore --project PROJECT     # has data, and carries no restore-point marker
  incus storage volume delete POOL check-restore --project PROJECT
  ```
- `tink helper status` is healthy, and a few minutes after the next scheduled copy `tink plan` no longer warns that copies are overdue.
- `tink join --from-server` prints what it printed before the change.
- Put back the host cron entry or unit you stopped before step 1.

## Do not

- Do not write code to read the old keys again, or a command to convert them. The owner chose spending an agent's time over lines of code.
- Do not run `tink backup run`, `tink plan apply` or a `tink helper` command from the new tink before the step above that says to.
- Do not delete a volume because it looks like a stray copy: a restore point that lost its marker is still a backup. Ask the owner.
- Do not change an instance that is not tink's, or a project the owner did not name.
- Do not put a real host name, domain or address in a commit message, an issue or this page. Use `example.org` and the documentation address blocks.
