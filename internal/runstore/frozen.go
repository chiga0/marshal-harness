package runstore

import (
	"fmt"

	"github.com/chiga0/marshal-harness/internal/domain"
)

type FrozenInputs struct {
	SpecDigest       string
	PolicyDigest     string
	CapabilityDigest string
	BaseSHA          string
}

// CheckFrozenInputs returns ErrConflict after a Run reaches READY. The caller
// must create a new Run ID instead of mutating the existing record.
func CheckFrozenInputs(state domain.RunState, next FrozenInputs) error {
	if state.State == domain.StateCreated || state.State == domain.StatePlanned {
		return nil
	}
	if state.SpecDigest != next.SpecDigest || state.PolicyDigest != next.PolicyDigest || state.CapabilityDigest != next.CapabilityDigest || state.BaseSHA != next.BaseSHA {
		return fmt.Errorf("%w: frozen input changed; create a new run", ErrConflict)
	}
	return nil
}
