//go:build darwin && arm64

package resultingress

import (
	"context"
	"os"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

// Called under the same physical owner and RB1 transaction as Close. Preserve
// bounded transcript objects, but never admit a stopped worker's business result.
func (s *DurableStore) collectStoppedBeforeCloseV2Locked(ctx context.Context, projection *Ingress, state AttemptAuthorityState, owner ControlOwnerState,
	identity AttemptIdentity, directory *os.File, fixedPath string, transport continuationTransportV2, read collectedTranscriptReaderV2, observe preparedJournalObserverV2) (AttemptAuthorityState, error) {
	if state.StopIntent == (AttemptStopIntent{}) {
		return state, nil
	}
	if !stoppedTranscriptCollectible(state) {
		return AttemptAuthorityState{}, ErrPreparedExecutionNotClosable
	}
	closing := state.SupervisorPendingIntentDigest != "" && state.SupervisorPendingIntent.Command == processsupervisor.CommandClose
	for _, checkpoint := range state.SupervisorCommandCheckpoints {
		closing = closing || checkpoint.Evidence.Command == processsupervisor.CommandClose
		if _, err := verifiedCollectOutcomeV2(checkpoint.Evidence); err == nil {
			// Collect is creation-once. A later Terminate may be the latest
			// checkpoint without invalidating the durable transcript receipt.
			// Continue through Close's current-owner/journal boundary, which
			// revalidates the physical transcript objects; do not replay Collect
			// or reread through the historical receipt's stale authority head.
			return state, nil
		}
	}
	if closing {
		// Never insert another command ahead of an already frozen Close.
		return AttemptAuthorityState{}, ErrPreparedExecutionNotClosable
	}
	if _, err := s.collectPreparedExecutionV2Locked(ctx, projection, state, owner, identity, directory, fixedPath, transport, read, observe); err != nil {
		return AttemptAuthorityState{}, err
	}
	key, err := identity.Key()
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	return projection.attempts[key], nil
}
