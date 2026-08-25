package writegate

import (
	"fmt"

	"github.com/chiga0/marshal-harness/internal/outbox"
)

// trackedEntry stores the commit-observed fields that the outbox's public
// read API does not expose per-entry (requestDigest, idempotencyKey,
// sequence, receiptDigest). Populated via ObserveCommit after each
// successful outbox.Commit call.
type trackedEntry struct {
	requestDigest  string
	idempotencyKey string
	sequence       int64
	receiptDigest  string
}

// OutboxVerifierAdapter adapts *outbox.Outbox to OutboxVerifier. It
// combines the outbox's public read API (entry existence, dispatch state,
// result state) with a commit-observation log that captures per-entry
// binding fields the outbox does not export.
type OutboxVerifierAdapter struct {
	obx     *outbox.Outbox
	entries map[string]trackedEntry
}

// NewOutboxVerifierAdapter creates a real verifier wrapping the given
// outbox. The caller must call ObserveCommit after every successful
// outbox.Commit to populate the per-entry binding cache.
func NewOutboxVerifierAdapter(obx *outbox.Outbox) *OutboxVerifierAdapter {
	return &OutboxVerifierAdapter{
		obx:     obx,
		entries: make(map[string]trackedEntry),
	}
}

// ObserveCommit records the binding fields from a successful outbox
// commit. The idempotencyKey is the original request's idempotencyKey;
// the receipt is the Commit return value.
func (a *OutboxVerifierAdapter) ObserveCommit(idempotencyKey string, rcp outbox.Receipt) {
	a.entries[rcp.CommandId] = trackedEntry{
		requestDigest:  rcp.RequestDigest,
		idempotencyKey: idempotencyKey,
		sequence:       rcp.Sequence,
		receiptDigest: ComputeReceiptDigest(
			rcp.CommandId, rcp.Sequence, rcp.FactDigest, rcp.RequestDigest),
	}
}

// VerifyBinding checks the proof against the outbox's committed state and
// the commit-observation log. Returns an error tagged with the appropriate
// RejectionReason label on any mismatch.
func (a *OutboxVerifierAdapter) VerifyBinding(proof Proof, kind MutationKind) error {
	_, exists := a.obx.Entry(proof.CommandId)
	if !exists {
		return fmt.Errorf("%s: commandId %q not committed in outbox",
			ReasonNotCommitted, proof.CommandId)
	}

	tracked, trackedOk := a.entries[proof.CommandId]
	if !trackedOk {
		return fmt.Errorf("%s: commandId %q has no observed commit record",
			ReasonNotCommitted, proof.CommandId)
	}

	if proof.RequestDigest != tracked.requestDigest {
		return fmt.Errorf("%s: requestDigest mismatch for commandId %q",
			ReasonDigestMismatch, proof.CommandId)
	}
	if proof.IdempotencyKey != tracked.idempotencyKey {
		return fmt.Errorf("%s: idempotencyKey mismatch for commandId %q",
			ReasonDigestMismatch, proof.CommandId)
	}
	if proof.ExpectedSequence != tracked.sequence {
		return fmt.Errorf("%s: expected sequence %d but entry has %d",
			ReasonSequenceMismatch, proof.ExpectedSequence, tracked.sequence)
	}
	if proof.ReceiptDigest != tracked.receiptDigest {
		return fmt.Errorf("%s: receiptDigest mismatch for commandId %q",
			ReasonDigestMismatch, proof.CommandId)
	}

	switch kind {
	case MutateDispatch:
		if !a.obx.IsDispatched(proof.CommandId) {
			return fmt.Errorf("%s: commandId %q not yet dispatched",
				ReasonNotCommitted, proof.CommandId)
		}
	case MutateResultAccept:
		if _, ok := a.obx.Result(proof.CommandId); !ok {
			return fmt.Errorf("%s: commandId %q has no recorded result",
				ReasonNotCommitted, proof.CommandId)
		}
	}

	return nil
}
