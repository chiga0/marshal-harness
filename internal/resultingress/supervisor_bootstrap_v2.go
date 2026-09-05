package resultingress

import (
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

// NewSupervisorBootstrapPreparedV2 projects the exact v2 producer input into
// the existing RB1 bootstrap fact. It never upgrades a v1 fact or stores the
// raw nonce; outer attempt-authority and ResultIngress revisions do not change.
func NewSupervisorBootstrapPreparedV2(owner CurrentOwnerBinding, request processsupervisor.BootstrapRequestV2) (SupervisorBootstrapPrepared, error) {
	raw, err := processsupervisor.CanonicalProtocolMessage(request)
	if err != nil || processsupervisor.ValidateDormantV2ProtocolMessage("bootstrap", raw) != nil {
		return SupervisorBootstrapPrepared{}, ErrAttemptAuthorityConflict
	}
	projection := SupervisorBootstrapRequestProjection{
		Generation: processsupervisor.DormantV2ProtocolContract(), SchemaVersion: request.SchemaVersion, ProtocolRevision: request.ProtocolRevision,
		LaunchChildProtocolRevision: request.LaunchChildProtocolRevision, MechanicsIdentity: request.MechanicsIdentity,
		SessionID: request.SessionID, SessionNonceDigest: canonical.DigestBytes([]byte(request.SessionNonce)), OwnerEpoch: request.OwnerEpoch,
		Authority: request.Authority, LaunchAuthorizedFact: request.LaunchAuthorizedFact, CurrentAuthorityHead: request.CurrentAuthorityHead,
		ControlDirectoryIdentity: request.ControlDirectoryIdentity, Core: request.Core,
	}
	digest, err := canonicalDigest(projection)
	if err != nil {
		return SupervisorBootstrapPrepared{}, err
	}
	prepared := SupervisorBootstrapPrepared{ProtocolRevision: request.ProtocolRevision, Owner: owner, LaunchAuthorizedFactDigest: request.LaunchAuthorizedFact,
		SessionID: request.SessionID, SessionNonceDigest: projection.SessionNonceDigest, ControlDirectory: request.ControlDirectoryIdentity,
		SupervisorBinary: request.Core.Binary, Request: projection, BootstrapRequestDigest: digest}
	if err := prepared.Validate(); err != nil {
		return SupervisorBootstrapPrepared{}, err
	}
	return prepared, nil
}

func validateSupervisorBootstrapPreparedV2(prepared SupervisorBootstrapPrepared) error {
	r := prepared.Request
	g := processsupervisor.DormantV2ProtocolContract()
	if r.Generation != g || r.ProtocolRevision != g.ProtocolRevision || prepared.ProtocolRevision != g.ProtocolRevision || r.SchemaVersion != g.BootstrapSchema ||
		r.LaunchChildProtocolRevision != g.LaunchChildProtocolRevision || r.MechanicsIdentity != g.MechanicsIdentity ||
		prepared.Owner.Validate() != nil || !supervisorEvidenceID.MatchString(r.SessionID) || r.OwnerEpoch == 0 || r.OwnerEpoch > maxExactJSONInteger ||
		validateSupervisorAuthorityTuple(r.Authority) != nil || validateControlDirectoryIdentity(r.ControlDirectoryIdentity) != nil ||
		r.Core.UID == 0 || r.Core.UID != r.ControlDirectoryIdentity.UID || r.Core.GID != r.ControlDirectoryIdentity.GID ||
		validateSupervisorProcessIdentity(r.Core.Process) != nil || validateFixedMarshalBinaryIdentity(r.Core.Binary) != nil {
		return ErrAttemptAuthorityConflict
	}
	for _, d := range []string{r.SessionNonceDigest, r.LaunchAuthorizedFact, r.CurrentAuthorityHead, prepared.BootstrapRequestDigest} {
		if requireDigest("v2BootstrapBinding", d) != nil {
			return ErrAttemptAuthorityConflict
		}
	}
	if r.SessionID != prepared.SessionID || r.SessionNonceDigest != prepared.SessionNonceDigest || r.OwnerEpoch != prepared.Owner.OwnerEpoch ||
		r.LaunchAuthorizedFact != prepared.LaunchAuthorizedFactDigest || r.ControlDirectoryIdentity != prepared.ControlDirectory || r.Core.Binary != prepared.SupervisorBinary {
		return ErrAttemptAuthorityConflict
	}
	digest, err := canonicalDigest(r)
	if err != nil || digest != prepared.BootstrapRequestDigest {
		return ErrAttemptAuthorityConflict
	}
	return nil
}
