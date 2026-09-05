package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

const (
	controlOwnerAuthorityProtocolRevision = "control-owner-authority/v1"
	controlOwnerFactType                  = "control-owner-acquired"
	supervisorCommandProtocolRevision     = "process-supervisor-command-recovery/v1"
	supervisorCommandIntentFactType       = "process-supervisor-command-intent"
	supervisorCommandOutcomeFactType      = "process-supervisor-command-outcome"
	supervisorReconnectFactType           = "process-supervisor-session-reconnected"
	maxExactJSONInteger                   = uint64(1<<53 - 1)
)

var (
	ErrControlOwnerConflict   = errors.New("resultingress: control owner authority conflict")
	ErrControlOwnerUnknown    = errors.New("resultingress: control owner authority unknown")
	ErrControlOwnerNotCurrent = errors.New("resultingress: control owner is not current")

	ownerHex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ControlOwnerScope is the repository/authority namespace over which one
// ProductionRuntime holds the external owner lock. RepositoryIdentityDigest
// is a stable, non-secret digest; a pathname is not an owner capability.
type ControlOwnerScope struct {
	AuthorityNamespaceID     authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	RepositoryIdentityDigest string                         `json:"repositoryIdentityDigest"`
}

func (scope ControlOwnerScope) Validate() error {
	if err := scope.AuthorityNamespaceID.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrControlOwnerConflict, err)
	}
	if err := requireDigest("repositoryIdentityDigest", scope.RepositoryIdentityDigest); err != nil {
		return fmt.Errorf("%w: %v", ErrControlOwnerConflict, err)
	}
	return nil
}

func (scope ControlOwnerScope) key() (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	return canonicalDigest(scope)
}

func validateSupervisorProcessIdentity(identity processsupervisor.ProcessIdentity) error {
	if identity.PID <= 0 || uint64(identity.PID) > maxExactJSONInteger || identity.BirthSeconds <= 0 || uint64(identity.BirthSeconds) > maxExactJSONInteger || identity.BirthMicroseconds < 0 || identity.BirthMicroseconds >= 1_000_000 || identity.SessionID <= 0 || uint64(identity.SessionID) > maxExactJSONInteger || identity.ProcessGroupID <= 0 || uint64(identity.ProcessGroupID) > maxExactJSONInteger {
		return fmt.Errorf("%w: invalid process birth/session identity", ErrControlOwnerConflict)
	}
	return nil
}

func validateFixedMarshalBinaryIdentity(identity processsupervisor.BinaryIdentity) error {
	if !filepath.IsAbs(identity.CanonicalPath) || filepath.Clean(identity.CanonicalPath) != identity.CanonicalPath || identity.FileType != "regular" || identity.Device == 0 || identity.Inode == 0 || identity.Device > maxExactJSONInteger || identity.Inode > maxExactJSONInteger || identity.Size <= 0 || uint64(identity.Size) > maxExactJSONInteger || identity.LinkCount != 1 || identity.Mode&0o170000 != POSIXFileTypeRegular || identity.Mode&0o111 == 0 || identity.Mode&0o6000 != 0 || requireDigest("rawSHA256", identity.RawSHA256) != nil || !ownerHex40.MatchString(identity.CDHash) || identity.CDHash == strings.Repeat("0", 40) || !ownerHex40.MatchString(identity.SourceHead) || strings.TrimSpace(identity.SelfProfile) == "" {
		return fmt.Errorf("%w: invalid fixed Marshal binary identity", ErrControlOwnerConflict)
	}
	return nil
}

func validateControlDirectoryIdentity(identity processsupervisor.ControlDirectoryIdentity) error {
	if !filepath.IsAbs(identity.CanonicalPath) || filepath.Clean(identity.CanonicalPath) != identity.CanonicalPath || identity.Device == 0 || identity.Inode == 0 || identity.Device > maxExactJSONInteger || identity.Inode > maxExactJSONInteger || identity.FileType != "directory" || identity.UID == 0 || identity.Mode&0o170000 != POSIXFileTypeDirectory || identity.Mode&0o077 != 0 || identity.LinkCount < 2 || identity.LinkCount > maxExactJSONInteger {
		return fmt.Errorf("%w: invalid control directory identity", ErrControlOwnerConflict)
	}
	return nil
}

// sameStableControlDirectoryIdentity implements ADR 0064's initial-to-final
// control-directory relationship. LinkCount is a phase observation: creating
// the nonce, journal and socket may legitimately change it on APFS. Every
// field that identifies the held directory object and its security boundary
// remains exact.
func sameStableControlDirectoryIdentity(left, right processsupervisor.ControlDirectoryIdentity) bool {
	return left.CanonicalPath == right.CanonicalPath &&
		left.Device == right.Device &&
		left.Inode == right.Inode &&
		left.FileType == right.FileType &&
		left.UID == right.UID &&
		left.GID == right.GID &&
		left.Mode == right.Mode
}

func validateControlSocketIdentity(identity processsupervisor.ControlSocketIdentity) error {
	if identity.Device == 0 || identity.Inode == 0 || identity.Device > maxExactJSONInteger || identity.Inode > maxExactJSONInteger || identity.FileType != "socket" || identity.UID == 0 || identity.Mode&0o170000 != 0o140000 || identity.Mode&0o777 != 0o600 || identity.LinkCount != 1 {
		return fmt.Errorf("%w: invalid control socket identity", ErrControlOwnerConflict)
	}
	return nil
}

// ControlOwnerAcquisition is one scope-global epoch allocation. A single
// acquisition may be bound into multiple pending Attempt chains; every such
// binding advances that Attempt head independently while referring to this
// exact fact digest.
type ControlOwnerAcquisition struct {
	Scope            ControlOwnerScope                 `json:"scope"`
	OwnerEpoch       uint64                            `json:"ownerEpoch"`
	OwnerUID         uint32                            `json:"ownerUid"`
	OwnerGID         uint32                            `json:"ownerGid"`
	OwnerProcess     processsupervisor.ProcessIdentity `json:"ownerProcess"`
	OwnerBinary      processsupervisor.BinaryIdentity  `json:"ownerBinary"`
	ObserverIdentity string                            `json:"observerIdentity"`
	ObservedAt       string                            `json:"observedAt"`
}

func (acquisition ControlOwnerAcquisition) Validate() error {
	if err := acquisition.Scope.Validate(); err != nil {
		return err
	}
	if acquisition.OwnerEpoch == 0 || acquisition.OwnerEpoch > maxExactJSONInteger || acquisition.OwnerUID == 0 || validateSupervisorProcessIdentity(acquisition.OwnerProcess) != nil || validateFixedMarshalBinaryIdentity(acquisition.OwnerBinary) != nil || strings.TrimSpace(acquisition.ObserverIdentity) == "" {
		return fmt.Errorf("%w: incomplete control owner acquisition", ErrControlOwnerConflict)
	}
	if err := validateAuthorityObservedAt(acquisition.ObservedAt, acquisition.OwnerProcess); err != nil {
		return err
	}
	return nil
}

// ControlOwnerAcquisitionDigest is the only canonical encoding of a durable
// repository owner acquisition. Delivery and transport code consume this
// digest instead of selecting or re-encoding acquisition fields themselves.
func ControlOwnerAcquisitionDigest(acquisition ControlOwnerAcquisition) (string, error) {
	if err := acquisition.Validate(); err != nil {
		return "", err
	}
	return canonicalDigest(acquisition)
}

type ControlOwnerState struct {
	Acquisition        ControlOwnerAcquisition `json:"acquisition"`
	PreviousFactDigest string                  `json:"previousFactDigest,omitempty"`
	FactDigest         string                  `json:"factDigest"`
}

// ControlOwnerLineageReference identifies one exact durable owner fact in the
// same RB1 scope. It is evidence to be replayed under the current physical
// owner lock, never a bearer capability.
type ControlOwnerLineageReference struct {
	Scope                  ControlOwnerScope `json:"scope"`
	OwnerEpoch             uint64            `json:"ownerEpoch"`
	OwnerFactDigest        string            `json:"ownerFactDigest"`
	OwnerAcquisitionDigest string            `json:"ownerAcquisitionDigest"`
}

func (reference ControlOwnerLineageReference) Validate() error {
	if reference.Scope.Validate() != nil || reference.OwnerEpoch > maxExactJSONInteger || requireDigest("ownerFactDigest", reference.OwnerFactDigest) != nil || requireDigest("ownerAcquisitionDigest", reference.OwnerAcquisitionDigest) != nil {
		return ErrControlOwnerConflict
	}
	return nil
}

type controlOwnerFact struct {
	ProtocolRevision    string                  `json:"protocolRevision"`
	FactType            string                  `json:"factType"`
	Sequence            int64                   `json:"sequence"`
	ScopeKey            string                  `json:"scopeKey"`
	PreviousOwnerEpoch  uint64                  `json:"previousOwnerEpoch"`
	PreviousOwnerDigest string                  `json:"previousOwnerFactDigest,omitempty"`
	Acquisition         ControlOwnerAcquisition `json:"acquisition"`
	Digest              string                  `json:"digest"`
}

type ControlOwnerAppendResult struct {
	State    ControlOwnerState
	Appended bool
}

// CurrentOwnerLockVerifier must hold the one production owner lock for the
// complete callback. The fact itself is evidence, not a bearer capability.
type CurrentOwnerLockVerifier interface {
	WithCurrentOwnerLock(context.Context, ControlOwnerAcquisition, func() error) error
}

func withCurrentOwnerLock(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, fn func() error) error {
	if verifier == nil || fn == nil {
		return ErrControlOwnerNotCurrent
	}
	var gate sync.Mutex
	called, repeated, closed := false, false, false
	var callbackErr error
	verifierErr := verifier.WithCurrentOwnerLock(ctx, acquisition, func() error {
		gate.Lock()
		defer gate.Unlock()
		if closed || called {
			repeated = true
			return ErrControlOwnerNotCurrent
		}
		called = true
		callbackErr = fn()
		return callbackErr
	})
	gate.Lock()
	closed = true
	calledOnce, calledAgain, heldErr := called, repeated, callbackErr
	gate.Unlock()
	if calledAgain {
		return fmt.Errorf("%w: owner verifier invoked callback more than once", ErrControlOwnerNotCurrent)
	}
	if heldErr != nil {
		return heldErr
	}
	if verifierErr != nil || !calledOnce {
		return fmt.Errorf("%w: owner lock rejected or not held", ErrControlOwnerNotCurrent)
	}
	return nil
}

// AcquireOwner allocates one strictly increasing epoch under the production
// owner lock. The fact is appended to the same physical ResultIngress/RB1
// ledger. A crash after this fsync never rolls back or reuses the epoch;
// attempts remain unbound until BindOwnerToAttempt appends their exact heads.
func (s *ingressDurableStore) AcquireOwner(ctx context.Context, verifier CurrentOwnerLockVerifier, expectedEpoch uint64, expectedFactDigest string, acquisition ControlOwnerAcquisition) (ControlOwnerAppendResult, error) {
	if err := acquisition.Validate(); err != nil {
		return ControlOwnerAppendResult{}, err
	}
	if expectedEpoch == 0 && expectedFactDigest != "" || expectedEpoch > 0 && requireDigest("expectedOwnerFactDigest", expectedFactDigest) != nil || acquisition.OwnerEpoch != expectedEpoch+1 {
		return ControlOwnerAppendResult{}, ErrControlOwnerConflict
	}
	var result ControlOwnerAppendResult
	err := withCurrentOwnerLock(ctx, verifier, acquisition, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			key, err := acquisition.Scope.key()
			if err != nil {
				return err
			}
			prior, exists := projection.controlOwners[key]
			if exists && prior.Acquisition == acquisition {
				if prior.Acquisition.OwnerEpoch != expectedEpoch+1 || prior.PreviousFactDigest != expectedFactDigest {
					return ErrControlOwnerConflict
				}
				result.State = prior
				return nil
			}
			if exists {
				if prior.Acquisition.OwnerEpoch != expectedEpoch || prior.FactDigest != expectedFactDigest {
					return ErrControlOwnerConflict
				}
			} else if expectedEpoch != 0 || expectedFactDigest != "" {
				return ErrControlOwnerUnknown
			}
			fact := &controlOwnerFact{ProtocolRevision: controlOwnerAuthorityProtocolRevision, FactType: controlOwnerFactType, Sequence: s.nextSequence, ScopeKey: key, PreviousOwnerEpoch: expectedEpoch, PreviousOwnerDigest: expectedFactDigest, Acquisition: acquisition}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
				return err
			}
			s.nextSequence++
			if err := applyControlOwnerFactValue(*fact, projection); err != nil {
				return fmt.Errorf("resultingress: appended control owner fact failed projection: %w", err)
			}
			result = ControlOwnerAppendResult{State: projection.controlOwners[key], Appended: true}
			return nil
		})
	})
	return result, err
}

// OpenOwner returns the replayed current acquisition for one scope. It does
// not acquire a lock and grants no right to mutate an Attempt or supervisor.
func (s *ingressDurableStore) OpenOwner(scope ControlOwnerScope) (ControlOwnerState, bool, error) {
	key, err := scope.key()
	if err != nil {
		return ControlOwnerState{}, false, err
	}
	projection := newAuthorityProjection()
	var state ControlOwnerState
	var found bool
	err = s.transact(projection, func() error {
		state, found = projection.controlOwners[key]
		return nil
	})
	return state, found, err
}

// WithCurrentOwnerLineage proves that reference is the current owner or one
// of its strict, unbroken predecessors in the same physical RB1 ledger. The
// ledger mutex is released before fn while the external owner lock remains
// held, so callers may reconcile immutable side projections without nesting
// an RB1 transaction.
func (s *ingressDurableStore) WithCurrentOwnerLineage(ctx context.Context, verifier CurrentOwnerLockVerifier, current ControlOwnerAcquisition, reference ControlOwnerLineageReference, fn func(ControlOwnerState) error) error {
	if s == nil || ctx == nil || current.Validate() != nil || reference.Validate() != nil || reference.Scope != current.Scope || fn == nil {
		return ErrControlOwnerConflict
	}
	return withCurrentOwnerLock(ctx, verifier, current, func() error {
		projection := newAuthorityProjection()
		var currentState ControlOwnerState
		if err := s.transact(projection, func() error {
			key, err := current.Scope.key()
			if err != nil {
				return err
			}
			state, ownerFound := projection.controlOwners[key]
			if !ownerFound || state.Acquisition != current || state.FactDigest == "" {
				return ErrControlOwnerNotCurrent
			}
			history := projection.controlOwnerHistory[key]
			var predecessor ControlOwnerState
			var found bool
			if reference.OwnerEpoch != 0 {
				predecessor, found = history[reference.OwnerEpoch]
			} else {
				for _, candidate := range history {
					if candidate.FactDigest == reference.OwnerFactDigest {
						if found {
							return ErrControlOwnerConflict
						}
						predecessor, found = candidate, true
					}
				}
			}
			if !found || predecessor.FactDigest != reference.OwnerFactDigest {
				return ErrControlOwnerConflict
			}
			digest, err := ControlOwnerAcquisitionDigest(predecessor.Acquisition)
			if err != nil || digest != reference.OwnerAcquisitionDigest || predecessor.Acquisition.Scope != current.Scope || (reference.OwnerEpoch != 0 && predecessor.Acquisition.OwnerEpoch != reference.OwnerEpoch) || predecessor.Acquisition.OwnerEpoch > current.OwnerEpoch {
				return ErrControlOwnerConflict
			}
			cursor := state
			for cursor.Acquisition.OwnerEpoch > predecessor.Acquisition.OwnerEpoch {
				prior, ok := history[cursor.Acquisition.OwnerEpoch-1]
				if !ok || cursor.PreviousFactDigest != prior.FactDigest || prior.Acquisition.OwnerEpoch+1 != cursor.Acquisition.OwnerEpoch {
					return ErrControlOwnerConflict
				}
				cursor = prior
			}
			if cursor.FactDigest != reference.OwnerFactDigest {
				return ErrControlOwnerConflict
			}
			currentState = state
			return nil
		}); err != nil {
			return err
		}
		return fn(currentState)
	})
}

type CurrentOwnerBinding struct {
	Scope                          ControlOwnerScope `json:"scope"`
	OwnerEpoch                     uint64            `json:"ownerEpoch"`
	ControlOwnerAcquiredFactDigest string            `json:"controlOwnerAcquiredFactDigest"`
}

// supervisorCommandFact is a recovery sub-chain in the one RB1 ledger. It
// deliberately does not mutate Attempt revision/head: an intent cites the
// already-current business authority and a later business fact cites the
// closed outcome fact, avoiding a request/current-head digest cycle.
type supervisorCommandFact struct {
	ProtocolRevision           string                      `json:"protocolRevision"`
	FactType                   string                      `json:"factType"`
	Sequence                   int64                       `json:"sequence"`
	AttemptKey                 string                      `json:"attemptKey"`
	AttemptRevision            uint64                      `json:"attemptRevision"`
	AttemptAuthorityHead       string                      `json:"attemptAuthorityHead"`
	PreviousRecoveryFactDigest string                      `json:"previousRecoveryFactDigest,omitempty"`
	Intent                     SupervisorCommandIntent     `json:"intent,omitempty,omitzero"`
	Outcome                    SupervisorCommandEvidence   `json:"outcome,omitempty,omitzero"`
	Reconnect                  SupervisorReconnectEvidence `json:"reconnect,omitempty,omitzero"`
	Digest                     string                      `json:"digest"`
}

func (binding CurrentOwnerBinding) Validate() error {
	if binding.Scope.Validate() != nil || binding.OwnerEpoch == 0 || binding.OwnerEpoch > maxExactJSONInteger || requireDigest("controlOwnerAcquiredFactDigest", binding.ControlOwnerAcquiredFactDigest) != nil {
		return ErrControlOwnerConflict
	}
	return nil
}

func currentOwnerMatches(state ControlOwnerState, binding CurrentOwnerBinding) bool {
	return state.Acquisition.Scope == binding.Scope && state.Acquisition.OwnerEpoch == binding.OwnerEpoch && state.FactDigest == binding.ControlOwnerAcquiredFactDigest
}

// WithCurrentOwner keeps the external owner lock held while proving the exact
// latest scope-global fact. The callback may perform an Attempt CAS; the owner
// fact alone never authorizes that CAS.
func (s *ingressDurableStore) WithCurrentOwner(ctx context.Context, verifier CurrentOwnerLockVerifier, binding CurrentOwnerBinding, fn func(ControlOwnerState) error) error {
	if err := binding.Validate(); err != nil || fn == nil {
		return ErrControlOwnerNotCurrent
	}
	state, found, err := s.OpenOwner(binding.Scope)
	if err != nil || !found || !currentOwnerMatches(state, binding) {
		return ErrControlOwnerNotCurrent
	}
	return withCurrentOwnerLock(ctx, verifier, state.Acquisition, func() error {
		current, ok, err := s.OpenOwner(binding.Scope)
		if err != nil || !ok || !currentOwnerMatches(current, binding) || current.Acquisition != state.Acquisition {
			return ErrControlOwnerNotCurrent
		}
		return fn(current)
	})
}

func applyControlOwnerLine(line []byte, in *Ingress, wantSequence int64) error {
	canonicalLine, err := canonical.JSON(line)
	if err != nil || !bytes.Equal(canonicalLine, line) {
		return fmt.Errorf("%w: control owner line is not canonical", ErrControlOwnerConflict)
	}
	var fact controlOwnerFact
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		return fmt.Errorf("%w: %v", ErrControlOwnerConflict, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON value", ErrControlOwnerConflict)
	}
	if fact.ProtocolRevision != controlOwnerAuthorityProtocolRevision || fact.FactType != controlOwnerFactType || fact.Sequence != wantSequence {
		return ErrControlOwnerConflict
	}
	stored := fact.Digest
	fact.Digest = ""
	digest, err := canonicalDigest(fact)
	if err != nil || stored == "" || digest != stored {
		return ErrControlOwnerConflict
	}
	fact.Digest = stored
	return applyControlOwnerFactValue(fact, in)
}

func applyControlOwnerFactValue(fact controlOwnerFact, in *Ingress) error {
	if err := fact.Acquisition.Validate(); err != nil {
		return err
	}
	key, err := fact.Acquisition.Scope.key()
	if err != nil || key != fact.ScopeKey {
		return ErrControlOwnerConflict
	}
	prior, exists := in.controlOwners[key]
	if !exists {
		if fact.PreviousOwnerEpoch != 0 || fact.PreviousOwnerDigest != "" || fact.Acquisition.OwnerEpoch != 1 {
			return ErrControlOwnerConflict
		}
	} else if prior.Acquisition.OwnerEpoch != fact.PreviousOwnerEpoch || prior.FactDigest != fact.PreviousOwnerDigest || fact.Acquisition.OwnerEpoch != prior.Acquisition.OwnerEpoch+1 {
		return ErrControlOwnerConflict
	}
	state := ControlOwnerState{Acquisition: fact.Acquisition, PreviousFactDigest: fact.PreviousOwnerDigest, FactDigest: fact.Digest}
	if in.controlOwnerHistory == nil {
		in.controlOwnerHistory = make(map[string]map[uint64]ControlOwnerState)
	}
	history := in.controlOwnerHistory[key]
	if history == nil {
		history = make(map[uint64]ControlOwnerState)
		in.controlOwnerHistory[key] = history
	}
	if _, exists := history[fact.Acquisition.OwnerEpoch]; exists {
		return ErrControlOwnerConflict
	}
	history[fact.Acquisition.OwnerEpoch] = state
	in.controlOwners[key] = state
	return nil
}

func validateAuthorityObservedAt(value string, birth processsupervisor.ProcessIdentity) error {
	observed, err := time.Parse(time.RFC3339Nano, value)
	birthTime := time.Unix(birth.BirthSeconds, birth.BirthMicroseconds*int64(time.Microsecond)).UTC()
	if err != nil || observed.Location() != time.UTC || observed.Format(time.RFC3339Nano) != value || observed.Before(birthTime) {
		return fmt.Errorf("%w: observedAt is not canonical or precedes process birth", ErrControlOwnerConflict)
	}
	return nil
}

// ProcessSupervisorStarted is the closed authority payload appended only
// after a passive fixed-binary supervisor handshake. It contains no raw nonce,
// argv, environment value, stdin, or transcript bytes.
type ProcessSupervisorStarted struct {
	Owner                       CurrentOwnerBinding                        `json:"owner"`
	LaunchAuthorizedFactDigest  string                                     `json:"launchAuthorizedFactDigest"`
	BootstrapPreparedFactDigest string                                     `json:"bootstrapPreparedFactDigest,omitempty"`
	ControlDirectory            processsupervisor.ControlDirectoryIdentity `json:"controlDirectory"`
	Handshake                   processsupervisor.HandshakeResponse        `json:"handshake,omitempty,omitzero"`
	V2                          SupervisorStartedV2                        `json:"v2,omitempty,omitzero"`
}

func (started ProcessSupervisorStarted) Validate() error {
	if started.V2 != (SupervisorStartedV2{}) {
		return validateProcessSupervisorStartedV2(started)
	}
	if started.Owner.Validate() != nil || requireDigest("launchAuthorizedFactDigest", started.LaunchAuthorizedFactDigest) != nil || validateControlDirectoryIdentity(started.ControlDirectory) != nil || processsupervisor.ValidateHandshakeResponse(started.Handshake) != nil || started.Handshake.OwnerEpoch != started.Owner.OwnerEpoch {
		return fmt.Errorf("%w: invalid process-supervisor-started payload", ErrAttemptAuthorityConflict)
	}
	if started.BootstrapPreparedFactDigest != "" && requireDigest("bootstrapPreparedFactDigest", started.BootstrapPreparedFactDigest) != nil {
		return fmt.Errorf("%w: invalid bootstrap prepared fact digest", ErrAttemptAuthorityConflict)
	}
	if started.ControlDirectory.UID != started.Handshake.ControlSocket.UID || started.ControlDirectory.GID != started.Handshake.ControlSocket.GID || validateControlSocketIdentity(started.Handshake.ControlSocket) != nil {
		return fmt.Errorf("%w: supervisor control owner mismatch", ErrAttemptAuthorityConflict)
	}
	if err := validateAuthorityObservedAt(started.Handshake.ObservedAt, started.Handshake.SupervisorProcess); err != nil {
		return fmt.Errorf("%w: invalid supervisor observedAt", ErrAttemptAuthorityConflict)
	}
	return nil
}

// NewProcessSupervisorStarted admits only a handshake already bound to the
// caller's kernel observation and exact authority anchor. The durable fact
// stores processsupervisor's canonical response model directly, preventing a
// second copy of its session, journal, socket, binary and process contract.
func NewProcessSupervisorStarted(owner CurrentOwnerBinding, launchAuthorizedFactDigest string, controlDirectory processsupervisor.ControlDirectoryIdentity, response processsupervisor.HandshakeResponse, anchor processsupervisor.HandshakeAnchor, observed processsupervisor.CoreIdentity) (ProcessSupervisorStarted, error) {
	if err := processsupervisor.ValidateHandshakeBinding(response, anchor, observed); err != nil {
		return ProcessSupervisorStarted{}, fmt.Errorf("%w: invalid bound supervisor handshake", ErrAttemptAuthorityConflict)
	}
	started := ProcessSupervisorStarted{Owner: owner, LaunchAuthorizedFactDigest: launchAuthorizedFactDigest, ControlDirectory: controlDirectory, Handshake: response}
	if err := started.Validate(); err != nil {
		return ProcessSupervisorStarted{}, err
	}
	return started, nil
}

// NewProcessSupervisorStartedFromBootstrap additionally binds Start's complete
// ConnectionEvidence to the durable pre-launch recovery anchor. ADR 0064
// permits only LinkCount to differ between the prepared initial observation
// and the final setup observation persisted in the started fact. The legacy
// constructor remains replay/source compatible for facts written before ADR
// 0060.
func NewProcessSupervisorStartedFromBootstrap(preparedFactDigest string, prepared SupervisorBootstrapPrepared, connection processsupervisor.ConnectionEvidence, observed processsupervisor.CoreIdentity) (ProcessSupervisorStarted, error) {
	request := prepared.Request
	response, anchor := connection.Handshake, connection.Anchor
	if requireDigest("bootstrapPreparedFactDigest", preparedFactDigest) != nil || prepared.Validate() != nil || connection.ReplayedOutcome != nil || connection.Recovery != nil || processsupervisor.ValidateSessionControlFiles(response.ControlFiles) != nil || anchor.ControlFiles != response.ControlFiles || response.SessionID != prepared.SessionID || response.SessionNonceDigest != prepared.SessionNonceDigest || response.SupervisorBinary != prepared.SupervisorBinary || connection.Core != request.Core || !sameStableControlDirectoryIdentity(prepared.ControlDirectory, connection.ControlDirectory) || anchor.SessionID != request.SessionID || anchor.SessionNonceDigest != request.SessionNonceDigest || anchor.Authority != request.Authority || anchor.OwnerEpoch != request.OwnerEpoch || anchor.CurrentAuthorityHead != request.CurrentAuthorityHead || anchor.UID != request.Core.UID || anchor.GID != request.Core.GID || anchor.FixedBinary != request.Core.Binary {
		return ProcessSupervisorStarted{}, fmt.Errorf("%w: handshake does not match prepared bootstrap", ErrAttemptAuthorityConflict)
	}
	started, err := NewProcessSupervisorStarted(prepared.Owner, prepared.LaunchAuthorizedFactDigest, connection.ControlDirectory, response, anchor, observed)
	if err != nil {
		return ProcessSupervisorStarted{}, err
	}
	started.BootstrapPreparedFactDigest = preparedFactDigest
	if err := started.Validate(); err != nil {
		return ProcessSupervisorStarted{}, err
	}
	return started, nil
}

// ProcessSupervisorClosed binds the exact close intent/receipt and final
// command head to an absence observation for the started supervisor birth.
// cleanup-completed must cite the resulting Attempt fact digest.
type ProcessSupervisorClosed struct {
	ProtocolRevision                   string                                      `json:"protocolRevision"`
	SessionID                          string                                      `json:"sessionId"`
	Owner                              CurrentOwnerBinding                         `json:"owner"`
	SupervisorStartedFactDigest        string                                      `json:"supervisorStartedFactDigest"`
	TerminalizationID                  string                                      `json:"terminalizationId"`
	CleanupBindingDigest               string                                      `json:"cleanupBindingDigest"`
	ProcessTerminalFactDigest          string                                      `json:"processTerminalFactDigest"`
	AllocationTerminatedFactDigest     string                                      `json:"allocationTerminatedFactDigest"`
	CloseIntentDigest                  string                                      `json:"closeIntentDigest"`
	CloseReceiptDigest                 string                                      `json:"closeReceiptDigest"`
	CloseObservationDigest             string                                      `json:"closeObservationDigest,omitempty"`
	FinalCommandHead                   string                                      `json:"finalCommandHead"`
	Mechanics                          SupervisorCommandEvidence                   `json:"mechanics,omitempty,omitzero"`
	SupervisorAbsenceObservationDigest string                                      `json:"supervisorAbsenceObservationDigest"`
	SupervisorProcess                  processsupervisor.ProcessIdentity           `json:"supervisorProcess"`
	ObserverIdentity                   string                                      `json:"observerIdentity"`
	ObservedAt                         string                                      `json:"observedAt"`
	SupervisorAbsence                  SupervisorAbsenceObservation                `json:"supervisorAbsence,omitempty,omitzero"`
	AuthenticatedSupervisorAbsence     processsupervisor.SupervisorAbsenceEvidence `json:"authenticatedSupervisorAbsence,omitempty,omitzero"`
}

type SupervisorAbsenceObservation struct {
	State             string                            `json:"state"`
	SupervisorProcess processsupervisor.ProcessIdentity `json:"supervisorProcess"`
	ObserverIdentity  string                            `json:"observerIdentity"`
	ObservedAt        string                            `json:"observedAt"`
}

func (observation SupervisorAbsenceObservation) Validate() error {
	if observation.State != "absent" || validateSupervisorProcessIdentity(observation.SupervisorProcess) != nil || strings.TrimSpace(observation.ObserverIdentity) == "" || validateAuthorityObservedAt(observation.ObservedAt, observation.SupervisorProcess) != nil {
		return ErrAttemptAuthorityConflict
	}
	return nil
}

func (closed ProcessSupervisorClosed) Validate() error {
	if closed.ProtocolRevision != processsupervisor.ProtocolRevision || strings.TrimSpace(closed.SessionID) == "" || closed.Owner.Validate() != nil || strings.TrimSpace(closed.TerminalizationID) == "" || validateSupervisorProcessIdentity(closed.SupervisorProcess) != nil || strings.TrimSpace(closed.ObserverIdentity) == "" {
		return fmt.Errorf("%w: invalid process-supervisor-closed identity", ErrAttemptAuthorityConflict)
	}
	for name, digest := range map[string]string{
		"supervisorStartedFactDigest":        closed.SupervisorStartedFactDigest,
		"cleanupBindingDigest":               closed.CleanupBindingDigest,
		"processTerminalFactDigest":          closed.ProcessTerminalFactDigest,
		"allocationTerminatedFactDigest":     closed.AllocationTerminatedFactDigest,
		"closeIntentDigest":                  closed.CloseIntentDigest,
		"closeReceiptDigest":                 closed.CloseReceiptDigest,
		"finalCommandHead":                   closed.FinalCommandHead,
		"supervisorAbsenceObservationDigest": closed.SupervisorAbsenceObservationDigest,
	} {
		if err := requireDigest(name, digest); err != nil {
			return fmt.Errorf("%w: %v", ErrAttemptAuthorityConflict, err)
		}
	}
	if err := validateAuthorityObservedAt(closed.ObservedAt, closed.SupervisorProcess); err != nil {
		return fmt.Errorf("%w: invalid supervisor close observedAt", ErrAttemptAuthorityConflict)
	}
	if closed.SupervisorAbsence != (SupervisorAbsenceObservation{}) {
		if closed.AuthenticatedSupervisorAbsence != (processsupervisor.SupervisorAbsenceEvidence{}) {
			return fmt.Errorf("%w: duplicate supervisor absence projections", ErrAttemptAuthorityConflict)
		}
		if closed.SupervisorAbsence.Validate() != nil || closed.SupervisorAbsence.SupervisorProcess != closed.SupervisorProcess || closed.SupervisorAbsence.ObserverIdentity != closed.ObserverIdentity || closed.SupervisorAbsence.ObservedAt != closed.ObservedAt {
			return fmt.Errorf("%w: supervisor absence projection mismatch", ErrAttemptAuthorityConflict)
		}
		digest, err := canonicalDigest(closed.SupervisorAbsence)
		if err != nil || digest != closed.SupervisorAbsenceObservationDigest {
			return fmt.Errorf("%w: supervisor absence digest mismatch", ErrAttemptAuthorityConflict)
		}
	}
	if absence := closed.AuthenticatedSupervisorAbsence; absence != (processsupervisor.SupervisorAbsenceEvidence{}) {
		if absence.Validate() != nil || absence.Expected != closed.SupervisorProcess || absence.ObservedAt != closed.ObservedAt || absence.Observer.Binary.SelfProfile != closed.ObserverIdentity {
			return fmt.Errorf("%w: authenticated supervisor absence projection mismatch", ErrAttemptAuthorityConflict)
		}
		digest, err := canonicalDigest(absence)
		if err != nil || digest != closed.SupervisorAbsenceObservationDigest {
			return fmt.Errorf("%w: authenticated supervisor absence digest mismatch", ErrAttemptAuthorityConflict)
		}
	}
	if !zeroSupervisorCommandEvidence(closed.Mechanics) {
		if closed.Mechanics.Validate() != nil || requireDigest("closeObservationDigest", closed.CloseObservationDigest) != nil || closed.Mechanics.Command != processsupervisor.CommandClose || closed.Mechanics.SessionID != closed.SessionID || closed.Mechanics.RequestDigest != closed.CloseIntentDigest || closed.Mechanics.ReceiptDigest != closed.CloseReceiptDigest || closed.Mechanics.CommandHead != closed.FinalCommandHead || closed.Mechanics.ObservationDigest != closed.CloseObservationDigest || closed.Mechanics.Outcome.State != SupervisorSessionClosed {
			return fmt.Errorf("%w: invalid supervisor close mechanics binding", ErrAttemptAuthorityConflict)
		}
	}
	return nil
}

// ProcessSupervisorCloseAuthority is the business authority surrounding one
// already verified close recovery. It contains no mechanics response fields.
type ProcessSupervisorCloseAuthority struct {
	Owner                          CurrentOwnerBinding
	SupervisorStartedFactDigest    string
	TerminalizationID              string
	CleanupBindingDigest           string
	ProcessTerminalFactDigest      string
	AllocationTerminatedFactDigest string
}

// NewProcessSupervisorClosedFromRecovery converts only producer-authenticated
// committed-Close evidence into the business fact payload. The mechanics
// outcome itself is first appended to the independent command recovery chain.
func NewProcessSupervisorClosedFromRecovery(authority ProcessSupervisorCloseAuthority, recovery processsupervisor.CommittedCloseRecoveryEvidence) (ProcessSupervisorClosed, error) {
	if recovery.Validate() != nil {
		return ProcessSupervisorClosed{}, fmt.Errorf("%w: invalid committed close recovery", ErrAttemptAuthorityConflict)
	}
	absenceDigest, err := canonicalDigest(recovery.Absence)
	if err != nil {
		return ProcessSupervisorClosed{}, err
	}
	outcome := recovery.Outcome
	closed := ProcessSupervisorClosed{
		ProtocolRevision: processsupervisor.ProtocolRevision, SessionID: outcome.Recovery.PreCommand.SessionID, Owner: authority.Owner,
		SupervisorStartedFactDigest: authority.SupervisorStartedFactDigest, TerminalizationID: authority.TerminalizationID, CleanupBindingDigest: authority.CleanupBindingDigest,
		ProcessTerminalFactDigest: authority.ProcessTerminalFactDigest, AllocationTerminatedFactDigest: authority.AllocationTerminatedFactDigest,
		CloseIntentDigest: outcome.RequestDigest, CloseReceiptDigest: outcome.ReceiptDigest, CloseObservationDigest: outcome.ObservationDigest, FinalCommandHead: outcome.CommandHead,
		SupervisorAbsenceObservationDigest: absenceDigest, SupervisorProcess: recovery.Absence.Expected, ObserverIdentity: recovery.Absence.Observer.Binary.SelfProfile, ObservedAt: recovery.Absence.ObservedAt,
		AuthenticatedSupervisorAbsence: recovery.Absence,
	}
	if err := closed.Validate(); err != nil {
		return ProcessSupervisorClosed{}, err
	}
	return closed, nil
}

func validateSupervisorTransitionAgainstProjection(in *Ingress, prior AttemptAuthorityState, exists bool, transition AttemptTransition, historicalReplay bool) error {
	if in == nil {
		return ErrAttemptAuthorityConflict
	}
	currentOwner := func(binding CurrentOwnerBinding) (ControlOwnerState, error) {
		key, err := binding.Scope.key()
		if err != nil {
			return ControlOwnerState{}, err
		}
		state, ok := in.controlOwners[key]
		if !ok || !currentOwnerMatches(state, binding) {
			return ControlOwnerState{}, ErrControlOwnerNotCurrent
		}
		return state, nil
	}
	switch transition.Kind {
	case AttemptTransitionControlOwnerBound:
		if !exists || transition.Identity.AuthorityNamespaceID != transition.Owner.Scope.AuthorityNamespaceID {
			return ErrControlOwnerConflict
		}
		_, err := currentOwner(transition.Owner)
		return err
	case AttemptTransitionProcessSupervisorStarted:
		started := transition.SupervisorStarted
		owner, err := currentOwner(started.Owner)
		if err != nil {
			return err
		}
		if started.V2 != (SupervisorStartedV2{}) {
			return validateStartedV2AgainstProjection(in, prior, exists, transition, owner)
		}
		if prior.SupervisorBootstrap.Request.Generation != (processsupervisor.ProtocolGenerationContract{}) {
			return ErrAttemptAuthorityConflict
		}
		expectedHandshakeHead := prior.HeadDigest
		typedBootstrap := prior.SupervisorBootstrap.Request != (SupervisorBootstrapRequestProjection{})
		if prior.SupervisorBootstrapDigest != "" && typedBootstrap {
			expectedHandshakeHead = prior.SupervisorBootstrap.Request.CurrentAuthorityHead
		}
		if !exists || prior.Owner != started.Owner || prior.ControlOwnerBindingDigest == "" || transition.Identity.AuthorityNamespaceID != started.Owner.Scope.AuthorityNamespaceID || started.Handshake.CurrentAuthorityHead != expectedHandshakeHead || started.Handshake.CommandSequence != 0 || started.Handshake.CommandHead != processsupervisor.CommandGenesisDigest || started.Handshake.JournalSequence != 1 || owner.Acquisition.OwnerBinary != started.Handshake.SupervisorBinary || owner.Acquisition.OwnerUID != started.ControlDirectory.UID || owner.Acquisition.OwnerGID != started.ControlDirectory.GID || sameSupervisorProcess(owner.Acquisition.OwnerProcess, started.Handshake.SupervisorProcess) {
			return ErrAttemptAuthorityConflict
		}
		if prior.SupervisorBootstrapDigest != "" && (started.BootstrapPreparedFactDigest != prior.SupervisorBootstrapDigest || started.Owner != prior.SupervisorBootstrap.Owner || started.LaunchAuthorizedFactDigest != prior.SupervisorBootstrap.LaunchAuthorizedFactDigest || started.Handshake.SessionID != prior.SupervisorBootstrap.SessionID || started.Handshake.SessionNonceDigest != prior.SupervisorBootstrap.SessionNonceDigest || !sameStableControlDirectoryIdentity(started.ControlDirectory, prior.SupervisorBootstrap.ControlDirectory) || started.Handshake.SupervisorBinary != prior.SupervisorBootstrap.SupervisorBinary || typedBootstrap && started.Handshake.OwnerEpoch != prior.SupervisorBootstrap.Request.OwnerEpoch) {
			return ErrAttemptAuthorityConflict
		}
		attemptKey, err := transition.Identity.Key()
		if err != nil {
			return err
		}
		for key, state := range in.attempts {
			if key == attemptKey || state.SupervisorStartedDigest == "" {
				continue
			}
			other := state.SupervisorStarted
			// v1 intentionally keeps closed Attempt identities in the append-only
			// authority projection. Device/inode reuse is therefore fail-closed
			// across history until a future ADR defines bounded authority-aware GC.
			if supervisorStartedObjectsConflict(other, started) {
				return ErrAttemptAuthorityConflict
			}
		}
		return nil
	case AttemptTransitionSupervisorBootstrap:
		prepared := transition.SupervisorBootstrap
		owner, err := currentOwner(prepared.Owner)
		if err != nil {
			return err
		}
		request := prepared.Request
		requestOmitted := request == (SupervisorBootstrapRequestProjection{})
		if requestOmitted && !historicalReplay {
			return ErrAttemptAuthorityConflict
		}
		if !exists || prior.Owner != prepared.Owner || prior.ControlOwnerBindingDigest == "" || prepared.LaunchAuthorizedFactDigest != prior.LaunchAuthorizedDigest || transition.Identity.AuthorityNamespaceID != prepared.Owner.Scope.AuthorityNamespaceID {
			return ErrAttemptAuthorityConflict
		}
		if !requestOmitted && (request.Authority != supervisorAuthorityTuple(transition.Identity) || request.CurrentAuthorityHead != prior.HeadDigest || request.Core.Process != owner.Acquisition.OwnerProcess || request.Core.Binary != owner.Acquisition.OwnerBinary || request.Core.UID != owner.Acquisition.OwnerUID || request.Core.GID != owner.Acquisition.OwnerGID) {
			return ErrAttemptAuthorityConflict
		}
		attemptKey, err := transition.Identity.Key()
		if err != nil {
			return err
		}
		for key, state := range in.attempts {
			if key == attemptKey || state.SupervisorBootstrapDigest == "" {
				continue
			}
			other := state.SupervisorBootstrap
			if other.SessionID == prepared.SessionID || sameControlObject(other.ControlDirectory.Device, other.ControlDirectory.Inode, prepared.ControlDirectory.Device, prepared.ControlDirectory.Inode) {
				return ErrAttemptAuthorityConflict
			}
		}
		return nil
	case AttemptTransitionProcessStarted:
		if !exists || prior.SupervisorStartedDigest == "" && (!historicalReplay || prior.ControlOwnerBindingDigest != "") {
			return ErrAttemptAuthorityOrder
		}
		if prior.SupervisorStartedDigest != "" {
			if _, err := currentOwner(prior.Owner); err != nil {
				return err
			}
			handshakeAt, handshakeErr := time.Parse(time.RFC3339Nano, prior.SupervisorStarted.Handshake.ObservedAt)
			processAt, processErr := time.Parse(time.RFC3339Nano, transition.ObservedAt)
			if handshakeErr != nil || processErr != nil || processAt.Before(handshakeAt) {
				return ErrAttemptAuthorityOrder
			}
		}
		return nil
	case AttemptTransitionProcessSupervisorClosed:
		closed := transition.SupervisorClosed
		if _, err := currentOwner(closed.Owner); err != nil {
			return err
		}
		if !exists || prior.Owner != closed.Owner || prior.SupervisorStartedDigest == "" || closed.SessionID != prior.SupervisorStarted.Handshake.SessionID || closed.SupervisorProcess != prior.SupervisorStarted.Handshake.SupervisorProcess || closed.FinalCommandHead == prior.SupervisorStarted.Handshake.CommandHead {
			return ErrAttemptAuthorityConflict
		}
		if prior.SupervisorBootstrapDigest != "" && transition.SupervisorOutcomeFactDigest != "" && !zeroSupervisorCommandEvidence(closed.Mechanics) {
			return ErrAttemptAuthorityConflict
		}
		if prior.SupervisorBootstrapDigest != "" && transition.SupervisorOutcomeFactDigest == "" && (zeroSupervisorCommandEvidence(closed.Mechanics) || closed.Mechanics.CurrentAuthorityHead != prior.HeadDigest || closed.Mechanics.Outcome.Process != prior.ProcessStartedEvidence.Outcome.Process) {
			return ErrAttemptAuthorityConflict
		}
		startedAt, startedErr := time.Parse(time.RFC3339Nano, prior.SupervisorStarted.Handshake.ObservedAt)
		closedAt, closedErr := time.Parse(time.RFC3339Nano, closed.ObservedAt)
		if startedErr != nil || closedErr != nil || closedAt.Before(startedAt) {
			return ErrAttemptAuthorityConflict
		}
		return nil
	case AttemptTransitionCleanupCompleted:
		if !exists || prior.SupervisorClosedDigest == "" && (!historicalReplay || prior.ControlOwnerBindingDigest != "") || prior.SupervisorClosedDigest != "" && transition.SupervisorClosedFactDigest != prior.SupervisorClosedDigest {
			return ErrAttemptAuthorityOrder
		}
		return nil
	case AttemptTransitionSupervisorIntervention:
		if _, err := currentOwner(transition.SupervisorIntervention.Owner); err != nil {
			return err
		}
		if !exists || prior.SupervisorBootstrapDigest == "" || transition.SupervisorIntervention.SessionID != prior.SupervisorBootstrap.SessionID || transition.SupervisorIntervention.Owner != prior.Owner {
			return ErrAttemptAuthorityConflict
		}
		return nil
	default:
		return nil
	}
}

func sameSupervisorProcess(left, right processsupervisor.ProcessIdentity) bool {
	return left.PID == right.PID && left.BirthSeconds == right.BirthSeconds && left.BirthMicroseconds == right.BirthMicroseconds
}

func sameControlObject(leftDevice, leftInode, rightDevice, rightInode uint64) bool {
	return leftDevice == rightDevice && leftInode == rightInode
}

// BindOwnerToAttempt advances one exact Attempt head to the current global
// acquisition. A crash after AcquireOwner but before this append leaves the
// Attempt unbound; supervisor reconnect/spawn remains forbidden until the same
// binding is replayed. Multiple Attempts may bind the same scope epoch.
func (s *ingressDurableStore) BindOwnerToAttempt(ctx context.Context, ownerVerifier CurrentOwnerLockVerifier, runVerifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request AttemptAuthorizationRequest, owner CurrentOwnerBinding) (AttemptAppendResult, error) {
	if request.Identity.AuthorityNamespaceID != owner.Scope.AuthorityNamespaceID || request.CurrentRunAuthority != runAuthorityBindingFor(request.Identity) {
		return AttemptAppendResult{}, ErrControlOwnerNotCurrent
	}
	transition := AttemptTransition{Kind: AttemptTransitionControlOwnerBound, Identity: request.Identity, Owner: owner}
	if err := validateTransitionShape(transition); err != nil {
		return AttemptAppendResult{}, err
	}
	var result AttemptAppendResult
	err := s.WithCurrentOwner(ctx, ownerVerifier, owner, func(ControlOwnerState) error {
		return withCurrentRunAuthority(ctx, runVerifier, request.CurrentRunAuthority, func() error {
			var appendErr error
			result, appendErr = s.compareAndAppend(expectedRevision, expectedHead, transition, false)
			return appendErr
		})
	})
	return result, err
}

// AppendSupervisorBootstrap records the recovery anchor before the fixed
// supervisor executable is started. It stores only digests and identities;
// the raw session nonce and bootstrap payload are never persisted.
func (s *ingressDurableStore) AppendSupervisorBootstrap(ctx context.Context, ownerVerifier CurrentOwnerLockVerifier, runVerifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request AttemptAuthorizationRequest, prepared SupervisorBootstrapPrepared) (AttemptAppendResult, error) {
	if request.Identity.AuthorityNamespaceID != prepared.Owner.Scope.AuthorityNamespaceID || request.CurrentRunAuthority != runAuthorityBindingFor(request.Identity) {
		return AttemptAppendResult{}, ErrControlOwnerNotCurrent
	}
	transition := AttemptTransition{Kind: AttemptTransitionSupervisorBootstrap, Identity: request.Identity, SupervisorBootstrap: prepared}
	if err := validateTransitionShape(transition); err != nil {
		return AttemptAppendResult{}, err
	}
	var result AttemptAppendResult
	err := s.WithCurrentOwner(ctx, ownerVerifier, prepared.Owner, func(ControlOwnerState) error {
		return withCurrentRunAuthority(ctx, runVerifier, request.CurrentRunAuthority, func() error {
			var appendErr error
			result, appendErr = s.compareAndAppend(expectedRevision, expectedHead, transition, false)
			return appendErr
		})
	})
	return result, err
}

// AppendSupervisorStarted serializes the passive supervisor handshake into
// the exact Attempt chain under both current owner and current Run authority.
func (s *ingressDurableStore) AppendSupervisorStarted(ctx context.Context, ownerVerifier CurrentOwnerLockVerifier, runVerifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request AttemptAuthorizationRequest, started ProcessSupervisorStarted) (AttemptAppendResult, error) {
	if request.Identity.AuthorityNamespaceID != started.Owner.Scope.AuthorityNamespaceID || request.CurrentRunAuthority != runAuthorityBindingFor(request.Identity) {
		return AttemptAppendResult{}, ErrControlOwnerNotCurrent
	}
	transition := AttemptTransition{Kind: AttemptTransitionProcessSupervisorStarted, Identity: request.Identity, SupervisorStarted: started}
	if err := validateTransitionShape(transition); err != nil {
		return AttemptAppendResult{}, err
	}
	var result AttemptAppendResult
	err := s.WithCurrentOwner(ctx, ownerVerifier, started.Owner, func(ControlOwnerState) error {
		return withCurrentRunAuthority(ctx, runVerifier, request.CurrentRunAuthority, func() error {
			var appendErr error
			result, appendErr = s.compareAndAppend(expectedRevision, expectedHead, transition, false)
			return appendErr
		})
	})
	return result, err
}

// AppendSupervisorReconnect persists the authenticated Previous→Current
// mechanics reanchor before any caller may use a new owner epoch / Attempt
// head. It shares the command recovery chain but never advances Attempt
// revision/head itself.
func (s *ingressDurableStore) AppendSupervisorReconnect(ctx context.Context, ownerVerifier CurrentOwnerLockVerifier, runVerifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request AttemptAuthorizationRequest, owner CurrentOwnerBinding, recovery processsupervisor.SessionRecoveryEvidence) (AttemptAppendResult, error) {
	reconnect, err := newSupervisorReconnectEvidence(recovery)
	if request.Identity.AuthorityNamespaceID != owner.Scope.AuthorityNamespaceID || request.CurrentRunAuthority != runAuthorityBindingFor(request.Identity) || err != nil {
		return AttemptAppendResult{}, ErrControlOwnerNotCurrent
	}
	var result AttemptAppendResult
	err = s.WithCurrentOwner(ctx, ownerVerifier, owner, func(ControlOwnerState) error {
		return withCurrentRunAuthority(ctx, runVerifier, request.CurrentRunAuthority, func() error {
			projection := newAuthorityProjection()
			return s.transact(projection, func() error {
				key, keyErr := request.Identity.Key()
				if keyErr != nil {
					return keyErr
				}
				state, found := projection.attempts[key]
				if !found || state.Identity != request.Identity || state.Owner != owner || state.Revision != expectedRevision || state.HeadDigest != expectedHead || state.SupervisorStartedDigest == "" || state.SupervisorInterventionDigest != "" {
					return ErrAttemptAuthorityConflict
				}
				if state.SupervisorReconnect == reconnect && state.SupervisorReconnectFactDigest != "" && state.SupervisorCommandRecoveryHead == state.SupervisorReconnectFactDigest {
					result = AttemptAppendResult{State: state, TransitionDigest: state.SupervisorReconnectFactDigest}
					return nil
				}
				if validateSupervisorReconnectAgainstState(state, owner, reconnect) != nil {
					return ErrAttemptAuthorityOrder
				}
				fact := &supervisorCommandFact{ProtocolRevision: supervisorCommandProtocolRevision, FactType: supervisorReconnectFactType, Sequence: s.nextSequence, AttemptKey: key, AttemptRevision: state.Revision, AttemptAuthorityHead: state.HeadDigest, PreviousRecoveryFactDigest: state.SupervisorCommandRecoveryHead, Reconnect: reconnect}
				if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
					return err
				}
				s.nextSequence++
				if err := applySupervisorCommandFactValue(*fact, projection); err != nil {
					return fmt.Errorf("resultingress: appended supervisor reconnect failed projection: %w", err)
				}
				result = AttemptAppendResult{State: projection.attempts[key], Appended: true, TransitionDigest: fact.Digest}
				return nil
			})
		})
	})
	return result, err
}

// AppendSupervisorCommandIntent persists one creation-once, secret-free
// command request projection before Client.Do. The held owner and Run
// authority span the complete RB1 CAS; no side effect is executed here.
func (s *ingressDurableStore) AppendSupervisorCommandIntent(ctx context.Context, ownerVerifier CurrentOwnerLockVerifier, runVerifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request AttemptAuthorizationRequest, owner CurrentOwnerBinding, intent SupervisorCommandIntent) (AttemptAppendResult, error) {
	if request.Identity.AuthorityNamespaceID != owner.Scope.AuthorityNamespaceID || request.CurrentRunAuthority != runAuthorityBindingFor(request.Identity) {
		return AttemptAppendResult{}, ErrControlOwnerNotCurrent
	}
	if err := intent.Validate(); err != nil {
		return AttemptAppendResult{}, err
	}
	var result AttemptAppendResult
	err := s.WithCurrentOwner(ctx, ownerVerifier, owner, func(ControlOwnerState) error {
		return withCurrentRunAuthority(ctx, runVerifier, request.CurrentRunAuthority, func() error {
			projection := newAuthorityProjection()
			return s.transact(projection, func() error {
				key, keyErr := request.Identity.Key()
				if keyErr != nil {
					return keyErr
				}
				state, found := projection.attempts[key]
				if !found || state.Identity != request.Identity || state.Owner != owner || state.Revision != expectedRevision || state.HeadDigest != expectedHead || state.SupervisorInterventionDigest != "" {
					return ErrAttemptAuthorityConflict
				}
				if state.SupervisorPendingIntentDigest != "" {
					if state.SupervisorPendingIntent == intent {
						result = AttemptAppendResult{State: state, TransitionDigest: state.SupervisorPendingIntentDigest}
						return nil
					}
					return ErrAttemptAuthorityOrder
				}
				if validateSupervisorCommandIntentAgainstState(state, intent) != nil {
					return ErrAttemptAuthorityOrder
				}
				fact := &supervisorCommandFact{ProtocolRevision: supervisorIntentRecoveryRevision(intent), FactType: supervisorCommandIntentFactType, Sequence: s.nextSequence, AttemptKey: key, AttemptRevision: state.Revision, AttemptAuthorityHead: state.HeadDigest, PreviousRecoveryFactDigest: state.SupervisorCommandRecoveryHead, Intent: intent}
				if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
					return err
				}
				s.nextSequence++
				if err := applySupervisorCommandFactValue(*fact, projection); err != nil {
					return fmt.Errorf("resultingress: appended supervisor intent failed projection: %w", err)
				}
				result = AttemptAppendResult{State: projection.attempts[key], Appended: true, TransitionDigest: fact.Digest}
				return nil
			})
		})
	})
	return result, err
}

// AppendSupervisorCommandOutcome closes exactly the currently pending intent
// with one already authenticated Client outcome. Rejected mechanics receipts
// are checkpoints too: they advance sequence/head and make a later retry
// continuous instead of leaving a hidden command gap.
func (s *ingressDurableStore) AppendSupervisorCommandOutcome(ctx context.Context, ownerVerifier CurrentOwnerLockVerifier, runVerifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request AttemptAuthorizationRequest, owner CurrentOwnerBinding, outcome SupervisorCommandEvidence) (AttemptAppendResult, error) {
	if request.Identity.AuthorityNamespaceID != owner.Scope.AuthorityNamespaceID || request.CurrentRunAuthority != runAuthorityBindingFor(request.Identity) {
		return AttemptAppendResult{}, ErrControlOwnerNotCurrent
	}
	if err := outcome.Validate(); err != nil {
		return AttemptAppendResult{}, err
	}
	var result AttemptAppendResult
	err := s.WithCurrentOwner(ctx, ownerVerifier, owner, func(ControlOwnerState) error {
		return withCurrentRunAuthority(ctx, runVerifier, request.CurrentRunAuthority, func() error {
			projection := newAuthorityProjection()
			return s.transact(projection, func() error {
				key, keyErr := request.Identity.Key()
				if keyErr != nil {
					return keyErr
				}
				state, found := projection.attempts[key]
				if !found || state.Identity != request.Identity || state.Owner != owner || state.Revision != expectedRevision || state.HeadDigest != expectedHead || state.SupervisorInterventionDigest != "" {
					return ErrAttemptAuthorityConflict
				}
				if state.SupervisorPendingIntentDigest == "" {
					if len(state.SupervisorCommandCheckpoints) != 0 && state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1].Evidence == outcome {
						checkpoint := state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1]
						result = AttemptAppendResult{State: state, TransitionDigest: checkpoint.FactDigest}
						return nil
					}
					return ErrAttemptAuthorityOrder
				}
				if validateSupervisorCommandOutcomeAgainstIntent(state, outcome) != nil {
					return ErrAttemptAuthorityOrder
				}
				fact := &supervisorCommandFact{ProtocolRevision: supervisorOutcomeRecoveryRevision(outcome), FactType: supervisorCommandOutcomeFactType, Sequence: s.nextSequence, AttemptKey: key, AttemptRevision: state.Revision, AttemptAuthorityHead: state.HeadDigest, PreviousRecoveryFactDigest: state.SupervisorCommandRecoveryHead, Outcome: outcome}
				if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
					return err
				}
				s.nextSequence++
				if err := applySupervisorCommandFactValue(*fact, projection); err != nil {
					return fmt.Errorf("resultingress: appended supervisor outcome failed projection: %w", err)
				}
				result = AttemptAppendResult{State: projection.attempts[key], Appended: true, TransitionDigest: fact.Digest}
				return nil
			})
		})
	})
	return result, err
}

func applySupervisorCommandLine(line []byte, in *Ingress, wantSequence int64) error {
	canonicalLine, err := canonical.JSON(line)
	if err != nil || !bytes.Equal(canonicalLine, line) {
		return fmt.Errorf("%w: supervisor command line is not canonical", ErrAttemptAuthorityConflict)
	}
	var fact supervisorCommandFact
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		return fmt.Errorf("%w: %v", ErrAttemptAuthorityConflict, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing supervisor command value", ErrAttemptAuthorityConflict)
	}
	if !validSupervisorRecoveryFactGeneration(fact) || fact.Sequence != wantSequence || fact.FactType != supervisorCommandIntentFactType && fact.FactType != supervisorCommandOutcomeFactType && fact.FactType != supervisorReconnectFactType {
		return ErrAttemptAuthorityConflict
	}
	stored := fact.Digest
	fact.Digest = ""
	digest, err := canonicalDigest(fact)
	if err != nil || stored == "" || digest != stored {
		return ErrAttemptAuthorityConflict
	}
	fact.Digest = stored
	return applySupervisorCommandFactValue(fact, in)
}

func applySupervisorCommandFactValue(fact supervisorCommandFact, in *Ingress) error {
	if !validSupervisorRecoveryFactGeneration(fact) {
		return ErrAttemptAuthorityConflict
	}
	state, found := in.attempts[fact.AttemptKey]
	if !found || state.Revision != fact.AttemptRevision || state.HeadDigest != fact.AttemptAuthorityHead || state.SupervisorStartedDigest == "" || state.SupervisorInterventionDigest != "" {
		return ErrAttemptAuthorityOrder
	}
	switch fact.FactType {
	case supervisorCommandIntentFactType:
		if fact.Outcome != (SupervisorCommandEvidence{}) || fact.Reconnect != (SupervisorReconnectEvidence{}) || state.SupervisorPendingIntentDigest != "" || fact.PreviousRecoveryFactDigest != state.SupervisorCommandRecoveryHead {
			return ErrAttemptAuthorityConflict
		}
		if fact.Intent.Command == processsupervisor.CommandBindAuthority && state.SupervisorBoundAuthorityHead != "" {
			if validateRebindSupervisorIntentAgainstState(state, fact.Intent) != nil {
				return ErrAttemptAuthorityConflict
			}
		} else if validateSupervisorCommandIntentAgainstState(state, fact.Intent) != nil {
			return ErrAttemptAuthorityConflict
		}
		state.SupervisorPendingIntent = fact.Intent
		state.SupervisorPendingIntentDigest = fact.Digest
		state.SupervisorCommandRecoveryHead = fact.Digest
	case supervisorCommandOutcomeFactType:
		if fact.Intent != (SupervisorCommandIntent{}) || fact.Reconnect != (SupervisorReconnectEvidence{}) || state.SupervisorPendingIntentDigest == "" || fact.PreviousRecoveryFactDigest != state.SupervisorCommandRecoveryHead || validateSupervisorCommandOutcomeAgainstIntent(state, fact.Outcome) != nil {
			return ErrAttemptAuthorityConflict
		}
		state.SupervisorCommandCheckpoints = append(state.SupervisorCommandCheckpoints, SupervisorCommandCheckpoint{FactDigest: fact.Digest, Intent: state.SupervisorPendingIntent, Evidence: fact.Outcome})
		advanceSupervisorCommandState(&state, fact.Outcome)
		state.SupervisorMechanicsAnchor = fact.Outcome.PostCommand
		if fact.Outcome.Command == processsupervisor.CommandBindAuthority && fact.Outcome.Disposition == "ok" {
			state.SupervisorBoundAuthorityHead = fact.Outcome.BoundAuthorityHead
			state.SupervisorMechanicsAuthorityHead = fact.Outcome.BoundAuthorityHead
		} else if fact.Outcome.Disposition == "ok" {
			state.SupervisorMechanicsAuthorityHead = fact.Outcome.PostCommand.CurrentAuthorityHead
		}
		state.SupervisorPendingIntent = SupervisorCommandIntent{}
		state.SupervisorPendingIntentDigest = ""
		state.SupervisorCommandRecoveryHead = fact.Digest
	case supervisorReconnectFactType:
		if fact.Intent != (SupervisorCommandIntent{}) || fact.Outcome != (SupervisorCommandEvidence{}) || fact.PreviousRecoveryFactDigest != state.SupervisorCommandRecoveryHead || validateSupervisorReconnectAgainstState(state, state.Owner, fact.Reconnect) != nil {
			return ErrAttemptAuthorityConflict
		}
		state.SupervisorReconnect = fact.Reconnect
		state.SupervisorReconnectFactDigest = fact.Digest
		state.SupervisorMechanicsAnchor = fact.Reconnect.Current
		state.SupervisorMechanicsAuthorityHead = fact.Reconnect.Current.CurrentAuthorityHead
		state.SupervisorCommandRecoveryHead = fact.Digest
	default:
		return ErrAttemptAuthorityConflict
	}
	in.attempts[fact.AttemptKey] = state
	return nil
}

// AppendProcessStarted closes the stale-owner gap between a durable
// supervisor-started fact and the existing Run-authorized process fact. Once
// an Attempt is owner-governed, the legacy Run-only entry point rejects both
// fresh appends and exact replays.
func (s *ingressDurableStore) AppendProcessStarted(ctx context.Context, ownerVerifier CurrentOwnerLockVerifier, runVerifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request AttemptAuthorizationRequest, owner CurrentOwnerBinding, transition AttemptTransition) (AttemptAppendResult, error) {
	if transition.Kind != AttemptTransitionProcessStarted || transition.Identity != request.Identity || request.Identity.AuthorityNamespaceID != owner.Scope.AuthorityNamespaceID || request.CurrentRunAuthority != runAuthorityBindingFor(request.Identity) {
		return AttemptAppendResult{}, ErrControlOwnerNotCurrent
	}
	if err := validateTransitionShape(transition); err != nil {
		return AttemptAppendResult{}, err
	}
	var result AttemptAppendResult
	err := s.WithCurrentOwner(ctx, ownerVerifier, owner, func(ControlOwnerState) error {
		state, found, stateErr := s.AttemptState(request.Identity)
		if stateErr != nil {
			return stateErr
		}
		if !found || state.Owner != owner || state.ControlOwnerBindingDigest == "" || state.SupervisorStartedDigest == "" {
			return ErrControlOwnerNotCurrent
		}
		return withCurrentRunAuthority(ctx, runVerifier, request.CurrentRunAuthority, func() error {
			var appendErr error
			result, appendErr = s.compareAndAppendWithOwner(expectedRevision, expectedHead, transition, false, true)
			return appendErr
		})
	})
	return result, err
}

// AppendSupervisorClosed is legal only after exact process-terminal and
// allocation-terminated facts under the still-current cleanup binding. It does
// not execute close mechanics; it records the already verified receipt and
// absence observation.
func (s *ingressDurableStore) AppendSupervisorClosed(ctx context.Context, ownerVerifier CurrentOwnerLockVerifier, runVerifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request CleanupAuthorizationRequest, closed ProcessSupervisorClosed, outcomeFactDigest string) (AttemptAppendResult, error) {
	if request.Identity.AuthorityNamespaceID != closed.Owner.Scope.AuthorityNamespaceID || request.CurrentRunAuthority != runAuthorityBindingFor(request.Identity) || request.TerminalizationID != closed.TerminalizationID || request.CleanupBindingDigest != closed.CleanupBindingDigest || request.Operation != CleanupReconcile {
		return AttemptAppendResult{}, ErrCleanupUnauthorized
	}
	transition := AttemptTransition{Kind: AttemptTransitionProcessSupervisorClosed, Identity: request.Identity, TerminalizationID: request.TerminalizationID, SupervisorClosed: closed, SupervisorOutcomeFactDigest: outcomeFactDigest}
	if err := validateTransitionShape(transition); err != nil {
		return AttemptAppendResult{}, err
	}
	var result AttemptAppendResult
	err := s.WithCurrentOwner(ctx, ownerVerifier, closed.Owner, func(ControlOwnerState) error {
		var appendErr error
		result, appendErr = s.compareAndAppendCleanup(ctx, runVerifier, expectedRevision, expectedHead, request, transition, true)
		return appendErr
	})
	return result, err
}

// AppendSupervisorIntervention permanently fences a prepared supervisor
// session whose bootstrap or command intent cannot be resolved. Exact replay
// remains read-only; every later lifecycle mutation is rejected.
func (s *ingressDurableStore) AppendSupervisorIntervention(ctx context.Context, ownerVerifier CurrentOwnerLockVerifier, runVerifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request AttemptAuthorizationRequest, intervention SupervisorIntervention) (AttemptAppendResult, error) {
	if request.Identity.AuthorityNamespaceID != intervention.Owner.Scope.AuthorityNamespaceID || request.CurrentRunAuthority != runAuthorityBindingFor(request.Identity) {
		return AttemptAppendResult{}, ErrControlOwnerNotCurrent
	}
	transition := AttemptTransition{Kind: AttemptTransitionSupervisorIntervention, Identity: request.Identity, SupervisorIntervention: intervention}
	if err := validateTransitionShape(transition); err != nil {
		return AttemptAppendResult{}, err
	}
	var result AttemptAppendResult
	err := s.WithCurrentOwner(ctx, ownerVerifier, intervention.Owner, func(ControlOwnerState) error {
		return withCurrentRunAuthority(ctx, runVerifier, request.CurrentRunAuthority, func() error {
			var appendErr error
			result, appendErr = s.compareAndAppend(expectedRevision, expectedHead, transition, false)
			return appendErr
		})
	})
	return result, err
}
