package resolve

import "github.com/minihci/tink/internal/finding"

// The reasons of the findings a plan raises (PlannedResource.Findings). A reason is a fixed phrase that names the kind of problem: it is written once, here,
// it never contains a name, a count or an error text, and it is the key a test, a status document or a notification can group by. The detail of a finding
// is what says it in words for a person, and may name anything.
//
// Two findings that mean the same thing share a reason; a reason is not made per call site. The phrases are in the order of the files that raise them.
const (
	// ReasonImageDrift is the reason of a confirmed image drift. It is a warning that a plan prints under its own label ("drift:"), apart from the others,
	// and that an apply does not repeat, so the printer and apply name it.
	ReasonImageDrift = "image drift"

	reasonImageDriftIgnored    = "image drift ignored"
	reasonImageUnverified      = "image could not be verified"
	reasonRebuildUnverified    = "rebuild will not act on an unverified image"
	reasonImageDriftBlocks     = "image drift with on_image_change: report"
	reasonWithheldChanges      = "changes withheld until the instance matches the YAML"
	reasonProfilesMissing      = "profiles are not on the instance"
	reasonSecretUnresolved     = "a secret could not be resolved"
	reasonSecretUnexpanded     = "a secret reference was never expanded"
	reasonHelperCannotRead     = "the helper cannot read the copy policy"
	reasonIncusOnRemote        = "kind: incus cannot run against a remote"
	reasonNetworkNotManaged    = "the network is not managed by Incus"
	reasonNetworkNotBridge     = "the network is not a bridge"
	reasonNetworkNotCreated    = "the network is not Created"
	reasonNetworkDisruption    = "changing the network would interrupt its instances"
	reasonNetworkAffectsUsers  = "changing the network affects its instances"
	reasonAuthzTwoNarrowers    = "more than one helper asks to narrow the authorizer"
	reasonAuthzNotGiven        = "narrowing was asked of a tink that cannot do it"
	reasonAuthzCannotNarrow    = "the daemon's certificate cannot be narrowed"
	reasonAuthzUnknown         = "whether the certificate is narrowed is unknown"
	reasonAuthzNotNarrowed     = "the certificate is not narrowed by tink"
	reasonAuthzNarrowed        = "the certificate is narrowed"
	reasonAuthzNarrowedUnasked = "the certificate is narrowed and the stack does not say so"
	reasonAuthzStale           = "the narrowing is out of date"
	reasonAuthzUnrestricted    = "the certificate is unrestricted"
	reasonHelperNameTaken      = "an instance of the helper's name is not a helper"
	reasonHelperNoTimezone     = "the helper has no timezone"
	reasonHelperRemoteToAdd    = "the helper will lack a remote"
	reasonHelperRemoteMissing  = "the helper has no remote the stack needs"
	reasonHelperState          = "the daemon helper's state"
	reasonHelperVersion        = "the daemon helper's version"
	reasonHelperProxyAPI       = "the helper reaches the API through a proxy device"
	reasonBackupPolicy         = "the backup policy cannot be built"
	reasonBackupTargetUnknown  = "a copy target is not known to the planner"
	reasonBackupSameDomain     = "a copy is in the volume's own failure domain"
	reasonBackup321            = "3-2-1 is not met"
	reasonBackupHelperRemote   = "copies go to a remote the helper does not have"
	reasonBackupUndeclared     = "no backup is declared"
	reasonBackupNoneButSet     = "backup: none but a schedule is set"
	reasonVolumeOtherStack     = "the volume is stamped by another stack"
	reasonVolumeCreationOnly   = "a creation-only key differs"
	reasonACLUnchecked         = "a network ACL could not be checked"
	reasonACLMissing           = "a network ACL does not exist"
	reasonOCINICNoName         = "an OCI NIC has no name"
	reasonNATProxyNotIP        = "a NAT proxy connects to something that is not an IP address"
	reasonNATProxyNotStatic    = "a NAT proxy connects to an address the instance does not hold"
	reasonProxyOverlap         = "proxy listeners overlap"
	reasonNoRootDisk           = "an instance has no root disk"
	reasonMixedProjects        = "a resource names no project where others do"
	reasonFileDeferred         = "a file is left for the next apply"
	reasonFileInVolume         = "a file is placed in a volume"
	reasonFileSeedDiffers      = "a seed file differs from its declaration"
	reasonFileNotPlacedFirst   = "a file is not placed before the first start"

	reasonRebuildNotContainer  = "rebuild supports containers only"
	reasonRebuildNotOCI        = "rebuild is for OCI containers only"
	reasonSnapshotsUnlisted    = "the instance's snapshots could not be listed"
	reasonSnapshotsExist       = "the instance has snapshots"
	reasonRebuildNoDataVolume  = "the instance has no data volume"
	reasonRebuildImageNotOCI   = "the image is not on an OCI remote"
	reasonRebuildConfigUnread  = "the new image's runtime config could not be read"
	reasonRebuildConfigChanges = "the new image changes runtime config"
	reasonRebuildImageUnsized  = "the new image could not be sized"
	reasonRebuildPoolUnread    = "the pool's free space could not be read"
	reasonRebuildPoolTooSmall  = "the pool has too little free space"
)

// details is the text of each finding, in order.
func details(fs []finding.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Detail)
	}
	return out
}
