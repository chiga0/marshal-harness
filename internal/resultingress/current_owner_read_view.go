package resultingress

// CurrentOwnerReadView is a deliberately narrow, non-mutating replay view of
// the current control owner and initial team facts. It exists for a separate fixed Marshal client;
// unlike DurableStore it exposes no append or lifecycle mutation methods.
type CurrentOwnerReadView struct {
	store *DurableStore
}

func (view *CurrentOwnerReadView) OpenOwner(scope ControlOwnerScope) (ControlOwnerState, bool, error) {
	if view == nil || view.store == nil {
		return ControlOwnerState{}, false, ErrResultIngressClosed
	}
	return view.store.OpenOwner(scope)
}

// ReadTeamPlan replays through the existing read-only descriptors/LOCK_SH. It
// cannot create a missing ledger or acquire mutation authority.
func (view *CurrentOwnerReadView) ReadTeamPlan(scope ControlOwnerScope, goalID string) (TeamPlanState, bool, error) {
	if view == nil || view.store == nil {
		return TeamPlanState{}, false, ErrResultIngressClosed
	}
	return view.store.ReadTeamPlan(scope, goalID)
}

func (view *CurrentOwnerReadView) ReadTeamOutcome(scope ControlOwnerScope, goalID string) (TeamDeliveryOutcome, bool, error) {
	if view == nil || view.store == nil {
		return TeamDeliveryOutcome{}, false, ErrResultIngressClosed
	}
	return view.store.ReadTeamOutcome(scope, goalID)
}

func (view *CurrentOwnerReadView) Close() error {
	if view == nil || view.store == nil {
		return nil
	}
	err := view.store.Close()
	view.store = nil
	return err
}
