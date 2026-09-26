// Package crashpoint marks named points where a fault-injection build may
// kill the process, so e2e tests can prove recovery between two steps
// (e.g. after publishing an Outbox Event and before recording it).
//
// Normal builds compile Hit to a no-op. A build with the faultinject tag
// (ticket 14) supplies the implementation that exits with code 137 when
// CRASH_AT names the point.
package crashpoint

// Names of the crashpoints, shared by the code that hits them and the tests
// that set CRASH_AT.
const (
	// OutboxBeforePublish: event claimed (lease held), not yet sent to SQS.
	OutboxBeforePublish = "outbox.before_publish"
	// OutboxAfterPublish: event sent to SQS, published_at not yet recorded.
	OutboxAfterPublish = "outbox.after_publish"
	// ConsumerAfterCommit: the operation and its Inbox Message committed,
	// the input message not yet deleted (its redelivery is a duplicate).
	ConsumerAfterCommit = "consumer.after_commit"
)

// PendingAfterClaim: Pending References claimed (lease committed), no retry
// started yet.
const PendingAfterClaim = "pending.after_claim"
