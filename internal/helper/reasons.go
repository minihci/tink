package helper

// The reasons of the findings that judge a helper (Report.Findings). A reason is a fixed phrase that names the kind of problem: it is written once, here,
// it never contains a name, a count or an error text, and it is the key a test, a notification or a status line can group by and send outside the site.
// The detail of a finding is what says it in words for a person, and may name volumes, files and the like.
//
// Two findings that mean the same thing share a reason; a reason is not made per call site. A reason that comes from Evaluate (what needs the server) is
// in the first block, and what JudgeStatus finds in the document is in the second.
const (
	// ReasonNotRunning is the instance being stopped (an error: the helper is down).
	ReasonNotRunning = "the helper's instance is not running"
	// ReasonStale is a document older than 2.5 heartbeats (an error).
	ReasonStale = "the helper has gone quiet"
	// ReasonCertificateNotTrusted is the helper's certificate missing from the host's trust store (an error).
	ReasonCertificateNotTrusted = "the helper's certificate is not trusted"
	// ReasonStatusVolumeUnreadable is a status volume that could not be read (a warning, so the helper is degraded; with a stopped instance it is
	// said as well).
	ReasonStatusVolumeUnreadable = "the status volume cannot be read"
	// ReasonStatusUnreadable is a document that is there and cannot be read: not JSON, no protocol, or one this tink is too old for.
	ReasonStatusUnreadable = "the status document cannot be read"
	// ReasonNoStatus is a running helper that has published nothing.
	ReasonNoStatus = "the helper has published no status document"

	// ReasonNoWriteTime and ReasonNoHeartbeat are a document that cannot be shown to be fresh.
	ReasonNoWriteTime = "the status document has no write time"
	ReasonNoHeartbeat = "the status document has no heartbeat"
	// ReasonVolumeSkipped is a volume the helper will not copy.
	ReasonVolumeSkipped = "a volume is skipped"
	// ReasonCopyFailing is a copy that has failed since its last success.
	ReasonCopyFailing = "a copy is failing"
	// ReasonDerivedBlocked and ReasonDerivedFailing are a derived file the daemon keeps and cannot; ReasonDNSBlocked and ReasonDNSFailing the dns
	// records; ReasonReconcileBlocked and ReasonReconcileFailing any other thing it keeps.
	ReasonDerivedBlocked   = "a derived file is blocked"
	ReasonDerivedFailing   = "a derived file is failing"
	ReasonDNSBlocked       = "the dns records are blocked"
	ReasonDNSFailing       = "the dns records are failing"
	ReasonReconcileBlocked = "a reconcile is blocked"
	ReasonReconcileFailing = "a reconcile is failing"
	// ReasonJobFailed is a last job that failed.
	ReasonJobFailed = "a job failed"
	// ReasonDraining is a helper that is not starting new work.
	ReasonDraining = "the helper is draining"
)
