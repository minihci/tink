# The helper

The helper is an Incus instance that runs `tink daemon run` next to the data: the backup scheduler, the job executor and (later) the ingress
reconcile. The design is in `docs/helper-design.md` (PR #16); this page covers what exists: **`tink helper install` and `remove`, the status
document, and the commands that read it.** The published image, `upgrade`, the ingress half and `plan` reading the status are later slices.

## Installing it

```
tink helper install                                                       # a release build: the image published for its own version
tink helper install --image ghcr:minihci/tink-helper:v0.1.0               # a particular image (tink at /usr/local/bin/tink)
tink helper install --binary ./tink-linux --timezone America/Denver      # a linux tink binary, in a stock alpine image
```

With no flag, a **release build** installs `ghcr:minihci/tink-helper:<its own version>`: the same binary, built by the same workflow from the same
tag. A development build has no image that matches it, and says so: give `--image` or `--binary`. The instance records the image's fingerprint (an OCI
image's digest) on itself, `user.tink.helper.image-fingerprint`, because a tag can move and a fingerprint cannot.

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

## The ingress half

```
tink helper install --ingress                        # also reconcile the instance named ingress
tink helper install --ingress --ingress-instance edge
```

The helper can do what `tink-daemon` does on the host: discover the instances that register with `user.ingress.{domain,port,enabled}`, render their Caddy
routes, and reload Caddy. It does it **through the ingress instance's file API**, not through a path in the host's storage pool: it reads and writes
`/etc/caddy/routes/generated/` *inside* the ingress instance (where the `ingress-routes` volume is mounted), then runs `caddy reload` in it. Nothing is mounted
into the helper, and nothing depends on the machine the helper runs on, which is also what makes it work under `--remote`. The rendering is the same code the
host path uses, so the files are the same bytes.

- **The same thing is available by hand**, to try it: `tink ingress status --via-api` and `tink ingress reconcile --via-api [--ingress-instance NAME]`, from
  anywhere that can reach the server (`--remote` included). `tink daemon run --ingress-via-api` is what the helper's entrypoint uses.
- **It is opt-in, and the host path is unchanged.** Without `--via-api` everything works as it did, on the host's filesystem.
- **A routes directory it cannot read is an error, never "no routes"**: a failed read taken for an empty directory would rewrite every route.
- **Writes before removals.** It writes the desired files first and removes the stale ones after, so there is never a moment with no routes; Caddy is told to
  reload only once everything is in place. A pass that changes nothing writes nothing and does not reload.
- **One reconciler.** With `--ingress` the instance is marked `user.tink.helper.ingress`, and **`tink deploy` then does not install the host's `tink-daemon`**, which
  would reconcile the same routes and reload the same Caddy from a place that does not know about the helper. If `tink-daemon` is already installed on the host,
  deploy says so and leaves it: disable it (`systemctl disable --now tink-daemon`). A helper without `--ingress` leaves ingress to the host's daemon, as before.
- **A failed pass is retried in 5 seconds**, not after the whole minute: the helper's first pass races its own enrolment.

## Copying to another Incus server

A backup target with `remote: nas2` is copied to by connecting to a remote **named `nas2` in the Incus client configuration of whoever runs the
copy**. For the helper that is the helper's own configuration, and `install` puts exactly one remote in it: `host`, its own server. So a copy to any
other server fails, every time, until the helper is given that remote:

```
incus config trust add helper -q | tink helper remote add nas2 --token-file -     # the token is made ON nas2; it carries nas2's address
tink helper remote add nas2 https://10.0.0.7:8443 --fingerprint SHA256HEX        # a server that already trusts the helper's certificate
tink helper remote list                                                         # what the helper says it can reach
tink helper remote remove nas2
```

- **The name is the stack's name.** `nas2` must be exactly what the backup target says in `remote:`. `host` is reserved for the helper's own server and
  cannot be added or removed this way.
- **The helper does the connecting**, from inside, with its own client certificate (the one `host` already trusts, whose key never left the instance). The
  token is read by this command and handed to the helper on standard input, never on a command line. It is single-use. The other server is told to trust the
  helper's certificate, can see it in its own trust store, and can revoke it there (`incus config trust remove`); `tink helper remote remove` only makes the
  helper forget the server.
- **The server must be reachable from the helper's network**, not from yours. A tunnel on your laptop (`ssh -L ...`, as in
  [volume-backup.md](volume-backup.md#remote-targets)) is not there for the helper; a name that only resolves on your machine will fail in the helper.
  Without a token, give `--fingerprint` or `--accept-certificate`: there is no one to ask inside the helper.
- **`--project`** is the project on that server the remote defaults to, as for `incus remote add`; it is where the copies land.
- **It needs the helper running** (the remote is added by running tink inside it), and each of these reads or changes only the helper's own client configuration.

**What you see when it is missing.** `tink plan` and `tink plan apply` **warn** for every copy to a remote the helper does not have, naming the command to run. It
is a warning, not a block, because the policy and the remote can be set up in either order. If it is left, the helper's scheduler tries the copy and fails: the
copy shows as `failing` in `tink helper status`, the helper is `degraded`, and the job's log says which remote and what to do. Once the remote is added the
next attempt works: the scheduler retries a failing copy with a growing delay (up to the copy's own interval), and a job queued by hand
(`tink daemon enqueue --jobs /data/jobs`, run inside the helper) does not wait for it.

## What `plan` and `apply` do about the helper

- **They tell you when nothing will run the copies.** A stack that declares copies, on a server with **no helper**, ends with a note naming the volumes and
  `tink helper install`. (Something else may be running them, `tink backup run --due` from cron or a `tink daemon run --jobs DIR` unit, which tink cannot see from here;
  the note says so.) With a helper there, that note is not shown, and the next point applies. This replaced a warning that was printed for every volume with copies
  whether or not anything ran them, which is to say a warning nobody read.
- **They tell you when the helper is not well.** `tink plan` and `tink plan apply` end with a note when the server has a helper that is degraded or down
  (skipping a volume, failing a copy, stopped, silent, or without its certificate), the same judgement `tink helper status` makes. A note, never a
  failure, and nothing is said when there is no helper or it is healthy.
- **They will not write a copy policy the helper cannot read.** The status document says the newest policy protocol the helper reads
  (`policy_proto`). If this tink would write a policy of a newer protocol, the volume is **BLOCKED** with the reason, instead of being applied and then
  skipped by the helper without a word: that is how copies would stop and nothing would say so. The way out is `tink helper upgrade`. Nothing is
  checked on a server with no helper, or one that has not said what it reads (an older helper). There is only protocol 1 today, so no helper is older
  than the policies written now; this is the net for the first time that stops being true.
- **They warn about a copy to a remote the helper does not have** (see [above](#copying-to-another-incus-server)): the status document lists the remotes
  the helper can reach, and a copy whose target names another one is warned about, naming `tink helper remote add`. An older helper that does not report
  its remotes is not checked.

## Upgrading it

```
tink helper upgrade                         # a release build: to the image published for its own version
tink helper upgrade --image ghcr:minihci/tink-helper:v0.2.0
tink helper upgrade --binary ./tink-linux   # replace only the binary in the instance
```

Upgrading **drains** the helper first. A file, `/data/jobs/DRAIN`, tells it to stop: the scheduler queues nothing new, the executor starts nothing, a job that
is already running finishes, and queued jobs wait and run after the upgrade. Its status document says `draining` and how many jobs are running
(`tink helper status` shows it as degraded, so a drain that was left behind cannot look healthy). When nothing is running the helper is replaced, the drain
file removed (it lives on the data volume, so it would survive the replacement), and the new helper waited for.

- **No new enrolment.** An image upgrade recreates the instance with the same configuration and devices and **keeps its volumes**, so the certificate and key
  (on the config volume) are still there, and the host still trusts them. A binary upgrade only replaces the file.
- **A job that will not finish.** After `--drain-timeout` (15 minutes) the upgrade gives up and **lifts the drain** instead of interrupting the job.
  `--force` goes on anyway: the job is marked `failed (interrupted)` and its schedule retries the work.
- **A helper that says nothing** cannot be seen to be idle; after 30 seconds without a document the upgrade goes on and says so.
- **If the new helper does not come up**, the old image is named in the error: `tink helper upgrade --image <it>` goes back (the volumes carry the state).

## Removing it

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
| `remotes` | the Incus servers the helper can reach by name: `host`, and any added with `tink helper remote add` (name, address, project; no credential). Absent from an older helper, which is why a missing list is not read as "none" |
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

## The image, and releasing it

`build/helper/Containerfile` is the helper image: the static tink binary in a small base (alpine, pinned by digest) that has CA certificates and a shell,
about 27 MB. It builds no Go itself: the workflow compiles the binaries and the image carries exactly those.

`.github/workflows/helper-image.yml` builds it for **amd64 and arm64**, with the version compiled in (`-X main.injectedVersion=<tag>`, which is what `tink
version` reports and what `tink helper install` uses to pick its image; a build in a container has no `.git`, so nothing else could tell two builds apart).
On a pull request that touches the image it only builds, so a broken Containerfile or workflow is found early. **On a tag it also publishes:**

```
git tag v0.1.0 && git push origin v0.1.0        # publishes ghcr.io/minihci/tink-helper:v0.1.0, multi-arch
```

The first publication creates the package **private**; make it public once in the repository's package settings, so a host can pull it without a login.
To try the image locally: build the binaries into `dist/tink-linux-<arch>` as the workflow does, then `podman build -f build/helper/Containerfile .`.
