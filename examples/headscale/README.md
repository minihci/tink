# Headscale

The control plane of a private overlay network, as a tink stack: one data volume, one instance, one config file, and a route through the shared ingress.
Devices run the stock Tailscale client and point it at this server (`--login-server`). Draft, written for the reference VPS (`incus.xlii.co`).

```
tink plan headscale.yaml        # read-only
tink plan apply headscale.yaml
```

## What was tried, and what was not

Tried on a throwaway project on the lab host (and the project deleted afterwards), using this stack with a project and an explicit NIC added:

- `tink plan apply` creates the volume, the instance and the config; Headscale **v0.29.4** starts from the pinned digest, creates its Noise key and SQLite database on the
  data volume, and serves `/health` and `/key`. Its CLI works inside the instance.
- A stock Caddy with the route **tink's own renderer generates** for this instance (only the site address changed, to plain HTTP) in front of it, and a real Tailscale client
  (`tailscale/tailscale:stable`, userspace networking) enrolled **through that Caddy** with a pre-auth key: `trial-node` came up online with `100.64.0.1`, using a Tailscale
  relay. So the control protocol's connection upgrade survives `encode zstd gzip` and the generated headers.
- `tink plan headscale.yaml` against the reference VPS (read-only): it would create the three resources and nothing else.

Not tried: HTTPS and the real name (`hs.xlii.co` has no record yet), a client on another network, more than one node, a phone, Headscale's own relay, a policy file,
and a restore of the data volume from a backup.

## Before applying to the reference VPS

1. **A DNS record:** `hs.xlii.co` A -> the VPS's public IP. The existing ingress gets the certificate by HTTP-01; no Cloudflare token is involved.
2. **The VPS's `tink` is too old for the registration keys.** The binary installed there is from 2026-09-17 and reads only the old `user.ingress.*` names, so it will not see
   `user.tink.ingress.*`. Either upgrade it (a release that includes the namespace change) or, until then, give the instance the old names instead. Without one of those the
   instance runs and no route is made.
3. **Edit three literals** if the host differs: the domain (`headscale.yaml` and `config.yaml`) and `trusted_proxies` in `config.yaml` (the bridge the ingress is on).
4. **Clients must be Tailscale 1.80 or newer** (the server logs this minimum).

## Enrolling a first device

Inside the instance the image has no shell; the CLI is the entrypoint binary, and it needs the config passed:

```
incus exec headscale -- /ko-app/headscale -c /etc/headscale.yaml users create NAME
incus exec headscale -- /ko-app/headscale -c /etc/headscale.yaml users list                       # note the user's ID
incus exec headscale -- /ko-app/headscale -c /etc/headscale.yaml preauthkeys create --user ID --expiration 1h
tailscale up --login-server https://hs.xlii.co --authkey KEY
```

Without `-c` the CLI warns "no config file found" and uses defaults. The pre-auth key is a credential: it should not go in a stack or a shell history that is kept.

## Things this stack works around or leaves open

- **The config is at `/etc/headscale.yaml`, not `/etc/headscale/config.yaml`.** The image has no `/etc/headscale` and tink's `kind: file` does not create parent directories
  (the push fails with "Not Found", seen in the trial), so the file goes where the directory exists and the entrypoint says where it is.
- **No policy yet:** with no policy file every device can reach every other. A policy (tags for servers, a group for people) should come before more devices join.
- **Relays are Tailscale's public ones.** They carry only encrypted packets, and only when two devices cannot connect directly. Headscale's own relay needs UDP 3478.
- **Only a local snapshot, so `plan` says 3-2-1 is not met.** The volume is the overlay's identity: losing it means re-enrolling every device. It needs an off-site copy
  (`kind: backup-target` for a second server) before this is relied on.
- **`dns.extra_records`** can serve names like `ha.lab.xlii.co -> 100.x` to every device on the overlay, with no public DNS record. It is empty here.
- **A half-failed create:** in the trial a first apply failed on a config key after the instance was created; running it again updated the instance but did not start it. That is
  tink's behaviour, not this stack's, and worth knowing if an apply of any stack stops partway.
