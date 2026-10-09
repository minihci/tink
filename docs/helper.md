# The helper

The helper is an Incus instance that runs `tink daemon run` next to the data: the backup scheduler, the job executor and, if you ask for it, the
ingress reconcile. It is where tink does work that needs nothing from the host's own filesystem or processes, so it behaves the same under `--remote`.
The design, with its reasoning and the findings behind it, is in [the helper design](https://github.com/minihci/tink/pull/16) (an open pull request: the
document is `docs/helper-design.md` on the `helper-design` branch, not yet on `main`). This page covers what exists: **`tink helper install`, `upgrade` and
`remove`, `remote add|list|remove`, `status`, the status document, the ingress half, the image and its release, and what `plan` and `apply` do with the helper.**

**Not built yet:**
- Retiring `tink daemon install`, which still prints the old ingress-only unit ([daemon-jobs.md](daemon-jobs.md)).

**Run live so far**, on one host (the lab server): a release build of tink installed the `v0.1.0` image with no flags; the scheduler queues and makes the copies
on its own, and the restore point it made verifies; a helper whose process was killed is running again in about six seconds, with a fresh status document and
its job history; and `upgrade` with no flags, from the `v0.1.1` release binary, drained it, moved it from the `v0.1.0` image to the `v0.1.1` one, and kept its
certificate and its job history. **Not yet tried:** a host reboot or an Incus restart, and the helper copying to a second physical server.

## Installing it

```
tink helper install                                                       # a release build: the image published for its own version
tink helper install --image ghcr:minihci/tink-helper:v0.1.0               # a particular image (tink at /usr/local/bin/tink): how any other build installs the published one
tink helper install --binary ./tink-linux --timezone America/Denver      # the escape hatch: a linux tink binary, in a stock alpine image
```

`--binary` is the escape hatch. It runs a binary you built (a development build, or a branch you are trying) in a stock alpine image, so it also works on a
host that cannot pull the helper image.

With no flag, a **release build** installs `ghcr:minihci/tink-helper:<its own version>`: the same binary, built by the same workflow from the same
tag. A development build has no image that matches it, and says so: give `--image` for the published one, or `--binary` to run the build you have. The instance records the image's fingerprint (an OCI
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
  (see "Security" in [the helper design](https://github.com/minihci/tink/pull/16)), and nothing here claims one.

**When it is "down", and how fast you hear it.** `tink helper status` also checks the host's trust store (if it may read it): a helper whose
certificate has been revoked is reported **down at once**, not after its last document has aged. Without that check the helper would look healthy for
25 minutes, because it can no longer write.

**It does not claim health it has not earned.** The helper publishes nothing until its scheduler has completed a pass over the volumes; before that a
"nothing skipped, nothing failing" would be about volumes nobody had looked at. A pass that could not look (the proxy to the host's API comes up a
moment after the helper's process does) is retried in 5 seconds, not after the full minute.

**What it needs:** the host's API listening (`core.https_address`; the loopback address is enough, and `tink deploy` sets one), and a network for the NIC
(`--network`, else the default profile's, else `incusbr0`). It refuses, before creating anything, if either is missing, or if another helper exists on
the server.

**If it dies,** Incus restarts it (`boot.autorestart` is set on the instance). On the lab host a helper whose process was killed with `SIGKILL` was running
again in about six seconds, published a new status document, and still had its job history, which lives on its data volume. An entrypoint that exits over and
over is restarted a limited number of times and then left stopped (ten quick restarts, measured in the design's spike): a stopped helper is what `tink helper
status` reports as down.

**Running it again** is safe: what exists is left alone, and an enrolled helper is not enrolled twice. `--reissue` enrols it again with a fresh key pair
and removes the old certificate. `--binary` puts the binary in the instance, which is also how a helper runs where an image cannot be pulled.

## The ingress half

```
tink helper install --ingress                        # also reconcile the instance named ingress
tink helper install --ingress --ingress-instance edge
```

The helper can do what `tink-daemon` does on the host: discover the instances that register with `user.tink.ingress.{domain,port,enabled}`, render their Caddy
routes, and reload Caddy. It does it **through the ingress instance's file API**, not through a path in the host's storage pool: it reads and writes
`/etc/caddy/routes/generated/` *inside* the ingress instance (where the `ingress-routes` volume is mounted), then runs `caddy reload` in it. Nothing is mounted
into the helper, and nothing depends on the machine the helper runs on, which is also what makes it work under `--remote`. The rendering is the same code the
host path uses, so the files are the same bytes.

- **Registering an app.** An app asks for a route with three keys on its own instance, in a stack's `config:`:

  ```yaml
  config:
    user.tink.ingress.enabled: "true"
    user.tink.ingress.domain: app.example.com
    user.tink.ingress.port: "8080"      # default 80
  ```

  **A machine Incus cannot read the address of** (a virtual machine with no guest agent, such as Home Assistant OS) is skipped with "no address yet", because the address is
  normally read from the instance's `eth0`. Say where it is:

  ```yaml
  config:
    user.tink.ingress.enabled: "true"
    user.tink.ingress.domain: ha.example.com
    user.tink.ingress.port: "8123"
    user.tink.ingress.address: 10.0.142.176   # an IP address or a host name: no scheme, no port
  ```

  The address you set is believed over anything Incus reports, so the machine is routed whether or not Incus can see it. Because it ends up in generated Caddy configuration it
  is held to what a host is (an IPv4 or IPv6 address, or a DNS name); a value that is not one **skips the instance with a warning** and is not replaced by the discovered
  address, since a route to the wrong place is worse than none. A route you set an address for is yours to keep correct: if the machine's address changes, so must the key.

  Everything tink writes or reads on an object's metadata is under `user.tink.*`. These keys used to be `user.ingress.{enabled,domain,port}`; **the old names still
  work**, because instances in other repositories register with them and a reconciler that stopped seeing them would delete their routes. A key under the new name wins
  over the old one, key by key. An instance still on the old names is routed as before and named once: in `tink ingress status`/`reconcile`, in the daemon's log
  (when the set changes, not every pass), and in `tink helper status`. Rename the keys and the notice goes away. The old names will stop being read once the hosts
  that use them have moved.
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

A stack can also say where the server is, as an opt-in ([`address` and `fingerprint` on the target](volume-backup.md#saying-where-the-server-is-optional)). The
bare name stays the default and the helper still goes by the name; what the stack's description adds is the exact command in the warning below, with no token,
for a server that already trusts the helper's certificate: `tink helper remote add nas2 https://10.0.0.7:8443 --fingerprint SHA256HEX`.

**What you see when it is missing.** `tink plan` and `tink plan apply` **warn** for every copy to a remote the helper does not have, naming the command to run. It
is a warning, not a block, because the policy and the remote can be set up in either order. If it is left, the helper's scheduler tries the copy and fails: the
copy shows as `failing` in `tink helper status`, the helper is `degraded`, and the job's log says which remote and what to do. Once the remote is added the
next attempt works: the scheduler retries a failing copy with a growing delay (up to the copy's own interval), and a job queued by hand
(`tink daemon enqueue --jobs /data/jobs`, run inside the helper) does not wait for it.

## Running a backup on the helper

`tink backup run` hands the run to the helper whenever the server has a **healthy** one, from a laptop over `--remote` or on the host itself, and then follows the job
(see [its jobs](#its-jobs)). The copies happen next to the data under the helper's supervision: a laptop that sleeps or loses its network interrupts nothing, and no volume
data passes through it, including copies to another server.

```
tink --remote tron backup run                # the stack's volumes; hands them to the helper and follows; Ctrl-C stops following, not the run
tink --remote tron backup run lib --due      # one volume, only if its copy is due
tink backup run --local                      # run it here, whatever (a copy to another server is relayed through this machine)
tink backup run --helper                     # hand it to the helper or fail; never run it here
```

**What it runs.** The helper has no stack: it runs the copy policies the volumes carry. So a hand-off names the stack's volumes, and is **refused** when what the stack
declares for them is not what they carry (no policy, a different one, a policy that cannot be read, or a second volume of the same name that would be picked up too):
`tink plan apply` first, or `--local` to run the stack as it is. It is also refused, before anything is queued, for a copy to a remote the helper does not have
(`tink helper remote add`).

**When it runs here instead**, saying why: the helper is stopped or stale, has not published a status document, is draining for an upgrade, or speaks another job protocol.
A helper that is merely degraded (a copy failing, a volume skipped) takes the run, since that is when a run by hand is most wanted. With no helper at all it runs here
and says nothing. `--helper` turns each of these into an error.

**One at a time.** The helper runs one job at a time, so a hand-off starts when the jobs ahead of it finish, and says how many there are. The same copy is never made twice at
once; one that is already running is skipped and reported.

**Exit status** is the job's: non-zero if a copy failed or the run was cancelled. Detaching (`Ctrl-C`) exits 0. (A helper older than the one that added this records a cancelled run that left copies unmade as `succeeded`; `tink helper upgrade` fixes that.)

## Its jobs

`tink helper jobs`, `tink helper log ID` and `tink helper cancel ID` are `tink daemon jobs|cancel` for the helper: the same jobs directory, reached through the
helper instance's file API, so they work from any machine that can reach the server (`--remote NAME`), not only inside the helper. They need the helper's instance
to be running, and find it the way `status` does (`--instance` and `--project` choose between several).

```
tink --remote tron helper jobs              # newest first: id, state, kind, origin (schedule or trigger), age, error
tink --remote tron helper log ID            # the job's status document and its log
tink --remote tron helper log -f ID         # keep printing the log until the job is over; exits non-zero if it failed or was cancelled
tink --remote tron helper cancel ID         # takes effect between copies; a copy under way finishes
```

`log -f` reads the job every 2 seconds at first and backs off to every 30 while nothing changes, because **each read is a request to the server that leaves an
event in its log**. It rides out a short gap (the helper restarting, a network blip) and gives up after five minutes of failing reads; the job is not affected
either way. `Ctrl-C` stops following and leaves the job running.

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

**Check that a host can pull it without a login.** `v0.1.0` was public as soon as it was published: a request for its manifest with no credentials returned both
architectures (the image carries the `org.opencontainers.image.source` label, which links the package to this repository). A package can start out private,
though, so after the first publication of anything new, try `podman pull ghcr.io/minihci/tink-helper:TAG` without logging in, and if it is refused change the
package's visibility in the repository's package settings.
To try the image locally: build the binaries into `dist/tink-linux-<arch>` as the workflow does, then `podman build -f build/helper/Containerfile .`.

**The command-line binaries.** `.github/workflows/release.yml` runs on the same tags. It builds tink for macOS and Linux, `amd64` and `arm64`, with the same version
compiled in, as `tink_<version>_<os>_<arch>.tar.gz` (the binary alone), plus a byte-identical copy of each named `tink_<os>_<arch>.tar.gz`, and attaches them to the
GitHub Release for the tag with a `checksums.txt` that lists all eight. Before it
publishes it checks that the checksums match, that there are eight archives and each holds one file, that each version-less archive is identical to its
versioned one, and that the Linux `amd64` binary reports the tag. A tag with a suffix
(`v0.2.0-rc.1`) is published as a pre-release and any other (`v0.1.1`, even before 1.0) as an ordinary release, so `releases/latest` always points at the newest
ordinary one. On a pull request that touches the workflow it only builds and checks. The binaries are not signed: a macOS binary downloaded with a browser is quarantined until `xattr -d
com.apple.quarantine tink`, and one fetched with `curl` is not. A binary from a release is a "release build", so `tink helper install` with no flag installs the
image published for the same tag.

`v0.1.1` was the first release to carry them, and `v0.1.2` the first to carry each archive under a version-less name as well. Observed: the `linux/amd64` binary
in an archive is byte-for-byte the one inside the image (the two workflows build them separately). For `v0.1.2`, `releases/latest/download/tink_darwin_arm64.tar.gz`
returns the archive (it returned 404 for `v0.1.1`, which has only the versioned names), byte-identical to its versioned twin, and each has its own verifying line in
the eight-line `checksums.txt`; the downloaded binary reports `tink v0.1.2`. The URL points at the newest *ordinary* release: a pre-release (a tag with a suffix) is
never "latest".

```
curl -fsSLO https://github.com/minihci/tink/releases/latest/download/tink_darwin_arm64.tar.gz        # the latest release; also darwin_amd64, linux_amd64, linux_arm64
curl -fsSLO https://github.com/minihci/tink/releases/latest/download/checksums.txt
grep ' ./tink_darwin_arm64.tar.gz$' checksums.txt | shasum -a 256 -c -                                # sha256sum -c - on Linux
gh release download --repo minihci/tink --pattern 'tink_darwin_arm64.tar.gz'                          # the same archive with gh (the exact name: '*darwin_arm64*' also fetches the versioned one)
curl -fsSLO https://github.com/minihci/tink/releases/download/v0.1.2/tink_v0.1.2_darwin_arm64.tar.gz   # a particular version
```

