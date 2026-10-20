# Removed: ingress self-registration

**For an agent (or a person) who finds a server that used it.** tink no longer reads the `user.tink.ingress.*` or the older `user.ingress.*` config keys and no longer has a `tink ingress` command or a reconcile loop in `tink daemon run`. This page says how to recognise a server that depended on it, what it did, and how to replace it by hand. There is no migration code, on purpose: do the steps below, check each one, and do not write code to carry the old shape forward.

Read the whole page before changing anything. Ask the owner before acting on a server that is in use.

## What it did

An instance opted in with config keys on itself:

| Key (old name `user.ingress.*`) | Meaning |
|---|---|
| `user.tink.ingress.enabled` | the string `true` to opt in; anything else is off |
| `user.tink.ingress.domain` | the full public host name, for example `app.example.org` |
| `user.tink.ingress.port` | the port the app listens on; `80` when unset |
| `user.tink.ingress.address` | where to proxy to, a host name or IP with no scheme and no port; when unset, the instance's `eth0` IPv4 address as Incus reports it |

A reconciler (`tink ingress reconcile`, or the ingress half of `tink daemon run`, which ran unless `--no-ingress` was given, on a timer) read every instance in every project, and wrote one Caddy site file per instance into a `generated/` directory beside the ingress instance's routes, then ran `caddy reload`. A domain claimed by two instances was skipped for both. The file name was `<instance>.caddy`, or `<project>_<instance>.caddy` outside the `default` project. The Caddyfile imported `routes/generated/*.caddy`.

**Nothing stops the moment tink is upgraded.** The generated files stay where they are and Caddy keeps serving them. They just stop being updated: a new instance gets no route, a changed port is not followed, and a removed instance keeps its route until the file is deleted by hand.

## Find out whether a server used it

```sh
# instances (any project) that carry either key set
incus list --all-projects -f json | jq -r '
  .[] | select(.config | keys | map(startswith("user.tink.ingress.") or startswith("user.ingress.")) | any)
      | "\(.project)/\(.name)\t\(.config | with_entries(select(.key | test("ingress"))))"'

# generated route files, in the ingress instance
incus exec INGRESS_INSTANCE --project PROJECT -- ls -l /etc/caddy/routes/generated/

# a helper that was started with the ingress flags (they no longer exist)
incus config get HELPER oci.entrypoint --project HELPER_PROJECT   # look for --no-ingress, --ingress-via-api, --routes-dir, --ingress-instance

# a host unit or cron entry from an older `tink deploy`
systemctl status tink-daemon 2>&1 | head -3; crontab -l 2>/dev/null | grep -i tink
```

If the first command prints nothing and the generated directory is empty or absent, the server never used it: only remove the stale flags below, if any.

## A host built with `tink deploy`

`tink deploy` built the ingress instance, installed `tink daemon run` as the host's reconcile daemon (a systemd unit, an OpenRC service or a cron entry, whichever the host's init supported), and set the Caddyfile to import `routes/generated/*.caddy`. Such a host has three things to look at:

- **The daemon.** The unit still runs the build that is installed, and keeps doing so until that build is replaced. Upgrading it to a build without self-registration leaves a unit whose command no longer starts. Do the conversion below first, then remove the unit (`systemctl disable --now tink-daemon`, or the host's equivalent) and delete its unit file.
- **Proxy devices.** If the ingress instance's ports are carried by userspace proxy devices (`connect: tcp:127.0.0.1:80`), every app behind it sees the client as `127.0.0.1`. That is how it was before and is unchanged by this removal; moving to `nat: "true"` proxy devices on a static address is a separate change, and is what keeps the real client address.
- **Keys with the old names.** Apps registered with the older `user.ingress.*` keys, not `user.tink.ingress.*`. The conversion is the same.

Do not run `tink deploy` again from a build that has dropped this: its reconcile-daemon step is removed with the feature, and the Caddyfile template it pushes no longer imports `routes/generated/*.caddy`, so any route still only in that directory would stop being served. Convert them first. The host keeps what deploy built.

## Replace it by hand, one instance at a time

1. **Read the file the reconciler wrote** (`/etc/caddy/routes/generated/<name>.caddy`) and note the domain, the address and the port. It is the source of truth for what was served, more than the keys, which may have changed since.
2. **Write the same route as an ordinary file the Caddyfile already imports**, outside `generated/`, for example `/etc/caddy/routes/<name>.caddy`. The reconciler's template was exactly this, with the three values filled in:

   ```
   app.example.org {
   	encode zstd gzip

   	header {
   		Strict-Transport-Security "max-age=31536000; includeSubDomains"
   		X-Content-Type-Options "nosniff"
   		X-Frame-Options "SAMEORIGIN"
   		Referrer-Policy "same-origin"
   		-Server
   	}

   	reverse_proxy http://192.0.2.10:8080 {
   		header_up X-Real-IP {remote_host}
   	}

   	log {
   		output file /data/access.log {
   			roll_size 10MiB
   			roll_keep 5
   		}
   		format json
   	}
   }
   ```

   Use an address that does not change. If the instance took its address from DHCP, give it a static `ipv4.address` on its NIC first, or the route breaks the next time it moves.
3. **Declare the file in the owner's stack** (a `kind: file` on the ingress instance, plus a `kind: exec` that reloads Caddy, or `restart: true` on the file if a restart is acceptable) so it is not a hand edit on a server. If the owner has no stack for the ingress instance, say so and put the file where the other hand-written routes are.
4. **Check before switching over:** `incus exec INGRESS -- caddy validate --config /etc/caddy/Caddyfile`. A domain in two places is an error to fix, not to ignore: the old file in `generated/` and the new one cannot both stay.
5. **Switch:** delete `generated/<name>.caddy`, then `incus exec INGRESS -- caddy reload --config /etc/caddy/Caddyfile`. Fetch the domain from outside (`curl -sSI https://app.example.org/`) and confirm the response is the app's.
6. **Remove the keys** from the instance: `incus config unset NAME user.tink.ingress.enabled user.tink.ingress.domain user.tink.ingress.port user.tink.ingress.address` (and the `user.ingress.*` names if present), in its project. If the instance is in a stack, remove them from the stack's `config:` too, or the next `tink plan apply` puts them back.
7. When every generated file is converted, remove `import /etc/caddy/routes/generated/*.caddy` from the Caddyfile only if the owner agrees, and the empty `generated/` directory.

## Remove the flags from a running helper

A helper started before the removal has `--no-ingress` or `--ingress-via-api` (and possibly `--routes-dir` and `--ingress-instance`) in its entrypoint. `tink daemon run` no longer accepts them, so an upgraded helper that keeps them will not start. Reinstall the helper without them (`tink helper remove`, then `tink helper install`; read `tink helper remove --help` first for what it keeps, and keep its data volumes). The copy policy is stamped on the volumes being backed up, and the copies are on the targets, so neither lives in the helper. Do it outside the hours when copies run, and check `tink helper status` afterwards.

## Do not

- Do not write code to read the old keys again, or a command to convert them. The owner chose spending an agent's time over lines of code.
- Do not delete `generated/*.caddy` before its replacement is live and answering.
- Do not change an instance that is not tink's, or a project the owner did not name.
- Do not put a real host name, domain or address in a commit message, an issue or this page. Use `example.org` and the documentation address blocks.

## Where this goes next

Once the derive stage lands ([roadmap.md](../roadmap.md), Phase 3), a published name is declared as an offer on the app and a `kind: publish` beside it, and tink writes the route. Until then a hand-written route is the supported way.
