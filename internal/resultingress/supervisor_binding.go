package resultingress

// AttemptSupervisorBindingCurrent classifies an already replayed Attempt's
// binding; it is not an owner capability. Callers must still hold/recheck the
// physical current owner and authenticate the exact live journal/peer.
func AttemptSupervisorBindingCurrent(state AttemptAuthorityState) bool {
	if state.ControlOwnerBindingRevision < 2 || state.ControlOwnerBindingRevision > state.Revision || state.ControlOwnerBindingDigest == "" || state.SupervisorBoundAuthorityHead == "" {
		return false
	}
	if state.SupervisorBoundAuthorityHead == state.ControlOwnerBindingDigest {
		return true
	}
	// Initial bind precedes ProcessStarted. Resume legitimately advances the
	// mechanics head without manufacturing an owner-successor binding.
	initial := state.SupervisorStarted.V2.Anchor
	return state.SupervisorStartedDigest != "" && state.SupervisorBoundAuthorityHead == state.SupervisorStartedDigest &&
		initial.Validate() == nil && state.SupervisorMechanicsAnchor.Validate() == nil &&
		initial.Generation == state.SupervisorMechanicsAnchor.Generation &&
		initial.Binding.OwnerEpoch == state.Owner.OwnerEpoch && state.SupervisorMechanicsAnchor.OwnerEpoch == state.Owner.OwnerEpoch &&
		state.SupervisorMechanicsAuthorityHead == state.SupervisorMechanicsAnchor.CurrentAuthorityHead
}
