package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
)

// attemptAuthorityProtocolRevision is deliberately separate from the legacy
// ResultIngress revision. New attempts use one physical append-only log and a
// per-attempt revision/head chain; legacy result facts remain replay-only.
const attemptAuthorityProtocolRevision = "attempt-authority/v1"

const (
	AttemptTransitionOpened                 AttemptTransitionKind = "attempt-opened"
	AttemptTransitionLaunchAuthorized       AttemptTransitionKind = "launch-authorized"
	AttemptTransitionProcessStarted         AttemptTransitionKind = "process-started"
	attemptTransitionResultAdmitted         AttemptTransitionKind = "result-admitted"
	AttemptTransitionTerminalizationBarrier AttemptTransitionKind = "terminalization-barrier"
	AttemptTransitionProcessTerminal        AttemptTransitionKind = "process-terminal"
	AttemptTransitionAllocationTerminated   AttemptTransitionKind = "allocation-terminated"
	AttemptTransitionCleanupCompleted       AttemptTransitionKind = "cleanup-completed"
	AttemptTransitionCleanupReleased        AttemptTransitionKind = "cleanup-released"
)

var (
	ErrAttemptAuthorityConflict = errors.New("resultingress: attempt authority compare-and-append conflict")
	ErrAttemptAuthorityOrder    = errors.New("resultingress: attempt authority transition out of order")
	ErrAttemptAuthorityUnknown  = errors.New("resultingress: attempt authority unknown")
	ErrRunAuthorityUnauthorized = errors.New("resultingress: current Run authority unauthorized")
	ErrCleanupUnauthorized      = errors.New("resultingress: cleanup operation unauthorized")
)

// AttemptTransitionKind is the closed transition set of the single Attempt
// authority. result-admitted is intentionally private: it can only be emitted
// by Ingress after the DRC and current-ledger checks have passed.
type AttemptTransitionKind string

// AttemptIdentity freezes the complete dispatch, namespace and current Run
// authority before launch. FencingTokenDigest is a digest, never the bearer.
type AttemptIdentity struct {
	AuthorityNamespaceID authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	// AuthorityNamespaceRef is the exact legacy/string namespace carried by
	// DRCs. The typed namespace remains the owner identity; this ref only
	// prevents an old wire credential from being rebound during migration.
	AuthorityNamespaceRef string `json:"authorityNamespaceRef"`
	TaskID                string `json:"taskId"`
	RunID                 string `json:"runId"`
	AttemptID             string `json:"attemptId"`
	AllocationID          string `json:"allocationId"`
	LeaseID               string `json:"leaseId"`
	LeaseDigest           string `json:"leaseDigest"`
	DispatchGeneration    int64  `json:"dispatchGeneration"`
	FencingTokenDigest    string `json:"fencingTokenDigest"`
	OrchestratorID        string `json:"orchestratorId"`
	RunAuthorityDigest    string `json:"runAuthorityDigest"`
}

func (id AttemptIdentity) Validate() error {
	if err := id.AuthorityNamespaceID.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrAttemptAuthorityConflict, err)
	}
	for name, value := range map[string]string{
		"authorityNamespaceRef": id.AuthorityNamespaceRef,
		"taskId":                id.TaskID, "runId": id.RunID, "attemptId": id.AttemptID,
		"allocationId": id.AllocationID, "leaseId": id.LeaseID,
		"orchestratorId": id.OrchestratorID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s is empty", ErrAttemptAuthorityConflict, name)
		}
	}
	if id.DispatchGeneration < 1 {
		return fmt.Errorf("%w: dispatchGeneration must be positive", ErrAttemptAuthorityConflict)
	}
	for name, value := range map[string]string{
		"leaseDigest":        id.LeaseDigest,
		"fencingTokenDigest": id.FencingTokenDigest,
		"runAuthorityDigest": id.RunAuthorityDigest,
	} {
		if err := requireDigest(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrAttemptAuthorityConflict, err)
		}
	}
	return nil
}

func (id AttemptIdentity) Key() (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	// The index key is deliberately narrower than the immutable opened fact.
	// Authority/lease/allocation drift must collide with the existing logical
	// Attempt and fail closed, never create a sibling authority chain.
	logical := struct {
		AuthorityNamespaceID authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
		TaskID               string                         `json:"taskId"`
		RunID                string                         `json:"runId"`
		AttemptID            string                         `json:"attemptId"`
	}{id.AuthorityNamespaceID, id.TaskID, id.RunID, id.AttemptID}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", err
	}
	canonicalBytes, err := canonical.JSON(raw)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalBytes), nil
}

// ProcessObservation is a Darwin ordinary-user process-group observation,
// not a containment assertion. It is persisted on process-started so later
// process control can recheck the exact birth/path/object identity.
type ProcessObservation struct {
	PID                    int    `json:"pid"`
	PGID                   int    `json:"pgid"`
	BirthSeconds           int64  `json:"birthSeconds"`
	BirthMicroseconds      int64  `json:"birthMicroseconds"`
	WorkingDirectory       string `json:"workingDirectory"`
	WorkingDirectoryDevice uint64 `json:"workingDirectoryDevice"`
	WorkingDirectoryInode  uint64 `json:"workingDirectoryInode"`
	WorkingDirectoryType   uint32 `json:"workingDirectoryFileType"`
	WorkingDirectoryOwner  uint32 `json:"workingDirectoryOwner"`
	WorkingDirectoryMode   uint32 `json:"workingDirectoryMode"`
	ExecutablePath         string `json:"executablePath"`
	ExecutableDevice       uint64 `json:"executableDevice"`
	ExecutableInode        uint64 `json:"executableInode"`
	ExecutableSize         int64  `json:"executableSize"`
	ExecutableType         uint32 `json:"executableFileType"`
	ExecutableOwner        uint32 `json:"executableOwner"`
	ExecutableGroup        uint32 `json:"executableGroup"`
	ExecutableMode         uint32 `json:"executableMode"`
	ExecutableLinkCount    uint64 `json:"executableLinkCount"`
	ExecutableSHA256       string `json:"executableSha256"`
	ObserverIdentity       string `json:"observerIdentity"`
	ObservationDigest      string `json:"observationDigest"`
}

const (
	// POSIXFileTypeDirectory and POSIXFileTypeRegular are the raw st_mode
	// S_IFMT values RB2 must persist, not Go fs.FileMode type bits.
	POSIXFileTypeDirectory uint32 = 0o040000
	POSIXFileTypeRegular   uint32 = 0o100000
)

func (p ProcessObservation) Validate() error {
	if p.PID <= 0 || p.PGID != p.PID || p.BirthSeconds <= 0 || p.BirthMicroseconds < 0 || p.BirthMicroseconds >= 1_000_000 {
		return fmt.Errorf("%w: invalid pid/pgid/birth observation", ErrAttemptAuthorityConflict)
	}
	if strings.TrimSpace(p.WorkingDirectory) == "" || strings.TrimSpace(p.ExecutablePath) == "" || strings.TrimSpace(p.ObserverIdentity) == "" || p.WorkingDirectoryInode == 0 || p.WorkingDirectoryMode == 0 || p.ExecutableInode == 0 || p.ExecutableSize <= 0 || p.ExecutableMode == 0 || p.ExecutableLinkCount == 0 {
		return fmt.Errorf("%w: incomplete process/object observation", ErrAttemptAuthorityConflict)
	}
	if !filepath.IsAbs(p.WorkingDirectory) || filepath.Clean(p.WorkingDirectory) != p.WorkingDirectory || !filepath.IsAbs(p.ExecutablePath) || filepath.Clean(p.ExecutablePath) != p.ExecutablePath {
		return fmt.Errorf("%w: process paths must be absolute and lexically canonical", ErrAttemptAuthorityConflict)
	}
	if p.WorkingDirectoryType != POSIXFileTypeDirectory || p.ExecutableType != POSIXFileTypeRegular {
		return fmt.Errorf("%w: cwd must be a directory and executable must be a regular file", ErrAttemptAuthorityConflict)
	}
	if err := requireDigest("executableSha256", p.ExecutableSHA256); err != nil {
		return fmt.Errorf("%w: %v", ErrAttemptAuthorityConflict, err)
	}
	stored := p.ObservationDigest
	p.ObservationDigest = ""
	digest, err := canonicalDigest(p)
	if err != nil || stored == "" || digest != stored {
		return fmt.Errorf("%w: observationDigest mismatch", ErrAttemptAuthorityConflict)
	}
	return nil
}

func validateObservedAt(value string, process ProcessObservation) error {
	observed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || observed.UTC().Format(time.RFC3339Nano) != value {
		return fmt.Errorf("%w: observedAt must be canonical UTC RFC3339Nano", ErrAttemptAuthorityConflict)
	}
	if observed.Unix() < process.BirthSeconds {
		return fmt.Errorf("%w: observedAt precedes process birth", ErrAttemptAuthorityConflict)
	}
	return nil
}

// SealProcessObservation derives the detached canonical observation digest.
func SealProcessObservation(observation ProcessObservation) (ProcessObservation, error) {
	observation.ObservationDigest = ""
	digest, err := canonicalDigest(observation)
	if err != nil {
		return ProcessObservation{}, err
	}
	observation.ObservationDigest = digest
	if err := observation.Validate(); err != nil {
		return ProcessObservation{}, err
	}
	return observation, nil
}

type TerminalReason string

const (
	TerminalAttemptCompleted TerminalReason = "attempt-completed"
	TerminalAttemptFailed    TerminalReason = "attempt-failed"
	TerminalAttemptAborted   TerminalReason = "attempt-aborted"
	TerminalOrphanReconciled TerminalReason = "orphan-reconciled"
)

func (r TerminalReason) Validate() error {
	switch r {
	case TerminalAttemptCompleted, TerminalAttemptFailed, TerminalAttemptAborted, TerminalOrphanReconciled:
		return nil
	default:
		return fmt.Errorf("%w: unknown terminal reason %q", ErrAttemptAuthorityConflict, r)
	}
}

// EligibilityTerminalKind is the closed terminal eligibility union committed
// by a terminalization barrier. A barrier never represents an ambiguous
// generic "done": it records exactly one normal completion, cancellation, or
// expiry outcome for the lease projection.
type EligibilityTerminalKind string

const (
	EligibilityTerminalCompleted EligibilityTerminalKind = "completed"
	EligibilityTerminalCancelled EligibilityTerminalKind = "cancelled"
	EligibilityTerminalExpired   EligibilityTerminalKind = "expired"
)

// EligibilityCancelReason mirrors the closed dispatch cancellation vocabulary
// without importing the dispatch read model into the authority package. RB3
// must map these exact values, never free-form text, into LeaseLedger.
type EligibilityCancelReason string

const (
	EligibilityCancelSecurityCriticalRevoke   EligibilityCancelReason = "security-critical-revoke"
	EligibilityCancelRegistrationExpired      EligibilityCancelReason = "registration-expired"
	EligibilityCancelRegistrationIncompatible EligibilityCancelReason = "registration-incompatible"
	EligibilityCancelSnapshotSuperseded       EligibilityCancelReason = "snapshot-superseded"
	EligibilityCancelSnapshotExpired          EligibilityCancelReason = "snapshot-expired"
	EligibilityCancelEvidenceRevoked          EligibilityCancelReason = "evidence-revoked"
	EligibilityCancelEvidenceExpired          EligibilityCancelReason = "evidence-expired"
	EligibilityCancelDeadlineExceeded         EligibilityCancelReason = "deadline-exceeded"
)

func (r EligibilityCancelReason) Validate() error {
	switch r {
	case EligibilityCancelSecurityCriticalRevoke, EligibilityCancelRegistrationExpired, EligibilityCancelRegistrationIncompatible, EligibilityCancelSnapshotSuperseded, EligibilityCancelSnapshotExpired, EligibilityCancelEvidenceRevoked, EligibilityCancelEvidenceExpired, EligibilityCancelDeadlineExceeded:
		return nil
	default:
		return fmt.Errorf("%w: unknown eligibility cancel reason %q", ErrAttemptAuthorityConflict, r)
	}
}

type EligibilityTerminal struct {
	Kind             EligibilityTerminalKind `json:"kind"`
	CompletionReason TerminalReason          `json:"completionReason,omitempty"`
	CancelReason     EligibilityCancelReason `json:"cancelReason,omitempty"`
}

func (terminal EligibilityTerminal) Validate() error {
	switch terminal.Kind {
	case EligibilityTerminalCompleted:
		if terminal.CancelReason != "" {
			return fmt.Errorf("%w: completed eligibility carries cancelReason", ErrAttemptAuthorityConflict)
		}
		return terminal.CompletionReason.Validate()
	case EligibilityTerminalCancelled:
		if terminal.CompletionReason != "" {
			return fmt.Errorf("%w: cancelled eligibility carries completionReason", ErrAttemptAuthorityConflict)
		}
		return terminal.CancelReason.Validate()
	case EligibilityTerminalExpired:
		if terminal.CompletionReason != "" || terminal.CancelReason != "" {
			return fmt.Errorf("%w: expired eligibility carries a reason", ErrAttemptAuthorityConflict)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown eligibility terminal kind %q", ErrAttemptAuthorityConflict, terminal.Kind)
	}
}

type ProcessTerminalKind string

const (
	ProcessAbsent           ProcessTerminalKind = "process-absent"
	ProcessTerminated       ProcessTerminalKind = "process-terminated"
	ProcessIdentityConflict ProcessTerminalKind = "process-identity-conflict"
)

func (kind ProcessTerminalKind) Validate() error {
	switch kind {
	case ProcessAbsent, ProcessTerminated, ProcessIdentityConflict:
		return nil
	default:
		return fmt.Errorf("%w: unknown process terminal kind %q", ErrAttemptAuthorityConflict, kind)
	}
}

type LaunchState string

const (
	LaunchNotAuthorized LaunchState = "not-authorized"
	LaunchUncertain     LaunchState = "launch-uncertain"
	LaunchStarted       LaunchState = "process-started"
)

// AttemptTransition is a sealed union. Only fields required by Kind may be
// populated; strict fact decoding and transition validation reject ambiguity.
type AttemptTransition struct {
	Kind                  AttemptTransitionKind    `json:"kind"`
	Identity              AttemptIdentity          `json:"identity"`
	LaunchAuthorizationID string                   `json:"launchAuthorizationId,omitempty"`
	CommandID             string                   `json:"commandId,omitempty"`
	ObservedAt            string                   `json:"observedAt,omitempty"`
	Process               ProcessObservation       `json:"process,omitempty"`
	TerminalizationID     string                   `json:"terminalizationId,omitempty"`
	EligibilityTerminal   EligibilityTerminal      `json:"eligibilityTerminal,omitempty"`
	ProcessTerminalKind   ProcessTerminalKind      `json:"processTerminalKind,omitempty"`
	ObservationDigest     string                   `json:"terminalObservationDigest,omitempty"`
	ReceiptDigest         string                   `json:"receiptDigest,omitempty"`
	AdmissionFactDigest   string                   `json:"admissionFactDigest,omitempty"`
	AdmissionSequence     uint64                   `json:"admissionSequence,omitempty"`
	LaunchClosure         launchidentity.ClosureV1 `json:"launchClosure,omitempty"`
	LaunchMaterialsDigest string                   `json:"launchMaterialsDigest,omitempty"`
	AgentLaunchSpecDigest string                   `json:"agentLaunchSpecDigest,omitempty"`
}

type AttemptAuthorityState struct {
	Identity                   AttemptIdentity     `json:"identity"`
	Revision                   uint64              `json:"revision"`
	HeadDigest                 string              `json:"headDigest"`
	OpenedDigest               string              `json:"openedDigest"`
	LaunchState                LaunchState         `json:"launchState"`
	LaunchAuthorizationID      string              `json:"launchAuthorizationId,omitempty"`
	LaunchAuthorizedDigest     string              `json:"launchAuthorizedDigest,omitempty"`
	CommandID                  string              `json:"commandId,omitempty"`
	ObservedAt                 string              `json:"observedAt,omitempty"`
	Process                    ProcessObservation  `json:"process,omitempty"`
	ProcessStartedDigest       string              `json:"processStartedDigest,omitempty"`
	CommittedResultFactDigest  string              `json:"committedResultFactDigest,omitempty"`
	CommittedResultSequence    uint64              `json:"committedResultSequence,omitempty"`
	BarrierDigest              string              `json:"barrierDigest,omitempty"`
	TerminalizationID          string              `json:"terminalizationId,omitempty"`
	EligibilityTerminal        EligibilityTerminal `json:"eligibilityTerminal,omitempty"`
	AdmissionClosed            bool                `json:"admissionClosed"`
	BarrierAdmissionFactDigest string              `json:"barrierAdmissionFactDigest,omitempty"`
	BarrierAdmissionSequence   uint64              `json:"barrierAdmissionSequence,omitempty"`
	TerminalGeneration         int64               `json:"terminalGeneration,omitempty"`
	CleanupBindingDigest       string              `json:"cleanupBindingDigest,omitempty"`
	ProcessTerminalDigest      string              `json:"processTerminalDigest,omitempty"`
	ProcessTerminalKind        ProcessTerminalKind `json:"processTerminalKind,omitempty"`
	ProcessTerminalObservation string              `json:"processTerminalObservationDigest,omitempty"`
	AllocationTerminalDigest   string              `json:"allocationTerminalDigest,omitempty"`
	AllocationReceiptDigest    string              `json:"allocationReceiptDigest,omitempty"`
	CleanupCompletedDigest     string              `json:"cleanupCompletedDigest,omitempty"`
	CleanupReleasedDigest      string              `json:"cleanupReleasedDigest,omitempty"`
	// PendingEffect* is the durable exclusion barrier established by an
	// effect-intent fact. It is cleared only by the matching reconcile fact;
	// receipt alone remains an observation and cannot unlock the Attempt.
	PendingEffectID                  string                         `json:"pendingEffectId,omitempty"`
	PendingEffectIntentFactDigest    string                         `json:"pendingEffectIntentFactDigest,omitempty"`
	PendingEffectRecordDigest        string                         `json:"pendingEffectRecordDigest,omitempty"`
	PendingEffectMarkerDigest        string                         `json:"pendingEffectMarkerDigest,omitempty"`
	PendingEffectPhase               EffectPhase                    `json:"pendingEffectPhase,omitempty"`
	AllocationProvisionEffectDigest  string                         `json:"allocationProvisionEffectDigest,omitempty"`
	AllocationProvisionReceiptDigest string                         `json:"allocationProvisionReceiptDigest,omitempty"`
	AllocationTerminateEffectDigest  string                         `json:"allocationTerminateEffectDigest,omitempty"`
	AllocationTerminateReceiptDigest string                         `json:"allocationTerminateReceiptDigest,omitempty"`
	EffectInterventionDigest         string                         `json:"effectInterventionDigest,omitempty"`
	LaunchClosure                    launchidentity.StoredClosureV1 `json:"launchClosure,omitempty"`
	LaunchMaterialsDigest            string                         `json:"launchMaterialsDigest,omitempty"`
	AgentLaunchSpecDigest            string                         `json:"agentLaunchSpecDigest,omitempty"`
}

// AttemptAppendResult distinguishes a fresh authority append from an exact
// idempotent replay. Callers may perform a one-shot external effect (most
// importantly spawn after launch-authorized) only when Appended is true.
// TransitionDigest binds the exact transition fact even when State.HeadDigest
// has advanced beyond it during an exact replay.
type AttemptAppendResult struct {
	State            AttemptAuthorityState
	Appended         bool
	TransitionDigest string
}

type attemptAuthorityFact struct {
	ProtocolRevision     string            `json:"protocolRevision"`
	FactType             string            `json:"factType"`
	Sequence             int64             `json:"sequence"`
	AttemptKey           string            `json:"attemptKey"`
	Revision             uint64            `json:"revision"`
	PreviousDigest       string            `json:"previousDigest,omitempty"`
	Transition           AttemptTransition `json:"transition"`
	AdmissionClosed      bool              `json:"admissionClosed,omitempty"`
	TerminalGeneration   int64             `json:"terminalGeneration,omitempty"`
	CleanupBindingDigest string            `json:"cleanupBindingDigest,omitempty"`
	Digest               string            `json:"digest"`
}

// CompareAndAppend is the unprivileged mutation surface. Authority-bearing
// transitions are deliberately rejected in favor of their held-authority
// entry points. The expected revision and head are mandatory CAS inputs after
// attempt-opened.
func (s *ingressDurableStore) CompareAndAppend(expectedRevision uint64, expectedHead string, transition AttemptTransition) (AttemptAppendResult, error) {
	if transition.Kind == attemptTransitionResultAdmitted {
		return AttemptAppendResult{}, fmt.Errorf("%w: result-admitted is reserved for Ingress", ErrAttemptAuthorityConflict)
	}
	if isCleanupTransition(transition.Kind) {
		return AttemptAppendResult{}, fmt.Errorf("%w: cleanup transitions require CompareAndAppendCleanup", ErrCleanupUnauthorized)
	}
	if transition.Kind == AttemptTransitionTerminalizationBarrier {
		return AttemptAppendResult{}, fmt.Errorf("%w: terminalization barrier requires CompareAndAppendBarrier", ErrRunAuthorityUnauthorized)
	}
	if isRunAuthorizedTransition(transition.Kind) {
		return AttemptAppendResult{}, fmt.Errorf("%w: %s requires CompareAndAppendAuthorized", ErrRunAuthorityUnauthorized, transition.Kind)
	}
	return s.compareAndAppend(expectedRevision, expectedHead, transition, false)
}

func isRunAuthorizedTransition(kind AttemptTransitionKind) bool {
	switch kind {
	case AttemptTransitionOpened, AttemptTransitionLaunchAuthorized, AttemptTransitionProcessStarted:
		return true
	default:
		return false
	}
}

func isCleanupTransition(kind AttemptTransitionKind) bool {
	switch kind {
	case AttemptTransitionProcessTerminal, AttemptTransitionAllocationTerminated, AttemptTransitionCleanupCompleted, AttemptTransitionCleanupReleased:
		return true
	default:
		return false
	}
}

func (s *ingressDurableStore) compareAndAppend(expectedRevision uint64, expectedHead string, transition AttemptTransition, internalAdmission bool) (AttemptAppendResult, error) {
	projection := newAuthorityProjection()
	var result AttemptAppendResult
	err := s.transact(projection, func() error {
		if err := transition.Identity.Validate(); err != nil {
			return err
		}
		if err := validateTransitionShape(transition); err != nil {
			return err
		}
		key, err := transition.Identity.Key()
		if err != nil {
			return err
		}
		prior, exists := projection.attempts[key]
		if replay, ok := exactTransitionReplay(prior, exists, transition); ok {
			result = AttemptAppendResult{State: replay, TransitionDigest: transitionDigest(replay, transition.Kind)}
			return nil
		}
		if transition.Kind == AttemptTransitionOpened {
			if exists || expectedRevision != 0 || expectedHead != "" {
				return ErrAttemptAuthorityConflict
			}
		} else if !exists {
			return ErrAttemptAuthorityUnknown
		} else if prior.Revision != expectedRevision || prior.HeadDigest != expectedHead {
			return ErrAttemptAuthorityConflict
		}
		if transition.Kind == attemptTransitionResultAdmitted && !internalAdmission {
			return ErrAttemptAuthorityConflict
		}
		fact := &attemptAuthorityFact{
			ProtocolRevision: attemptAuthorityProtocolRevision,
			FactType:         string(transition.Kind),
			Sequence:         s.nextSequence,
			AttemptKey:       key,
			Revision:         expectedRevision + 1,
			PreviousDigest:   expectedHead,
			Transition:       transition,
		}
		if err := prepareAttemptFact(prior, exists, fact); err != nil {
			return err
		}
		if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
			return err
		}
		s.nextSequence++
		if err := applyAttemptAuthorityFactValue(*fact, projection); err != nil {
			return fmt.Errorf("resultingress: appended attempt fact failed projection: %w", err)
		}
		result = AttemptAppendResult{State: projection.attempts[key], Appended: true, TransitionDigest: fact.Digest}
		return nil
	})
	return result, err
}

func transitionDigest(state AttemptAuthorityState, kind AttemptTransitionKind) string {
	switch kind {
	case AttemptTransitionOpened:
		return state.OpenedDigest
	case AttemptTransitionLaunchAuthorized:
		return state.LaunchAuthorizedDigest
	case AttemptTransitionProcessStarted:
		return state.ProcessStartedDigest
	case attemptTransitionResultAdmitted:
		return state.CommittedResultFactDigest
	case AttemptTransitionTerminalizationBarrier:
		return state.BarrierDigest
	case AttemptTransitionProcessTerminal:
		return state.ProcessTerminalDigest
	case AttemptTransitionAllocationTerminated:
		return state.AllocationTerminalDigest
	case AttemptTransitionCleanupCompleted:
		return state.CleanupCompletedDigest
	case AttemptTransitionCleanupReleased:
		return state.CleanupReleasedDigest
	default:
		return ""
	}
}

func prepareAttemptFact(prior AttemptAuthorityState, exists bool, fact *attemptAuthorityFact) error {
	t := fact.Transition
	if err := validateTransitionShape(t); err != nil {
		return err
	}
	if exists && (prior.PendingEffectIntentFactDigest != "" || prior.EffectInterventionDigest != "") {
		// An admitted effect owns the Attempt head until a matching receipt and
		// reconcile decision close it. Exact transition replays are handled before
		// this function and remain read-only.
		return ErrAttemptAuthorityOrder
	}
	switch t.Kind {
	case AttemptTransitionOpened:
		return nil
	case AttemptTransitionLaunchAuthorized:
		if prior.LaunchState != LaunchNotAuthorized || prior.BarrierDigest != "" || prior.AllocationProvisionEffectDigest == "" || prior.AllocationProvisionReceiptDigest == "" {
			return ErrAttemptAuthorityOrder
		}
	case AttemptTransitionProcessStarted:
		if prior.LaunchState != LaunchUncertain || prior.BarrierDigest != "" {
			return ErrAttemptAuthorityOrder
		}
		if t.LaunchMaterialsDigest != prior.LaunchMaterialsDigest || t.AgentLaunchSpecDigest != prior.AgentLaunchSpecDigest || !processMatchesRuntime(t.Process, prior.LaunchClosure.RuntimeExecutable) {
			return ErrAttemptAuthorityOrder
		}
	case attemptTransitionResultAdmitted:
		if prior.ProcessStartedDigest == "" || prior.BarrierDigest != "" {
			return ErrAttemptAuthorityOrder
		}
	case AttemptTransitionTerminalizationBarrier:
		if prior.BarrierDigest != "" {
			return ErrAttemptAuthorityOrder
		}
		// The barrier always closes result admission. Whether it bound an
		// already-admitted result or closed an empty admission slot is encoded
		// by AdmissionFactDigest/AdmissionSequence, not by this state bit.
		fact.AdmissionClosed = true
		fact.Transition.AdmissionFactDigest = prior.CommittedResultFactDigest
		fact.Transition.AdmissionSequence = prior.CommittedResultSequence
		fact.TerminalGeneration = t.Identity.DispatchGeneration + 1
		binding := cleanupBindingMaterial{
			AttemptKey: fact.AttemptKey, TerminalizationID: t.TerminalizationID,
			BarrierPriorDigest: prior.HeadDigest, TerminalGeneration: fact.TerminalGeneration,
			OrchestratorID: t.Identity.OrchestratorID, RunAuthorityDigest: t.Identity.RunAuthorityDigest,
			Operations: cleanupOperations(),
		}
		digest, err := canonicalDigest(binding)
		if err != nil {
			return err
		}
		fact.CleanupBindingDigest = digest
	case AttemptTransitionProcessTerminal:
		if prior.BarrierDigest == "" || prior.ProcessTerminalDigest != "" || prior.CleanupReleasedDigest != "" {
			return ErrAttemptAuthorityOrder
		}
		if prior.LaunchState == LaunchUncertain && t.ProcessTerminalKind != ProcessAbsent && t.ProcessTerminalKind != ProcessIdentityConflict {
			return ErrAttemptAuthorityOrder
		}
		if prior.LaunchState == LaunchNotAuthorized && t.ProcessTerminalKind != ProcessAbsent {
			return ErrAttemptAuthorityOrder
		}
	case AttemptTransitionAllocationTerminated:
		if prior.ProcessTerminalKind != ProcessAbsent && prior.ProcessTerminalKind != ProcessTerminated || prior.AllocationTerminalDigest != "" || prior.AllocationTerminateEffectDigest == "" || prior.AllocationTerminateReceiptDigest == "" || t.ReceiptDigest != prior.AllocationTerminateReceiptDigest {
			return ErrAttemptAuthorityOrder
		}
	case AttemptTransitionCleanupCompleted:
		if prior.AllocationTerminalDigest == "" || prior.CleanupCompletedDigest != "" {
			return ErrAttemptAuthorityOrder
		}
	case AttemptTransitionCleanupReleased:
		if prior.CleanupCompletedDigest == "" || prior.CleanupReleasedDigest != "" {
			return ErrAttemptAuthorityOrder
		}
	default:
		return fmt.Errorf("%w: unknown transition %q", ErrAttemptAuthorityConflict, t.Kind)
	}
	_ = exists
	return nil
}

func validateTransitionShape(t AttemptTransition) error {
	if err := t.Identity.Validate(); err != nil {
		return err
	}
	if t.Kind != AttemptTransitionLaunchAuthorized && t.Kind != AttemptTransitionProcessStarted && (!zeroLaunchClosure(t.LaunchClosure) || t.LaunchMaterialsDigest != "" || t.AgentLaunchSpecDigest != "") {
		return fmt.Errorf("%w: launch identity on unrelated transition", ErrAttemptAuthorityConflict)
	}
	switch t.Kind {
	case AttemptTransitionOpened:
		if transitionHasAnyPayload(t) {
			return fmt.Errorf("%w: attempt-opened carries unrelated payload", ErrAttemptAuthorityConflict)
		}
		return nil
	case AttemptTransitionLaunchAuthorized:
		if strings.TrimSpace(t.LaunchAuthorizationID) == "" || t.LaunchClosure.Validate() != nil || t.LaunchMaterialsDigest != "" || t.AgentLaunchSpecDigest != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.TerminalizationID != "" || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 {
			return fmt.Errorf("%w: launchAuthorizationId is empty", ErrAttemptAuthorityConflict)
		}
	case AttemptTransitionProcessStarted:
		if strings.TrimSpace(t.CommandID) == "" || t.Process.Validate() != nil || validateObservedAt(t.ObservedAt, t.Process) != nil || !validLaunchDigest(t.LaunchMaterialsDigest) || !validLaunchDigest(t.AgentLaunchSpecDigest) || !zeroLaunchClosure(t.LaunchClosure) || t.LaunchAuthorizationID != "" || t.TerminalizationID != "" || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 {
			return fmt.Errorf("%w: incomplete process-started transition", ErrAttemptAuthorityConflict)
		}
	case attemptTransitionResultAdmitted:
		if err := requireDigest("admissionFactDigest", t.AdmissionFactDigest); err != nil || t.AdmissionSequence == 0 || t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.TerminalizationID != "" || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" {
			return fmt.Errorf("%w: invalid result admission binding", ErrAttemptAuthorityConflict)
		}
	case AttemptTransitionTerminalizationBarrier:
		if strings.TrimSpace(t.TerminalizationID) == "" || t.EligibilityTerminal.Validate() != nil || t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" {
			return fmt.Errorf("%w: invalid terminalization barrier", ErrAttemptAuthorityConflict)
		}
	case AttemptTransitionProcessTerminal:
		if strings.TrimSpace(t.TerminalizationID) == "" || t.ProcessTerminalKind.Validate() != nil || t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ReceiptDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 {
			return fmt.Errorf("%w: invalid process terminal", ErrAttemptAuthorityConflict)
		}
		if err := requireDigest("terminalObservationDigest", t.ObservationDigest); err != nil {
			return fmt.Errorf("%w: %v", ErrAttemptAuthorityConflict, err)
		}
	case AttemptTransitionAllocationTerminated:
		if strings.TrimSpace(t.TerminalizationID) == "" || t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 {
			return fmt.Errorf("%w: terminalizationId is empty", ErrAttemptAuthorityConflict)
		}
		if err := requireDigest("receiptDigest", t.ReceiptDigest); err != nil {
			return fmt.Errorf("%w: %v", ErrAttemptAuthorityConflict, err)
		}
	case AttemptTransitionCleanupCompleted, AttemptTransitionCleanupReleased:
		if strings.TrimSpace(t.TerminalizationID) == "" || t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 {
			return fmt.Errorf("%w: terminalizationId is empty", ErrAttemptAuthorityConflict)
		}
	default:
		return fmt.Errorf("%w: unknown transition %q", ErrAttemptAuthorityConflict, t.Kind)
	}
	return nil
}

func transitionHasAnyPayload(t AttemptTransition) bool {
	return t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.TerminalizationID != "" || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 || !zeroLaunchClosure(t.LaunchClosure) || t.LaunchMaterialsDigest != "" || t.AgentLaunchSpecDigest != ""
}

func zeroLaunchClosure(closure launchidentity.ClosureV1) bool {
	return reflect.DeepEqual(closure, launchidentity.ClosureV1{})
}

func validLaunchDigest(value string) bool { return requireDigest("launchDigest", value) == nil }

func processMatchesRuntime(process ProcessObservation, runtime launchidentity.ObjectV1) bool {
	return process.ExecutablePath == runtime.CanonicalPath && process.ExecutableDevice == runtime.Device && process.ExecutableInode == runtime.Inode && process.ExecutableSize == runtime.Size && process.ExecutableType == runtime.FileType && process.ExecutableOwner == runtime.UID && process.ExecutableGroup == runtime.GID && process.ExecutableMode == runtime.Mode && process.ExecutableLinkCount == runtime.LinkCount && process.ExecutableSHA256 == runtime.RawSHA256
}

func exactTransitionReplay(state AttemptAuthorityState, exists bool, t AttemptTransition) (AttemptAuthorityState, bool) {
	if !exists || state.Identity != t.Identity {
		return AttemptAuthorityState{}, false
	}
	switch t.Kind {
	case AttemptTransitionOpened:
		return state, state.OpenedDigest != ""
	case AttemptTransitionLaunchAuthorized:
		stored, err := t.LaunchClosure.Stored()
		return state, err == nil && state.LaunchAuthorizationID == t.LaunchAuthorizationID && state.LaunchAuthorizedDigest != "" && state.LaunchClosure == stored
	case AttemptTransitionProcessStarted:
		return state, state.ProcessStartedDigest != "" && state.CommandID == t.CommandID && state.ObservedAt == t.ObservedAt && state.Process == t.Process && state.LaunchMaterialsDigest == t.LaunchMaterialsDigest && state.AgentLaunchSpecDigest == t.AgentLaunchSpecDigest
	case attemptTransitionResultAdmitted:
		return state, state.CommittedResultFactDigest == t.AdmissionFactDigest && state.CommittedResultSequence == t.AdmissionSequence
	case AttemptTransitionTerminalizationBarrier:
		return state, state.BarrierDigest != "" && state.TerminalizationID == t.TerminalizationID && state.EligibilityTerminal == t.EligibilityTerminal
	case AttemptTransitionProcessTerminal:
		return state, state.ProcessTerminalDigest != "" && state.TerminalizationID == t.TerminalizationID && state.ProcessTerminalKind == t.ProcessTerminalKind && state.ProcessTerminalObservation == t.ObservationDigest
	case AttemptTransitionAllocationTerminated:
		return state, state.AllocationTerminalDigest != "" && state.TerminalizationID == t.TerminalizationID && state.AllocationReceiptDigest == t.ReceiptDigest
	case AttemptTransitionCleanupCompleted:
		return state, state.CleanupCompletedDigest != "" && state.TerminalizationID == t.TerminalizationID
	case AttemptTransitionCleanupReleased:
		return state, state.CleanupReleasedDigest != "" && state.TerminalizationID == t.TerminalizationID
	default:
		return AttemptAuthorityState{}, false
	}
}

func (s *ingressDurableStore) AttemptState(identity AttemptIdentity) (AttemptAuthorityState, bool, error) {
	key, err := identity.Key()
	if err != nil {
		return AttemptAuthorityState{}, false, err
	}
	projection := newAuthorityProjection()
	var state AttemptAuthorityState
	var found bool
	err = s.transact(projection, func() error {
		state, found = projection.attempts[key]
		return nil
	})
	return state, found, err
}

// AttemptStates replays the single authority log and returns deterministic
// value copies ordered by AttemptIdentity.Key. It is the restart-safe source
// for composition; callers must not maintain an independent identity index.
func (s *ingressDurableStore) AttemptStates() ([]AttemptAuthorityState, error) {
	return s.attemptStates(false)
}

// PendingAttemptStates is the deterministic subset whose cleanup authority
// has not been released. It includes pre-barrier attempts because a restart
// must reconcile launch-uncertain state before deciding terminalization.
func (s *ingressDurableStore) PendingAttemptStates() ([]AttemptAuthorityState, error) {
	return s.attemptStates(true)
}

func (s *ingressDurableStore) attemptStates(pendingOnly bool) ([]AttemptAuthorityState, error) {
	projection := newAuthorityProjection()
	var states []AttemptAuthorityState
	err := s.transact(projection, func() error {
		keys := make([]string, 0, len(projection.attempts))
		for key, state := range projection.attempts {
			if pendingOnly && state.CleanupReleasedDigest != "" {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		states = make([]AttemptAuthorityState, 0, len(keys))
		for _, key := range keys {
			states = append(states, projection.attempts[key])
		}
		return nil
	})
	return states, err
}

type RunAuthorityBinding struct {
	AuthorityNamespaceID authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	RunID                string                         `json:"runId"`
	OrchestratorID       string                         `json:"orchestratorId"`
	RunAuthorityDigest   string                         `json:"runAuthorityDigest"`
}

// AttemptAuthorizationRequest binds an opened/launch/process identity mutation
// to the held current Run authority. The request contains no bearer secret.
type AttemptAuthorizationRequest struct {
	Identity            AttemptIdentity
	CurrentRunAuthority RunAuthorityBinding
}

type CurrentRunAuthorityVerifier interface {
	// WithCurrentRunAuthority holds the verified current Run authority for the
	// complete callback. Returning success without invoking fn is invalid. This
	// closes the check-then-append ownership drift window during cleanup CAS.
	WithCurrentRunAuthority(context.Context, RunAuthorityBinding, func() error) error
}

func withCurrentRunAuthority(ctx context.Context, verifier CurrentRunAuthorityVerifier, binding RunAuthorityBinding, fn func() error) error {
	if verifier == nil {
		return fmt.Errorf("%w: verifier is required", ErrRunAuthorityUnauthorized)
	}
	var callbackGate sync.Mutex
	called := false
	doubleCall := false
	closed := false
	var callbackErr error
	verifierErr := verifier.WithCurrentRunAuthority(ctx, binding, func() error {
		callbackGate.Lock()
		defer callbackGate.Unlock()
		if closed || called {
			doubleCall = true
			return ErrRunAuthorityUnauthorized
		}
		called = true
		callbackErr = fn()
		return callbackErr
	})
	callbackGate.Lock()
	closed = true
	calledOnce, invokedTwice, heldCallbackErr := called, doubleCall, callbackErr
	callbackGate.Unlock()
	if invokedTwice {
		return fmt.Errorf("%w: verifier invoked held callback more than once", ErrRunAuthorityUnauthorized)
	}
	if heldCallbackErr != nil {
		return heldCallbackErr
	}
	if verifierErr != nil || !calledOnce {
		return fmt.Errorf("%w: verifier rejected or did not hold authority: %v", ErrRunAuthorityUnauthorized, verifierErr)
	}
	return nil
}

// CompareAndAppendAuthorized is the sole mutation path for attempt-opened,
// launch-authorized and process-started. The held authority spans complete
// replay/read/CAS, including exact replay, so a stale orchestrator never gets
// a fresh Appended=true launch fact.
func (s *ingressDurableStore) CompareAndAppendAuthorized(ctx context.Context, verifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request AttemptAuthorizationRequest, transition AttemptTransition) (AttemptAppendResult, error) {
	if !isRunAuthorizedTransition(transition.Kind) || transition.Identity != request.Identity {
		return AttemptAppendResult{}, ErrRunAuthorityUnauthorized
	}
	if err := validateTransitionShape(transition); err != nil {
		return AttemptAppendResult{}, err
	}
	wantRun := runAuthorityBindingFor(request.Identity)
	if request.CurrentRunAuthority != wantRun {
		return AttemptAppendResult{}, ErrRunAuthorityUnauthorized
	}
	var result AttemptAppendResult
	err := withCurrentRunAuthority(ctx, verifier, request.CurrentRunAuthority, func() error {
		var appendErr error
		result, appendErr = s.compareAndAppend(expectedRevision, expectedHead, transition, false)
		return appendErr
	})
	return result, err
}

func runAuthorityBindingFor(identity AttemptIdentity) RunAuthorityBinding {
	return RunAuthorityBinding{AuthorityNamespaceID: identity.AuthorityNamespaceID, RunID: identity.RunID, OrchestratorID: identity.OrchestratorID, RunAuthorityDigest: identity.RunAuthorityDigest}
}

// BarrierAuthorizationRequest binds the held current Run authority to the
// exact Attempt tuple being terminalized. It contains no bearer token.
type BarrierAuthorizationRequest struct {
	Identity            AttemptIdentity
	CurrentRunAuthority RunAuthorityBinding
}

// CompareAndAppendBarrier is the only terminalization-barrier mutation. The
// verifier must hold current Run authority across the complete authority
// replay/read/CAS callback. Exact transition replay still passes this gate.
func (s *ingressDurableStore) CompareAndAppendBarrier(ctx context.Context, verifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request BarrierAuthorizationRequest, transition AttemptTransition) (AttemptAppendResult, error) {
	if transition.Kind != AttemptTransitionTerminalizationBarrier || transition.Identity != request.Identity {
		return AttemptAppendResult{}, ErrRunAuthorityUnauthorized
	}
	if err := validateTransitionShape(transition); err != nil {
		return AttemptAppendResult{}, err
	}
	wantRun := runAuthorityBindingFor(request.Identity)
	if request.CurrentRunAuthority != wantRun {
		return AttemptAppendResult{}, ErrRunAuthorityUnauthorized
	}
	var result AttemptAppendResult
	err := withCurrentRunAuthority(ctx, verifier, request.CurrentRunAuthority, func() error {
		var appendErr error
		result, appendErr = s.compareAndAppend(expectedRevision, expectedHead, transition, false)
		return appendErr
	})
	return result, err
}

type CleanupOperation string

const (
	CleanupInspect   CleanupOperation = "inspect"
	CleanupReconcile CleanupOperation = "reconcile"
	CleanupSignal    CleanupOperation = "signal"
	CleanupTerminate CleanupOperation = "terminate"
)

func (operation CleanupOperation) Validate() error {
	switch operation {
	case CleanupInspect, CleanupReconcile, CleanupSignal, CleanupTerminate:
		return nil
	default:
		return fmt.Errorf("%w: cleanup operation %q is outside allowlist", ErrCleanupUnauthorized, operation)
	}
}

func cleanupOperations() []CleanupOperation {
	return []CleanupOperation{CleanupInspect, CleanupReconcile, CleanupSignal, CleanupTerminate}
}

type cleanupBindingMaterial struct {
	AttemptKey         string             `json:"attemptKey"`
	TerminalizationID  string             `json:"terminalizationId"`
	BarrierPriorDigest string             `json:"barrierPriorDigest"`
	TerminalGeneration int64              `json:"terminalGeneration"`
	OrchestratorID     string             `json:"orchestratorId"`
	RunAuthorityDigest string             `json:"runAuthorityDigest"`
	Operations         []CleanupOperation `json:"operations"`
}

type CleanupAuthorizationRequest struct {
	Identity             AttemptIdentity     `json:"identity"`
	CurrentRunAuthority  RunAuthorityBinding `json:"currentRunAuthority"`
	TerminalizationID    string              `json:"terminalizationId"`
	TerminalGeneration   int64               `json:"terminalGeneration"`
	CleanupBindingDigest string              `json:"cleanupBindingDigest"`
	Operation            CleanupOperation    `json:"operation"`
}

// AuthorizeCleanup is a read-only preflight and grants no permission to a
// later side effect. Production cleanup must use WithAuthorizedCleanup so the
// Provider operation runs while current Run authority is held.
func (s *ingressDurableStore) AuthorizeCleanup(ctx context.Context, verifier CurrentRunAuthorityVerifier, request CleanupAuthorizationRequest) error {
	return s.WithAuthorizedCleanup(ctx, verifier, request, func(AttemptAuthorityState) error { return nil })
}

// WithAuthorizedCleanup verifies the exact current Run/barrier/generation and
// operation allowlist, then invokes fn before releasing that authority hold.
// cleanupBindingDigest is evidence, never a bearer capability. RB3 must place
// the external Provider cleanup effect inside fn.
func (s *ingressDurableStore) WithAuthorizedCleanup(ctx context.Context, verifier CurrentRunAuthorityVerifier, request CleanupAuthorizationRequest, fn func(AttemptAuthorityState) error) error {
	if fn == nil {
		return ErrCleanupUnauthorized
	}
	if err := request.Operation.Validate(); err != nil {
		return err
	}
	err := withCurrentRunAuthority(ctx, verifier, request.CurrentRunAuthority, func() error {
		state, found, err := s.AttemptState(request.Identity)
		if err != nil {
			return err
		}
		if !found || !cleanupRequestMatchesState(state, request) || state.CleanupReleasedDigest != "" {
			return ErrCleanupUnauthorized
		}
		if !cleanupEffectAllowed(state, request.Operation) {
			return ErrCleanupUnauthorized
		}
		return fn(state)
	})
	if errors.Is(err, ErrRunAuthorityUnauthorized) {
		return fmt.Errorf("%w: %v", ErrCleanupUnauthorized, err)
	}
	return err
}

func cleanupEffectAllowed(state AttemptAuthorityState, operation CleanupOperation) bool {
	if state.CleanupReleasedDigest != "" || state.CleanupCompletedDigest != "" || state.AllocationTerminalDigest != "" {
		return false
	}
	if state.PendingEffectIntentFactDigest != "" || state.EffectInterventionDigest != "" {
		// Read-only inspection remains available for diagnosis. Recovery of this
		// effect must use RecoverPendingEffect so it is captured by the same
		// authority chain; the cleanup callback cannot bypass that ledger.
		return operation == CleanupInspect
	}
	if state.ProcessTerminalDigest == "" {
		switch operation {
		case CleanupInspect, CleanupReconcile:
			return true
		case CleanupSignal:
			return state.LaunchState == LaunchStarted
		default:
			// Provider/allocation termination is not legal until the exact process
			// has a terminal authority fact.
			return false
		}
	}
	// After process terminal, process Signal is permanently closed. Provider
	// termination is a distinct effect and is legal only until its receipt is
	// appended; identity conflict remains inspect/reconcile-only.
	if state.ProcessTerminalKind == ProcessIdentityConflict {
		return operation == CleanupInspect || operation == CleanupReconcile
	}
	return operation == CleanupInspect || operation == CleanupReconcile || operation == CleanupTerminate
}

func cleanupAppendOperationAllowed(kind AttemptTransitionKind, operation CleanupOperation) bool {
	switch kind {
	case AttemptTransitionProcessTerminal:
		return operation == CleanupInspect || operation == CleanupReconcile
	case AttemptTransitionAllocationTerminated:
		return operation == CleanupTerminate || operation == CleanupReconcile
	case AttemptTransitionCleanupCompleted, AttemptTransitionCleanupReleased:
		return operation == CleanupReconcile
	default:
		return false
	}
}

func cleanupAppendAllowedInPhase(state AttemptAuthorityState, kind AttemptTransitionKind, exactReplay bool) bool {
	switch {
	case state.CleanupReleasedDigest != "":
		return exactReplay && kind == AttemptTransitionCleanupReleased
	case state.CleanupCompletedDigest != "":
		return kind == AttemptTransitionCleanupReleased || exactReplay && kind == AttemptTransitionCleanupCompleted
	case state.AllocationTerminalDigest != "":
		return kind == AttemptTransitionCleanupCompleted || exactReplay && kind == AttemptTransitionAllocationTerminated
	case state.ProcessTerminalDigest != "":
		return kind == AttemptTransitionAllocationTerminated || exactReplay && kind == AttemptTransitionProcessTerminal
	default:
		return kind == AttemptTransitionProcessTerminal
	}
}

func cleanupRequestMatchesState(state AttemptAuthorityState, request CleanupAuthorizationRequest) bool {
	wantRun := RunAuthorityBinding{AuthorityNamespaceID: state.Identity.AuthorityNamespaceID, RunID: state.Identity.RunID, OrchestratorID: state.Identity.OrchestratorID, RunAuthorityDigest: state.Identity.RunAuthorityDigest}
	return state.BarrierDigest != "" && state.Identity == request.Identity && state.TerminalizationID == request.TerminalizationID && state.TerminalGeneration == request.TerminalGeneration && state.CleanupBindingDigest == request.CleanupBindingDigest && request.CurrentRunAuthority == wantRun
}

// CompareAndAppendCleanup couples the use-time current Run authority check to
// a cleanup-only CAS append. The subsequent CAS rechecks the exact current
// Attempt revision/head, so a concurrent barrier/release cannot reuse the
// authorization decision.
func (s *ingressDurableStore) CompareAndAppendCleanup(ctx context.Context, verifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request CleanupAuthorizationRequest, transition AttemptTransition) (AttemptAppendResult, error) {
	if !isCleanupTransition(transition.Kind) || transition.Identity != request.Identity || transition.TerminalizationID != request.TerminalizationID {
		return AttemptAppendResult{}, ErrCleanupUnauthorized
	}
	if err := validateTransitionShape(transition); err != nil {
		return AttemptAppendResult{}, err
	}
	if err := request.Operation.Validate(); err != nil {
		return AttemptAppendResult{}, err
	}
	var result AttemptAppendResult
	err := withCurrentRunAuthority(ctx, verifier, request.CurrentRunAuthority, func() error {
		state, found, err := s.AttemptState(request.Identity)
		if err != nil {
			return err
		}
		if !found || !cleanupRequestMatchesState(state, request) {
			return ErrCleanupUnauthorized
		}
		replay, exactReplay := exactTransitionReplay(state, true, transition)
		if !cleanupAppendOperationAllowed(transition.Kind, request.Operation) || !cleanupAppendAllowedInPhase(state, transition.Kind, exactReplay) {
			return ErrCleanupUnauthorized
		}
		if state.CleanupReleasedDigest != "" {
			// Release revokes every future cleanup effect. The sole legal operation
			// is a no-side-effect exact replay of the already durable release fact,
			// still under current Run authority and exact tuple verification.
			result = AttemptAppendResult{State: replay, TransitionDigest: transitionDigest(replay, transition.Kind)}
			return nil
		}
		if exactReplay {
			result = AttemptAppendResult{State: replay, TransitionDigest: transitionDigest(replay, transition.Kind)}
			return nil
		}
		var appendErr error
		result, appendErr = s.compareAndAppend(expectedRevision, expectedHead, transition, false)
		return appendErr
	})
	if errors.Is(err, ErrRunAuthorityUnauthorized) {
		return AttemptAppendResult{}, fmt.Errorf("%w: %v", ErrCleanupUnauthorized, err)
	}
	return result, err
}

func newAuthorityProjection() *Ingress {
	return &Ingress{
		admitted:          make(map[string]admittedEntry),
		attempts:          make(map[string]AttemptAuthorityState),
		effects:           make(map[string]EffectAuthorityState),
		effectCommands:    make(map[string]string),
		effectIdempotency: make(map[string]string),
		effectMarkers:     make(map[string]string),
	}
}

func applyAttemptAuthorityLine(line []byte, in *Ingress, wantSequence int64) error {
	canonicalLine, err := canonical.JSON(line)
	if err != nil || !bytes.Equal(canonicalLine, line) {
		return fmt.Errorf("%w: attempt authority line is not canonical", ErrAttemptAuthorityConflict)
	}
	var fact attemptAuthorityFact
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		return fmt.Errorf("%w: %v", ErrAttemptAuthorityConflict, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON value", ErrAttemptAuthorityConflict)
	}
	if fact.ProtocolRevision != attemptAuthorityProtocolRevision || fact.Sequence != wantSequence || fact.FactType != string(fact.Transition.Kind) {
		return ErrAttemptAuthorityConflict
	}
	stored := fact.Digest
	fact.Digest = ""
	digest, err := canonicalDigest(fact)
	if err != nil || stored == "" || digest != stored {
		return ErrAttemptAuthorityConflict
	}
	fact.Digest = stored
	return applyAttemptAuthorityFactValue(fact, in)
}

func applyAttemptAuthorityFactValue(fact attemptAuthorityFact, in *Ingress) error {
	if err := fact.Transition.Identity.Validate(); err != nil {
		return err
	}
	key, err := fact.Transition.Identity.Key()
	if err != nil || key != fact.AttemptKey {
		return ErrAttemptAuthorityConflict
	}
	prior, exists := in.attempts[key]
	if fact.Transition.Kind == AttemptTransitionOpened {
		if exists || fact.Revision != 1 || fact.PreviousDigest != "" {
			return ErrAttemptAuthorityOrder
		}
	} else if !exists || prior.Identity != fact.Transition.Identity || fact.Revision != prior.Revision+1 || fact.PreviousDigest != prior.HeadDigest {
		return ErrAttemptAuthorityOrder
	}
	prepared := fact
	if err := prepareAttemptFact(prior, exists, &prepared); err != nil {
		return err
	}
	if prepared.AdmissionClosed != fact.AdmissionClosed || prepared.TerminalGeneration != fact.TerminalGeneration || prepared.CleanupBindingDigest != fact.CleanupBindingDigest || prepared.Transition.AdmissionFactDigest != fact.Transition.AdmissionFactDigest || prepared.Transition.AdmissionSequence != fact.Transition.AdmissionSequence {
		return ErrAttemptAuthorityConflict
	}
	state := prior
	state.Identity = fact.Transition.Identity
	state.Revision = fact.Revision
	state.HeadDigest = fact.Digest
	t := fact.Transition
	switch t.Kind {
	case AttemptTransitionOpened:
		state.OpenedDigest = fact.Digest
		state.LaunchState = LaunchNotAuthorized
	case AttemptTransitionLaunchAuthorized:
		state.LaunchState = LaunchUncertain
		state.LaunchAuthorizationID = t.LaunchAuthorizationID
		state.LaunchAuthorizedDigest = fact.Digest
		stored, err := t.LaunchClosure.Stored()
		if err != nil {
			return ErrAttemptAuthorityConflict
		}
		state.LaunchClosure = stored
		state.LaunchMaterialsDigest = t.LaunchClosure.LaunchMaterialsDigest
		state.AgentLaunchSpecDigest = t.LaunchClosure.AgentLaunchSpecDigest
	case AttemptTransitionProcessStarted:
		state.LaunchState = LaunchStarted
		state.CommandID, state.ObservedAt, state.Process = t.CommandID, t.ObservedAt, t.Process
		state.ProcessStartedDigest = fact.Digest
	case attemptTransitionResultAdmitted:
		state.CommittedResultFactDigest = t.AdmissionFactDigest
		state.CommittedResultSequence = t.AdmissionSequence
	case AttemptTransitionTerminalizationBarrier:
		if fact.TerminalGeneration != t.Identity.DispatchGeneration+1 || fact.CleanupBindingDigest == "" || !fact.AdmissionClosed || t.AdmissionFactDigest != state.CommittedResultFactDigest || t.AdmissionSequence != state.CommittedResultSequence {
			return ErrAttemptAuthorityConflict
		}
		state.BarrierDigest, state.TerminalizationID, state.EligibilityTerminal = fact.Digest, t.TerminalizationID, t.EligibilityTerminal
		state.AdmissionClosed = fact.AdmissionClosed
		state.BarrierAdmissionFactDigest, state.BarrierAdmissionSequence = t.AdmissionFactDigest, t.AdmissionSequence
		state.TerminalGeneration, state.CleanupBindingDigest = fact.TerminalGeneration, fact.CleanupBindingDigest
	case AttemptTransitionProcessTerminal:
		if t.TerminalizationID != state.TerminalizationID {
			return ErrAttemptAuthorityConflict
		}
		state.ProcessTerminalDigest, state.ProcessTerminalKind, state.ProcessTerminalObservation = fact.Digest, t.ProcessTerminalKind, t.ObservationDigest
	case AttemptTransitionAllocationTerminated:
		if t.TerminalizationID != state.TerminalizationID {
			return ErrAttemptAuthorityConflict
		}
		state.AllocationTerminalDigest, state.AllocationReceiptDigest = fact.Digest, t.ReceiptDigest
	case AttemptTransitionCleanupCompleted:
		if t.TerminalizationID != state.TerminalizationID {
			return ErrAttemptAuthorityConflict
		}
		state.CleanupCompletedDigest = fact.Digest
	case AttemptTransitionCleanupReleased:
		if t.TerminalizationID != state.TerminalizationID {
			return ErrAttemptAuthorityConflict
		}
		state.CleanupReleasedDigest = fact.Digest
	default:
		return ErrAttemptAuthorityConflict
	}
	in.attempts[key] = state
	return nil
}

func canonicalDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	canonicalBytes, err := canonical.JSON(raw)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalBytes), nil
}
