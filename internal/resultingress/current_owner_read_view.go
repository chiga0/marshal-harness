package resultingress

// CurrentOwnerReadView is a deliberately narrow, non-mutating replay view of
// the current control owner. It exists for a separate fixed Marshal client;
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

func (view *CurrentOwnerReadView) Close() error {
	if view == nil || view.store == nil {
		return nil
	}
	err := view.store.Close()
	view.store = nil
	return err
}
