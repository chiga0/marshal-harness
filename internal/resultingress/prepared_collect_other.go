//go:build !darwin || !arm64

package resultingress

import (
	"context"
	"errors"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

var ErrPreparedExecutionNotCollectible = errors.New("resultingress: prepared execution is not yet collectible")

type PreparedExecutionTranscript struct {
	Identity          AttemptIdentity
	OutcomeFactDigest string
	Transcript        processsupervisor.CollectedTranscript
}

func (s *DurableStore) CollectPreparedExecution(context.Context, CurrentOwnerLockVerifier, ControlOwnerAcquisition, AttemptIdentity) (PreparedExecutionTranscript, error) {
	return PreparedExecutionTranscript{}, ErrPreparedExecutionUnavailable
}
