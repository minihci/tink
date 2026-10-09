# Exploration notes, round three: Audiobookshelf, as a newcomer

2026-10-09. Same lab host, same throwaway project (`tink-play`), a different shape of project: the image is on **ghcr.io** (not Docker Hub), the install wants
**four** volumes (two of them media you already own), and it runs as root in its container. The rule this time: only what the README and `--help` say, the way
someone's first evening would go. Install command, from the project's `docker-compose.yml`: `ghcr.io/advplyr/audiobookshelf:latest`, `13378:80`,
`./audiobooks:/audiobooks ./podcasts:/podcasts ./config:/config ./metadata:/metadata`, restart `unless-stopped`.

## The journey

| Step | What happened |
|---|---|
| `tink run --dry-run` with the compose file translated literally | accepted, no complaint (see N2). Device lines come out in a different order every run (N5) |
| the same, for real | **failed**: `Failed instance creation: ... skopeo ... docker://docker.io/ghcr.io/advplyr/audiobookshelf:latest ... access denied`. tink had said `using docker-oci:ghcr.io/...`: its own fallback, from round two, sent a name that already carried a registry host to Docker Hub (N1, fixed) |
| with `ghcr:advplyr/audiobookshelf:latest` (found in `docs/remote.md`'s table of built-in remotes) and the relative `./` paths | `Invalid devices: Device validation failed for "volume0": Source path must be absolute for local sources`, **after** `created abs-play`. A stopped half-made instance was left, and a re-run would say "already exists" (N2, N4) |
| named volumes for config/metadata, absolute server paths for the media | ran. HTTP 200, `serverVersion 2.37.1`, `isInit:false` (its first-run setup) |
| look inside | the bind-mounted media shows as `nobody:nobody`; as root in the container, `touch /audiobooks/x` is **Permission denied**, `touch /config/x` works (N3). Audiobookshelf writes into its media folders, so this would surface later as a confusing failure |
| `tink export` | clean: `ghcr:advplyr/audiobookshelf:latest@sha256:581d...` verified against the registry and pinned, 4 image-derived keys left out, bind mounts kept as host paths, the two managed volumes exported, the `# created by:` comment. `plan`: no changes |
| **delete the instance and both volumes, then `tink plan apply` the exported file** | 8 seconds: project unchanged, 2 volumes and the instance created. HTTP 200, same version. A second `plan`: no changes. This is the run-then-distill loop closing |

## Findings

- **N1 (mine, fixed in this round).** The `docker-oci:` fallback now looks at the first path segment the way Docker does (a dot, a colon, or `localhost` means a registry host) and
  uses the configured OCI remote for that host: `ghcr.io/advplyr/audiobookshelf:latest` becomes `ghcr:advplyr/audiobookshelf:latest`, `docker.io/library/redis:7` becomes
  `docker-oci:library/redis:7`. A host with no configured remote is left alone so the real "not found" shows, instead of an error about `docker.io/<that host>/...`.
- **N2. A relative host path in `-v` is accepted, and refused by the *server* after the instance exists.** With `--remote` a "host path" is a path on the server, so `./audiobooks`
  from a compose file cannot mean anything. Suggest: `Build` rejects a source with a `/` that does not start with `/`, naming the flag and saying the path is on the server.
- **N3. Bind mounts are unwritable by the app** (unprivileged container, id mapping), and `tink run` has no way to say `shift=true`. Managed volumes are fine. This is the most
  likely second-evening failure for anyone with an existing media library. Suggest at least a docs paragraph; a flag or a default is a design decision (Incus's `shift` needs
  kernel support; a default that is wrong on some hosts is worse than none).
- **N4. A failure after create leaves the instance behind** (stopped, no devices). In `Run` only (the helper and `plan apply` call `Create`/`ApplyConfig` themselves), removing the
  instance it just made on a later failure would match how the volume-less failure in round one already behaved. Named volumes should stay, as Docker keeps them.
- **N5. `-v ...:ro` is silently wrong.** `strings.Cut` on the first `:` makes the container path `/audiobooks:ro` (dry run: `path:/audiobooks:ro`). Docker users type `:ro` by
  reflex. Suggest: understand `:ro` (-> `readonly=true`), accept `:rw`, reject anything else with a message.
- **N6. Dry-run output order is random** (map iteration): `volume2, volume3, proxy0, volume0, volume1`. Sort the lines.
- **N7. Nothing records which stack file made an instance** after `plan apply`: the rebuilt instance has no `user.tink.*` key at all (the `run` stamp is gone with the old instance;
  `plan apply` writes none). `kind: stack` exists for volumes (`user.tink.stack`); instances could carry it too, which would also let `plan` say "this instance belongs to no stack".
- **N8. Good, and worth keeping:** `export` needed no special handling for a non-Docker-Hub registry, mixed bind and managed volumes, or a port mapped to 80; the first
  `plan` after `apply` was clean; and the secret/pinning machinery from round two was not even exercised, because this app takes no secrets.

## State left on tron

Project `tink-play`: `kuma-play` (round one/two), `abs-play` (running, http://tron.local:13378, rebuilt from `export`'s file), volumes `kuma-data`, `abs-config`, `abs-metadata`,
cached images for both, and **host directories `/tmp/abs-media/{audiobooks,podcasts}`** (not an Incus object). Remove with the commands at the end of `explore-uptime-kuma.md`, plus
`incus delete -f abs-play`, the two volumes, the image, and `rm -r /tmp/abs-media`.
