package domain

import (
	"fmt"
	"slices"
)

// State is the persisted lifecycle state of a Run.
type State string

const (
	StateCreated         State = "CREATED"
	StatePlanned         State = "PLANNED"
	StateReady           State = "READY"
	StateRunning         State = "RUNNING"
	StateRetryPending    State = "RETRY_PENDING"
	StateVerifying       State = "VERIFYING"
	StateReviewPending   State = "REVIEW_PENDING"
	StateReworkRequested State = "REWORK_REQUESTED"
	StatePublishing      State = "PUBLISHING"
	StatePublished       State = "PUBLISHED"
	StateCIPending       State = "CI_PENDING"
	StateAccepted        State = "ACCEPTED"
	StateRejected        State = "REJECTED"
	StateBlocked         State = "BLOCKED"
	StateAborted         State = "ABORTED"
	StateNoChange        State = "NO_CHANGE"
)

var states = []State{
	StateCreated,
	StatePlanned,
	StateReady,
	StateRunning,
	StateRetryPending,
	StateVerifying,
	StateReviewPending,
	StateReworkRequested,
	StatePublishing,
	StatePublished,
	StateCIPending,
	StateAccepted,
	StateRejected,
	StateBlocked,
	StateAborted,
	StateNoChange,
}

// States returns all lifecycle states in stable order.
func States() []State {
	return slices.Clone(states)
}

// ParseState rejects unknown lifecycle states.
func ParseState(value string) (State, error) {
	state := State(value)
	if slices.Contains(states, state) {
		return state, nil
	}
	return "", fmt.Errorf("unknown run state %q", value)
}

// Terminal reports whether no further transition is allowed in the same Run.
func (s State) Terminal() bool {
	switch s {
	case StateAccepted, StateRejected, StateBlocked, StateAborted, StateNoChange:
		return true
	default:
		return false
	}
}
