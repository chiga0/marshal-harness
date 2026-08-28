//go:build !darwin

package processsupervisor

import "context"

func RecoverCommittedClose(context.Context, CommittedCloseRecoveryOptions) (CommittedCloseRecoveryEvidence, error) {
	return CommittedCloseRecoveryEvidence{}, ErrUnavailable
}
