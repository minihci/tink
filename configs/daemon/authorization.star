# Authorization scriptlet for the Incus server. `tink deploy` splices this
# file under the authorization.scriptlet key of the server-config template,
# configs/daemon/server-config.yaml (internal/bootstrap/render.go).
#
# VALIDATED ON INCUS 7.5.1 ONLY. The shape of `details` has moved between
# Incus versions with no changelog entry. This file used to read
# `details.RequestDetails.Protocol` (recorded as correct on 7.4); on 7.5.1
# `details` is flat (fields Certificate, Chain, Claims, IsAllProjectsRequest,
# ProjectName, Protocol, Username) and `RequestDetails` does not exist, so
# that read fails with `Invalid field "RequestDetails"`. Re-test on a
# throwaway server at every Incus upgrade, with a real request: `incus config
# set authorization.scriptlet=...` rejects syntax errors but NOT unknown
# fields, so a wrong field path only shows when a request is made. On an
# earlier version it showed as empty lists (no error) on list endpoints and
# as the real Starlark error on a direct fetch:
#   incus query /1.0/projects/default
#
# THIS ONLY RUNS FOR CLIENT CLASSES ROUTED TO IT. Since Incus 7.x each class
# of client has its own server key, authorization.client.unix | tls |
# tls-restricted | oidc | default, with values allow, deny, tls (tls-restricted
# only: the certificate's project list), openfga or scriptlet. For example:
#   incus config set authorization.client.oidc=scriptlet
# The built-in routing is unix=allow, tls=allow, tls-restricted=tls, oidc=allow,
# default=deny, and with it this script is never called: OIDC and unrestricted
# certificates are already allowed, and a restricted certificate is held to its
# project list. `tink deploy`'s template routes oidc, tls and tls-restricted
# here (Incus 7.3+), so on a deployed host it is called for those three.
#
# THE POLICY, which is the coarse version: trust anyone who made it through
# the OIDC login (Authelia) or who holds a trusted TLS client certificate
# (added with `incus config trust add`). Neither is a new class of access:
# someone with admin access had to grant that trust deliberately. Anything
# that is neither oidc nor tls is denied, so do not route a class here whose
# clients must be allowed otherwise (the local unix socket, for one).
#
# A restricted certificate is NOT held to its project list when
# authorization.client.tls-restricted is routed here: this script only looks
# at the protocol, which is tls for any certificate, so it answers True.
# docs/volume-backup-design.md measured a restricted certificate seeing every
# project through a scriptlet that returned True; this script's answer for
# that class has not been re-measured on 7.5.1. To keep the project list,
# route that class to Incus's own check:
#   incus config set authorization.client.tls-restricted=tls
#
# The routing values above are the whole menu on 7.5.1: none of them is a
# per-identity permission-group model. For narrower than this, look at
# OpenFGA instead of growing this script:
#   https://linuxcontainers.org/incus/docs/main/authorization/
def authorize(details, object, entitlement):
    if details.Protocol == "oidc":
        return True
    if details.Protocol == "tls":
        return True
    return False
