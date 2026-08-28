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

type ControlOwnerState struct {
	Acquisition        ControlOwnerAcquisition `json:"acquisition"`
	PreviousFactDigest string                  `json:"previousFactDigest,omitempty"`
	FactDigest         string                  `json:"factDigest"`
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

type CurrentOwnerBinding struct {
	Scope                          ControlOwnerScope `json:"scope"`
	OwnerEpoch                     uint64            `json:"ownerEpoch"`
	ControlOwnerAcquiredFactDigest string            `json:"controlOwnerAcquiredFactDigest"`
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
	in.controlOwners[key] = ControlOwnerState{Acquisition: fact.Acquisition, PreviousFactDigest: fact.PreviousOwnerDigest, FactDigest: fact.Digest}
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
	Owner                      CurrentOwnerBinding                        `json:"owner"`
	LaunchAuthorizedFactDigest string                                     `json:"launchAuthorizedFactDigest"`
	ControlDirectory           processsupervisor.ControlDirectoryIdentity `json:"controlDirectory"`
	Handshake                  processsupervisor.HandshakeResponse        `json:"handshake"`
}

func (started ProcessSupervisorStarted) Validate() error {
	if started.Owner.Validate() != nil || requireDigest("launchAuthorizedFactDigest", started.LaunchAuthorizedFactDigest) != nil || validateControlDirectoryIdentity(started.ControlDirectory) != nil || processsupervisor.ValidateHandshakeResponse(started.Handshake) != nil || started.Handshake.OwnerEpoch != started.Owner.OwnerEpoch {
		return fmt.Errorf("%w: invalid process-supervisor-started payload", ErrAttemptAuthorityConflict)
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

// ProcessSupervisorClosed binds the exact close intent/receipt and final
// command head to an absence observation for the started supervisor birth.
// cleanup-completed must cite the resulting Attempt fact digest.
type ProcessSupervisorClosed struct {
	ProtocolRevision                   string                            `json:"protocolRevision"`
	SessionID                          string                            `json:"sessionId"`
	Owner                              CurrentOwnerBinding               `json:"owner"`
	SupervisorStartedFactDigest        string                            `json:"supervisorStartedFactDigest"`
	TerminalizationID                  string                            `json:"terminalizationId"`
	CleanupBindingDigest               string                            `json:"cleanupBindingDigest"`
	ProcessTerminalFactDigest          string                            `json:"processTerminalFactDigest"`
	AllocationTerminatedFactDigest     string                            `json:"allocationTerminatedFactDigest"`
	CloseIntentDigest                  string                            `json:"closeIntentDigest"`
	CloseReceiptDigest                 string                            `json:"closeReceiptDigest"`
	FinalCommandHead                   string                            `json:"finalCommandHead"`
	SupervisorAbsenceObservationDigest string                            `json:"supervisorAbsenceObservationDigest"`
	SupervisorProcess                  processsupervisor.ProcessIdentity `json:"supervisorProcess"`
	ObserverIdentity                   string                            `json:"observerIdentity"`
	ObservedAt                         string                            `json:"observedAt"`
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
	return nil
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
		if !exists || prior.Owner != started.Owner || prior.ControlOwnerBindingDigest == "" || transition.Identity.AuthorityNamespaceID != started.Owner.Scope.AuthorityNamespaceID || started.Handshake.CurrentAuthorityHead != prior.HeadDigest || started.Handshake.CommandSequence != 0 || started.Handshake.CommandHead != processsupervisor.CommandGenesisDigest || started.Handshake.JournalSequence != 1 || owner.Acquisition.OwnerBinary != started.Handshake.SupervisorBinary || owner.Acquisition.OwnerUID != started.ControlDirectory.UID || owner.Acquisition.OwnerGID != started.ControlDirectory.GID || sameSupervisorProcess(owner.Acquisition.OwnerProcess, started.Handshake.SupervisorProcess) {
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
			if other.Handshake.SessionID == started.Handshake.SessionID || sameSupervisorProcess(other.Handshake.SupervisorProcess, started.Handshake.SupervisorProcess) || sameControlObject(other.ControlDirectory.Device, other.ControlDirectory.Inode, started.ControlDirectory.Device, started.ControlDirectory.Inode) || sameControlObject(other.Handshake.ControlSocket.Device, other.Handshake.ControlSocket.Inode, started.Handshake.ControlSocket.Device, started.Handshake.ControlSocket.Inode) {
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
func (s *ingressDurableStore) AppendSupervisorClosed(ctx context.Context, ownerVerifier CurrentOwnerLockVerifier, runVerifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request CleanupAuthorizationRequest, closed ProcessSupervisorClosed) (AttemptAppendResult, error) {
	if request.Identity.AuthorityNamespaceID != closed.Owner.Scope.AuthorityNamespaceID || request.CurrentRunAuthority != runAuthorityBindingFor(request.Identity) || request.TerminalizationID != closed.TerminalizationID || request.CleanupBindingDigest != closed.CleanupBindingDigest || request.Operation != CleanupReconcile {
		return AttemptAppendResult{}, ErrCleanupUnauthorized
	}
	transition := AttemptTransition{Kind: AttemptTransitionProcessSupervisorClosed, Identity: request.Identity, TerminalizationID: request.TerminalizationID, SupervisorClosed: closed}
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
