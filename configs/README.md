# configs/

`tink deploy`'s config templates — moved here from
`incus-host` (2026-09-18) so a
host only needs one checkout (this repo's) to run `deploy`, not two.
`--repo-root` defaults to this directory; `deploy.env` and the real
secrets below are resolved relative to whatever `--repo-root` points at.

Same split as `incus-host` always had: files here are templates and
`deploy` fills in `${VAR}` placeholders from `deploy.env` before pushing
them — see `internal/bootstrap/render.go`. This directory also mixes in
host-owned, gitignored files at deploy time (`deploy.env`, `secrets/`,
`authelia/users_database.yml`) — those never leave whatever host they're
generated on, same as before.

| path | purpose |
|---|---|
| `deploy.env.example` | per-host values — copy to `deploy.env` (or wherever `--deploy-env` points), fill in, never commit |
| `daemon/server-config.yaml` | Incus server-config template (OIDC, authorization, trusted proxy) |
| `daemon/authorization.star` | the authorization scriptlet — spliced into `server-config.yaml` at deploy time. It only runs for client classes routed to it, and was validated on Incus 7.5.1 only; see [Host authorization](#host-authorization) and its own header comment |
| `incus-ui/incus-ui.profile.yaml` | Incus profile template for the `incus-ui` OCI application container ([`minihci/incus-ui`](https://github.com/minihci/incus-ui) builds the image itself) |
| `authelia/configuration.yml` | Authelia config — safe to commit as-is, see the comment at its top for how secrets and per-host domains get resolved without ever being written here |
| `authelia/authelia.profile.yaml` | Incus profile template for Authelia, official upstream image |
| `authelia/users_database.yml.example` | shape only; the real file has a real password hash and isn't committed |
| `ingress/ingress.profile.yaml` | Incus profile template for the shared `ingress` Caddy instance |
| `ingress/Caddyfile` | the shared public edge — owns `:80`/`:443`, no domain logic of its own, just `import routes/*.caddy` |
| `ingress/routes/*.caddy` | one file per public domain this host hand-maintains (`incus-ui.caddy`/`auth.caddy`); add yours alongside them. Self-registration from config keys was removed: [how to replace it](../docs/removals/ingress-self-registration.md) |

## Host authorization

What `tink deploy` sets on the Incus server, and what that does and does not do.

- **The scriptlet runs only for a client class routed to it.** Since Incus 7.x each class of client has its own server key,
  `authorization.client.unix`, `.tls`, `.tls-restricted`, `.oidc` and `.default`, set to `allow`, `deny`, `tls` (for `tls-restricted` only:
  the certificate's project list), `openfga` or `scriptlet`. Incus's built-in routing is `unix=allow`, `tls=allow`, `tls-restricted=tls`,
  `oidc=allow`, `default=deny`. Under it `authorization.scriptlet` is never called, and OIDC logins and unrestricted certificates are
  already allowed. On such a host the script is inert; to use it you route a class to it, for example
  `incus config set authorization.client.oidc=scriptlet`.
- **`daemon/server-config.yaml` routes two classes to it:** `oidc` and `tls`, and sets `tls-restricted` to the built-in `tls` value (Incus
  7.3+; an older Incus rejects the whole document over those keys). So on a deployed host the script is called for `oidc` and `tls`. It allows
  both and denies anything else, which is what the built-in routing already did for those two. The routing leaves `unix` and `default` on the
  built-in values.
- **A restricted certificate is held to its projects, because that class is not routed to the script.** `tls-restricted: tls` is Incus's own
  check, the certificate's project list. Routing that class to the scriptlet would give it the scriptlet's answer, which is `True` for every
  `tls` client: full access. That was measured on another server with a scriptlet that returned `True`
  ([volume-backup-design.md](../docs/volume-backup-design.md), step 3b), and measured again with this script on Incus 7.5.1 (2026-10-17, an unrestricted and a restricted-to-one-project client certificate over the HTTPS API): with `tls-restricted: tls` the restricted certificate listed only its project and got `403` for another; routed to this script it listed every project and fetched one outside its list. An earlier version of the
  template did route it there; a host deployed with that version keeps it until the next `tink deploy`, which applies the template with
  `incus config edit` and replaces the server's whole config (so it also loses any `incus config set` made by hand). A host that wants
  restricted certificates held to something narrower than their project list routes that class to a script of its own.
- **The script is validated on Incus 7.5.1 only.** The shape of `details` moves between Incus versions: the script used to read
  `details.RequestDetails.Protocol`, which is not a field on 7.5.1 (it reads `details.Protocol`): with the old script an unrestricted certificate got `403 Authorization scriptlet execution failed ... Invalid field "RequestDetails"` on a direct fetch and empty lists on the list endpoints, and with this one it listed and fetched every project (measured 2026-10-17). A wrong field name is not caught by
  `incus config set authorization.scriptlet=...`, only by a request. Re-test on a throwaway server at every Incus upgrade, with a client of
  each routed class. `internal/bootstrap/authorization_star_test.go` pins the 7.5.1 shape and the routing above in CI; it cannot tell you
  the script is right for another version.

**What didn't move here**: `scripts/generate-authelia-secrets.sh` (one-time
per host, mints every secret via Authelia's own CLI) and the reconciler's
reference bash implementation (`reconciler/{reconcile.sh,DESIGN.md}`) are
staying in `incus-host` for now — see that repo's own README for why.
