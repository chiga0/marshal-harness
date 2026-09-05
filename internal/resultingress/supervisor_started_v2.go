package resultingress

import "github.com/chiga0/marshal-harness/internal/processsupervisor"

// SupervisorStartedV2 is the exact v2 subprojection of the existing started
// fact. The legacy handshake must be absent; both shapes cannot coexist.
type SupervisorStartedV2 struct {
	Handshake processsupervisor.HandshakeResponseV2 `json:"handshake"`
	Anchor    processsupervisor.SessionAnchorV2     `json:"anchor"`
}

func supervisorSessionAnchorV2(anchor SupervisorMechanicsAnchor) processsupervisor.SessionAnchorV2 {
	return processsupervisor.SessionAnchorV2{Generation: anchor.Generation, Binding: supervisorObjectBinding(anchor), ControlDirectory: anchor.ControlDirectory}
}

func projectSupervisorMechanicsAnchorV2(anchor processsupervisor.SessionAnchorV2) SupervisorMechanicsAnchor {
	projected := projectSupervisorMechanicsAnchor(anchor.Binding)
	projected.Generation, projected.ControlDirectory = anchor.Generation, anchor.ControlDirectory
	return projected
}

func validateProcessSupervisorStartedV2(started ProcessSupervisorStarted) error {
	v := started.V2
	h, a := v.Handshake, v.Anchor
	if started.Handshake != (processsupervisor.HandshakeResponse{}) || started.Owner.Validate() != nil ||
		requireDigest("launchAuthorizedFactDigest", started.LaunchAuthorizedFactDigest) != nil || requireDigest("bootstrapPreparedFactDigest", started.BootstrapPreparedFactDigest) != nil ||
		started.ControlDirectory != a.ControlDirectory || h.OwnerEpoch != started.Owner.OwnerEpoch || a.Binding.OwnerEpoch != started.Owner.OwnerEpoch {
		return ErrAttemptAuthorityConflict
	}
	peer := processsupervisor.CoreIdentity{UID: a.Binding.UID, GID: a.Binding.GID, Process: h.SupervisorProcess, Binary: h.SupervisorBinary}
	if processsupervisor.ValidateInitialHandshakeBindingV2(h, a, peer) != nil || validateAuthorityObservedAt(h.ObservedAt, h.SupervisorProcess) != nil {
		return ErrAttemptAuthorityConflict
	}
	return nil
}

func NewProcessSupervisorStartedV2FromBootstrap(preparedFactDigest string, prepared SupervisorBootstrapPrepared, handshake processsupervisor.HandshakeResponseV2, anchor processsupervisor.SessionAnchorV2, observed processsupervisor.CoreIdentity) (ProcessSupervisorStarted, error) {
	if processsupervisor.ValidateInitialHandshakeBindingV2(handshake, anchor, observed) != nil {
		return ProcessSupervisorStarted{}, ErrAttemptAuthorityConflict
	}
	started := ProcessSupervisorStarted{Owner: prepared.Owner, LaunchAuthorizedFactDigest: prepared.LaunchAuthorizedFactDigest, BootstrapPreparedFactDigest: preparedFactDigest,
		ControlDirectory: anchor.ControlDirectory, V2: SupervisorStartedV2{Handshake: handshake, Anchor: anchor}}
	if validateStartedV2BootstrapBinding(started, preparedFactDigest, prepared) != nil {
		return ProcessSupervisorStarted{}, ErrAttemptAuthorityConflict
	}
	return started, nil
}

func validateStartedV2BootstrapBinding(started ProcessSupervisorStarted, preparedFactDigest string, prepared SupervisorBootstrapPrepared) error {
	if started.Validate() != nil || prepared.Validate() != nil || prepared.Request.Generation != processsupervisor.DormantV2ProtocolContract() ||
		started.BootstrapPreparedFactDigest != preparedFactDigest || started.Owner != prepared.Owner || started.LaunchAuthorizedFactDigest != prepared.LaunchAuthorizedFactDigest {
		return ErrAttemptAuthorityConflict
	}
	r, a := prepared.Request, started.V2.Anchor.Binding
	if a.SessionID != r.SessionID || a.SessionNonceDigest != r.SessionNonceDigest || a.Authority != r.Authority || a.OwnerEpoch != r.OwnerEpoch || a.CurrentAuthorityHead != r.CurrentAuthorityHead ||
		a.UID != r.Core.UID || a.GID != r.Core.GID || a.FixedBinary != r.Core.Binary || !sameStableControlDirectoryIdentity(started.ControlDirectory, prepared.ControlDirectory) {
		return ErrAttemptAuthorityConflict
	}
	return nil
}

// The current-ledger check remains here, not inside a self-consistent client
// receipt. Constructor validation alone cannot authorize this transition.
func validateStartedV2AgainstProjection(in *Ingress, prior AttemptAuthorityState, exists bool, transition AttemptTransition, owner ControlOwnerState) error {
	started := transition.SupervisorStarted
	h := started.V2.Handshake
	if !exists || prior.Owner != started.Owner || prior.ControlOwnerBindingDigest == "" || prior.SupervisorBootstrapDigest == "" ||
		transition.Identity.AuthorityNamespaceID != started.Owner.Scope.AuthorityNamespaceID ||
		validateStartedV2BootstrapBinding(started, prior.SupervisorBootstrapDigest, prior.SupervisorBootstrap) != nil ||
		owner.Acquisition.OwnerBinary != h.SupervisorBinary || owner.Acquisition.OwnerUID != started.ControlDirectory.UID || owner.Acquisition.OwnerGID != started.ControlDirectory.GID ||
		sameSupervisorProcess(owner.Acquisition.OwnerProcess, h.SupervisorProcess) {
		return ErrAttemptAuthorityConflict
	}
	key, err := transition.Identity.Key()
	if err != nil {
		return err
	}
	for otherKey, state := range in.attempts {
		if otherKey != key && state.SupervisorStartedDigest != "" && supervisorStartedObjectsConflict(state.SupervisorStarted, started) {
			return ErrAttemptAuthorityConflict
		}
	}
	return nil
}

// Cross-generation historical ABA checks compare only object identities.
// This is not a conversion of v2 evidence into a v1 authority object.
func supervisorStartedObjectsConflict(left, right ProcessSupervisorStarted) bool {
	ls, lp, lo := left.Handshake.SessionID, left.Handshake.SupervisorProcess, left.Handshake.ControlSocket
	if left.V2 != (SupervisorStartedV2{}) {
		ls, lp, lo = left.V2.Handshake.SessionID, left.V2.Handshake.SupervisorProcess, left.V2.Handshake.ControlSocket
	}
	rs, rp, ro := right.Handshake.SessionID, right.Handshake.SupervisorProcess, right.Handshake.ControlSocket
	if right.V2 != (SupervisorStartedV2{}) {
		rs, rp, ro = right.V2.Handshake.SessionID, right.V2.Handshake.SupervisorProcess, right.V2.Handshake.ControlSocket
	}
	return ls == rs || sameSupervisorProcess(lp, rp) || sameControlObject(left.ControlDirectory.Device, left.ControlDirectory.Inode, right.ControlDirectory.Device, right.ControlDirectory.Inode) || sameControlObject(lo.Device, lo.Inode, ro.Device, ro.Inode)
}
