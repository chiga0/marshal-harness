package processsupervisor

import (
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	AttachSchemaV2            = "marshal.process-supervisor-attach.v2"
	AttachObservationSchemaV2 = "marshal.process-supervisor-attach-observation.v2"
)

// AttachAuthorityV2 retains the whole generation. Owner/child coordinates
// are generation-neutral; neither a v1 handshake nor v1 wire is synthesized.
type AttachAuthorityV2 struct {
	PreviousSupervisor     SessionAnchorV2        `json:"previousSupervisor"`
	Supervisor             ProcessIdentity        `json:"supervisor"`
	CurrentAcquisition     AttachOwnerAcquisition `json:"currentAcquisition"`
	CurrentOwnerBoundFact  AttachOwnerBoundFact   `json:"currentOwnerBoundFact"`
	Child                  ProcessIdentity        `json:"child"`
	ChildObservationDigest string                 `json:"childObservationDigest"`
}

func (a AttachAuthorityV2) Validate() error {
	p, c := a.PreviousSupervisor.Binding, a.CurrentAcquisition
	if a.PreviousSupervisor.Validate() != nil || p.JournalSequence != 2*p.CommandSequence+1 {
		return ErrConflict
	}
	// Reuse only the common business-coordinate check, not protocol decoding.
	common := AttachAuthority{PreviousSupervisor: p, Supervisor: a.Supervisor, CurrentAcquisition: c,
		CurrentOwnerBoundFact: a.CurrentOwnerBoundFact, Child: a.Child, ChildObservationDigest: a.ChildObservationDigest}
	if common.validate() != nil || c.OwnerEpoch < p.OwnerEpoch || c.OwnerUID != p.UID || c.OwnerGID != p.GID ||
		!sameBinaryObject(c.OwnerBinary, p.FixedBinary) || c.OwnerProcess == a.Supervisor || a.Child == a.Supervisor {
		return ErrConflict
	}
	return nil
}

type attachRequestV2 struct {
	SchemaVersion               string            `json:"schemaVersion"`
	ProtocolRevision            string            `json:"protocolRevision"`
	LaunchChildProtocolRevision string            `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string            `json:"mechanicsIdentity"`
	SessionNonce                string            `json:"sessionNonce"`
	Core                        CoreIdentity      `json:"core"`
	Authority                   AttachAuthorityV2 `json:"authority"`
	RequestDigest               string            `json:"requestDigest"`
}

type attachRequestDigestInputV2 struct {
	SchemaVersion               string            `json:"schemaVersion"`
	ProtocolRevision            string            `json:"protocolRevision"`
	LaunchChildProtocolRevision string            `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string            `json:"mechanicsIdentity"`
	SessionNonceDigest          string            `json:"sessionNonceDigest"`
	Core                        CoreIdentity      `json:"core"`
	Authority                   AttachAuthorityV2 `json:"authority"`
}

func (r attachRequestV2) detachedDigest() (string, error) {
	return digestValue(attachRequestDigestInputV2{r.SchemaVersion, r.ProtocolRevision, r.LaunchChildProtocolRevision, r.MechanicsIdentity,
		r.Authority.PreviousSupervisor.Binding.SessionNonceDigest, r.Core, r.Authority})
}

func (r attachRequestV2) validateProjection() error {
	if !validV2Binding(r.SchemaVersion, AttachSchemaV2, r.ProtocolRevision, r.LaunchChildProtocolRevision, r.MechanicsIdentity) || r.Authority.Validate() != nil {
		return ErrConflict
	}
	a := r.Authority.CurrentAcquisition
	want := CoreIdentity{UID: a.OwnerUID, GID: a.OwnerGID, Process: a.OwnerProcess, Binary: a.OwnerBinary}
	digest, err := r.detachedDigest()
	if r.Core != want || err != nil || digest != r.RequestDigest {
		return ErrConflict
	}
	return nil
}

func (r attachRequestV2) validate() error {
	if r.validateProjection() != nil || !hex64Pattern.MatchString(r.SessionNonce) || canonical.DigestBytes([]byte(r.SessionNonce)) != r.Authority.PreviousSupervisor.Binding.SessionNonceDigest {
		return ErrConflict
	}
	return nil
}

type attachResponseV2 struct {
	SchemaVersion               string              `json:"schemaVersion"`
	ProtocolRevision            string              `json:"protocolRevision"`
	LaunchChildProtocolRevision string              `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string              `json:"mechanicsIdentity"`
	Status                      string              `json:"status"`
	ReasonCode                  string              `json:"reasonCode"`
	RequestDigest               string              `json:"requestDigest"`
	Handshake                   HandshakeResponseV2 `json:"handshake"`
	Authority                   AttachAuthorityV2   `json:"authority"`
	ObserverIdentity            string              `json:"observerIdentity"`
	ObservedAt                  string              `json:"observedAt"`
	ResponseDigest              string              `json:"responseDigest,omitempty"`
}

func (r attachResponseV2) detachedDigest() (string, error) {
	r.ResponseDigest = ""
	return digestValue(r)
}

func (r attachResponseV2) validate(request attachRequestV2, peer CoreIdentity) error {
	if request.validateProjection() != nil || !validV2Binding(r.SchemaVersion, AttachObservationSchemaV2, r.ProtocolRevision, r.LaunchChildProtocolRevision, r.MechanicsIdentity) ||
		r.Status != "ok" || r.ReasonCode != "process-supervisor-attached" || r.Authority != request.Authority || r.RequestDigest != request.RequestDigest ||
		r.ObserverIdentity != observerIdentityV2 || r.Handshake.Reconciliation != "" || r.Handshake.ReplayedResponse != nil ||
		ValidateHandshakeBindingV2(r.Handshake, r.Authority.PreviousSupervisor, peer) != nil || r.Handshake.SupervisorProcess != r.Authority.Supervisor {
		return ErrConflict
	}
	at, err := time.Parse(time.RFC3339Nano, r.ObservedAt)
	handshakeAt, handshakeErr := time.Parse(time.RFC3339Nano, r.Handshake.ObservedAt)
	if err != nil || handshakeErr != nil || at.Location() != time.UTC || at.Format(time.RFC3339Nano) != r.ObservedAt || at.Before(handshakeAt) {
		return ErrConflict
	}
	digest, err := r.detachedDigest()
	if err != nil || digest != r.ResponseDigest {
		return ErrConflict
	}
	return nil
}

// AttachObservationV2 is the only value allowed to escape a borrowed Attach.
// Its validation does not contact a peer or grant current-owner authority.
type AttachObservationV2 struct {
	Response attachResponseV2 `json:"response"`
	Peer     CoreIdentity     `json:"peer"`
}

func (o AttachObservationV2) Validate() error {
	a := o.Response.Authority.CurrentAcquisition
	request := attachRequestV2{SchemaVersion: AttachSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2,
		MechanicsIdentity: mechanicsIdentityV2, Core: CoreIdentity{UID: a.OwnerUID, GID: a.OwnerGID, Process: a.OwnerProcess, Binary: a.OwnerBinary},
		Authority: o.Response.Authority, RequestDigest: o.Response.RequestDigest}
	return o.Response.validate(request, o.Peer)
}
