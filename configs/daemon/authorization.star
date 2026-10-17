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
# project list. `tink deploy`'s template routes oidc and tls here (Incus 7.3+),
# so on a deployed host it is called for those two; it sets tls-restricted to
# the built-in tls value, so a restricted certificate keeps its project list.
#
# THE POLICY, which is the coarse version: trust anyone who made it through
# the OIDC login (Authelia) or who holds a trusted TLS client certificate
# (added with `incus config trust add`). Neither is a new class of access:
# someone with admin access had to grant that trust deliberately. Anything
# that is neither oidc nor tls is denied, so do not route a class here whose
# clients must be allowed otherwise (the local unix socket, for one).
#
# DO NOT route authorization.client.tls-restricted here. A restricted
# certificate would no longer be held to its project list: this script only
# looks at the protocol, which is tls for any certificate, so it answers True.
# docs/volume-backup-design.md measured a restricted certificate seeing every
# project through a scriptlet that returned True, and the same was measured on
# Incus 7.5.1 with this script routed to that class (2026-10-17): a certificate
# restricted to one project listed every project and fetched one outside its
# list. The deploy template keeps that class on Incus's own check:
#   authorization.client.tls-restricted: tls
# A host that wants a restricted certificate held to something narrower than its
# project list routes that class to a script of its own, one that tells
# certificates apart by details.Username (the certificate's fingerprint).
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
