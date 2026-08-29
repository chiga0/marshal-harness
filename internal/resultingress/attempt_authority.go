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
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

// attemptAuthorityProtocolRevision is deliberately separate from the legacy
// ResultIngress revision. New attempts use one physical append-only log and a
// per-attempt revision/head chain; legacy result facts remain replay-only.
const attemptAuthorityProtocolRevision = "attempt-authority/v1"

const (
	AttemptTransitionOpened                   AttemptTransitionKind = "attempt-opened"
	AttemptTransitionControlOwnerBound        AttemptTransitionKind = "control-owner-bound"
	AttemptTransitionLaunchAuthorized         AttemptTransitionKind = "launch-authorized"
	AttemptTransitionSupervisorBootstrap      AttemptTransitionKind = "process-supervisor-bootstrap-prepared"
	AttemptTransitionProcessSupervisorStarted AttemptTransitionKind = "process-supervisor-started"
	AttemptTransitionProcessStarted           AttemptTransitionKind = "process-started"
	attemptTransitionResultAdmitted           AttemptTransitionKind = "result-admitted"
	AttemptTransitionTerminalizationBarrier   AttemptTransitionKind = "terminalization-barrier"
	AttemptTransitionProcessTerminal          AttemptTransitionKind = "process-terminal"
	AttemptTransitionAllocationTerminated     AttemptTransitionKind = "allocation-terminated"
	AttemptTransitionProcessSupervisorClosed  AttemptTransitionKind = "process-supervisor-closed"
	AttemptTransitionCleanupCompleted         AttemptTransitionKind = "cleanup-completed"
	AttemptTransitionCleanupReleased          AttemptTransitionKind = "cleanup-released"
	AttemptTransitionSupervisorIntervention   AttemptTransitionKind = "process-supervisor-intervention-required"
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
	Kind                            AttemptTransitionKind       `json:"kind"`
	Identity                        AttemptIdentity             `json:"identity"`
	LaunchAuthorizationID           string                      `json:"launchAuthorizationId,omitempty"`
	CommandID                       string                      `json:"commandId,omitempty"`
	ObservedAt                      string                      `json:"observedAt,omitempty"`
	Process                         ProcessObservation          `json:"process,omitempty"`
	TerminalizationID               string                      `json:"terminalizationId,omitempty"`
	EligibilityTerminal             EligibilityTerminal         `json:"eligibilityTerminal,omitempty"`
	ProcessTerminalKind             ProcessTerminalKind         `json:"processTerminalKind,omitempty"`
	ObservationDigest               string                      `json:"terminalObservationDigest,omitempty"`
	ReceiptDigest                   string                      `json:"receiptDigest,omitempty"`
	AdmissionFactDigest             string                      `json:"admissionFactDigest,omitempty"`
	AdmissionSequence               uint64                      `json:"admissionSequence,omitempty"`
	LaunchClosure                   launchidentity.ClosureV1    `json:"launchClosure,omitempty"`
	LaunchMaterialsDigest           string                      `json:"launchMaterialsDigest,omitempty"`
	AgentLaunchSpecDigest           string                      `json:"agentLaunchSpecDigest,omitempty"`
	Owner                           CurrentOwnerBinding         `json:"owner,omitempty,omitzero"`
	SupervisorBootstrap             SupervisorBootstrapPrepared `json:"supervisorBootstrap,omitempty,omitzero"`
	SupervisorStarted               ProcessSupervisorStarted    `json:"supervisorStarted,omitempty,omitzero"`
	SupervisorBindOutcomeFactDigest string                      `json:"supervisorBindOutcomeFactDigest,omitempty"`
	SupervisorOutcomeFactDigest     string                      `json:"supervisorOutcomeFactDigest,omitempty"`
	SupervisorBindEvidence          SupervisorCommandEvidence   `json:"supervisorBindEvidence,omitempty,omitzero"`
	SupervisorPrecedingEvidence     []SupervisorCommandEvidence `json:"supervisorPrecedingEvidence,omitempty"`
	SupervisorEvidence              SupervisorCommandEvidence   `json:"supervisorEvidence,omitempty,omitzero"`
	SupervisorClosed                ProcessSupervisorClosed     `json:"supervisorClosed,omitempty,omitzero"`
	SupervisorIntervention          SupervisorIntervention      `json:"supervisorIntervention,omitempty,omitzero"`
	SupervisorClosedFactDigest      string                      `json:"supervisorClosedFactDigest,omitempty"`
}

type AttemptAuthorityState struct {
	ProtocolRevision                 string                        `json:"protocolRevision"`
	OpenedSchemaRevision             string                        `json:"openedSchemaRevision,omitempty"`
	ReservationFactDigest            string                        `json:"reservationFactDigest,omitempty"`
	AttemptOrdinal                   uint64                        `json:"attemptOrdinal,omitempty"`
	Identity                         AttemptIdentity               `json:"identity"`
	Revision                         uint64                        `json:"revision"`
	HeadDigest                       string                        `json:"headDigest"`
	OpenedDigest                     string                        `json:"openedDigest"`
	Owner                            CurrentOwnerBinding           `json:"owner,omitempty,omitzero"`
	ControlOwnerBindingDigest        string                        `json:"controlOwnerBindingDigest,omitempty"`
	LaunchState                      LaunchState                   `json:"launchState"`
	LaunchAuthorizationID            string                        `json:"launchAuthorizationId,omitempty"`
	LaunchAuthorizedDigest           string                        `json:"launchAuthorizedDigest,omitempty"`
	SupervisorBootstrap              SupervisorBootstrapPrepared   `json:"supervisorBootstrap,omitempty,omitzero"`
	SupervisorBootstrapDigest        string                        `json:"supervisorBootstrapDigest,omitempty"`
	SupervisorStarted                ProcessSupervisorStarted      `json:"supervisorStarted,omitempty,omitzero"`
	SupervisorStartedDigest          string                        `json:"supervisorStartedDigest,omitempty"`
	SupervisorPendingIntent          SupervisorCommandIntent       `json:"supervisorPendingIntent,omitempty,omitzero"`
	SupervisorPendingIntentDigest    string                        `json:"supervisorPendingIntentDigest,omitempty"`
	SupervisorCommandCheckpoints     []SupervisorCommandCheckpoint `json:"supervisorCommandCheckpoints,omitempty"`
	SupervisorCommandRecoveryHead    string                        `json:"supervisorCommandRecoveryHead,omitempty"`
	SupervisorReconnect              SupervisorReconnectEvidence   `json:"supervisorReconnect,omitempty,omitzero"`
	SupervisorReconnectFactDigest    string                        `json:"supervisorReconnectFactDigest,omitempty"`
	SupervisorMechanicsAnchor        SupervisorMechanicsAnchor     `json:"supervisorMechanicsAnchor,omitempty,omitzero"`
	SupervisorMechanicsAuthorityHead string                        `json:"supervisorMechanicsAuthorityHead,omitempty"`
	SupervisorBoundAuthorityHead     string                        `json:"supervisorBoundAuthorityHead,omitempty"`
	SupervisorBindEvidence           SupervisorCommandEvidence     `json:"supervisorBindEvidence,omitempty,omitzero"`
	SupervisorCommandSequence        uint64                        `json:"supervisorCommandSequence,omitempty"`
	SupervisorCommandHead            string                        `json:"supervisorCommandHead,omitempty"`
	SupervisorCommandIDs             []string                      `json:"supervisorCommandIds,omitempty"`
	ProcessStartedPreceding          []SupervisorCommandEvidence   `json:"processStartedPreceding,omitempty"`
	CommandID                        string                        `json:"commandId,omitempty"`
	ObservedAt                       string                        `json:"observedAt,omitempty"`
	Process                          ProcessObservation            `json:"process,omitempty"`
	ProcessStartedEvidence           SupervisorCommandEvidence     `json:"processStartedEvidence,omitempty,omitzero"`
	ProcessStartedBindOutcomeDigest  string                        `json:"processStartedBindOutcomeDigest,omitempty"`
	ProcessStartedOutcomeDigest      string                        `json:"processStartedOutcomeDigest,omitempty"`
	ProcessStartedDigest             string                        `json:"processStartedDigest,omitempty"`
	CommittedResultFactDigest        string                        `json:"committedResultFactDigest,omitempty"`
	CommittedResultSequence          uint64                        `json:"committedResultSequence,omitempty"`
	CommittedResultPreceding         []SupervisorCommandEvidence   `json:"committedResultPreceding,omitempty"`
	CommittedResultCollect           SupervisorCommandEvidence     `json:"committedResultCollect,omitempty,omitzero"`
	CommittedResultOutcomeDigest     string                        `json:"committedResultOutcomeDigest,omitempty"`
	BarrierDigest                    string                        `json:"barrierDigest,omitempty"`
	TerminalizationID                string                        `json:"terminalizationId,omitempty"`
	EligibilityTerminal              EligibilityTerminal           `json:"eligibilityTerminal,omitempty"`
	AdmissionClosed                  bool                          `json:"admissionClosed"`
	BarrierAdmissionFactDigest       string                        `json:"barrierAdmissionFactDigest,omitempty"`
	BarrierAdmissionSequence         uint64                        `json:"barrierAdmissionSequence,omitempty"`
	TerminalGeneration               int64                         `json:"terminalGeneration,omitempty"`
	CleanupBindingDigest             string                        `json:"cleanupBindingDigest,omitempty"`
	ProcessTerminalDigest            string                        `json:"processTerminalDigest,omitempty"`
	ProcessTerminalKind              ProcessTerminalKind           `json:"processTerminalKind,omitempty"`
	ProcessTerminalObservation       string                        `json:"processTerminalObservationDigest,omitempty"`
	ProcessTerminalPreceding         []SupervisorCommandEvidence   `json:"processTerminalPreceding,omitempty"`
	ProcessTerminalEvidence          SupervisorCommandEvidence     `json:"processTerminalEvidence,omitempty,omitzero"`
	ProcessTerminalOutcomeDigest     string                        `json:"processTerminalOutcomeDigest,omitempty"`
	AllocationTerminalDigest         string                        `json:"allocationTerminalDigest,omitempty"`
	AllocationReceiptDigest          string                        `json:"allocationReceiptDigest,omitempty"`
	SupervisorClosed                 ProcessSupervisorClosed       `json:"supervisorClosed,omitempty,omitzero"`
	SupervisorClosedPreceding        []SupervisorCommandEvidence   `json:"supervisorClosedPreceding,omitempty"`
	SupervisorClosedDigest           string                        `json:"supervisorClosedDigest,omitempty"`
	SupervisorClosedOutcomeDigest    string                        `json:"supervisorClosedOutcomeDigest,omitempty"`
	CleanupCompletedDigest           string                        `json:"cleanupCompletedDigest,omitempty"`
	CleanupReleasedDigest            string                        `json:"cleanupReleasedDigest,omitempty"`
	SupervisorIntervention           SupervisorIntervention        `json:"supervisorIntervention,omitempty,omitzero"`
	SupervisorInterventionDigest     string                        `json:"supervisorInterventionDigest,omitempty"`
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
	ProtocolRevision      string            `json:"protocolRevision"`
	SchemaRevision        string            `json:"schemaRevision,omitempty"`
	FactType              string            `json:"factType"`
	Sequence              int64             `json:"sequence"`
	AttemptKey            string            `json:"attemptKey"`
	Revision              uint64            `json:"revision"`
	PreviousDigest        string            `json:"previousDigest,omitempty"`
	Transition            AttemptTransition `json:"transition"`
	ReservationFactDigest string            `json:"reservationFactDigest,omitempty"`
	AttemptOrdinal        uint64            `json:"attemptOrdinal,omitempty"`
	AdmissionClosed       bool              `json:"admissionClosed,omitempty"`
	TerminalGeneration    int64             `json:"terminalGeneration,omitempty"`
	CleanupBindingDigest  string            `json:"cleanupBindingDigest,omitempty"`
	Digest                string            `json:"digest"`
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
	if transition.Kind == AttemptTransitionControlOwnerBound || transition.Kind == AttemptTransitionSupervisorBootstrap || transition.Kind == AttemptTransitionProcessSupervisorStarted || transition.Kind == AttemptTransitionSupervisorIntervention {
		return AttemptAppendResult{}, fmt.Errorf("%w: %s requires current control owner authority", ErrControlOwnerNotCurrent, transition.Kind)
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
	case AttemptTransitionProcessTerminal, AttemptTransitionAllocationTerminated, AttemptTransitionProcessSupervisorClosed, AttemptTransitionCleanupCompleted, AttemptTransitionCleanupReleased:
		return true
	default:
		return false
	}
}

func (s *ingressDurableStore) compareAndAppend(expectedRevision uint64, expectedHead string, transition AttemptTransition, internalAdmission bool) (AttemptAppendResult, error) {
	return s.compareAndAppendWithOwner(expectedRevision, expectedHead, transition, internalAdmission, false)
}

func (s *ingressDurableStore) compareAndAppendWithOwner(expectedRevision uint64, expectedHead string, transition AttemptTransition, internalAdmission, currentOwnerHeld bool) (AttemptAppendResult, error) {
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
		if transition.Kind == AttemptTransitionProcessStarted && exists && prior.ControlOwnerBindingDigest != "" && !currentOwnerHeld {
			return ErrControlOwnerNotCurrent
		}
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
		if err := validateSupervisorTransitionAgainstProjection(projection, prior, exists, transition, false); err != nil {
			return err
		}
		if transition.Kind == attemptTransitionResultAdmitted && !internalAdmission {
			return ErrAttemptAuthorityConflict
		}
		protocolRevision := attemptAuthorityProtocolRevision
		if exists && prior.ProtocolRevision == attemptAuthorityProtocolV2 {
			protocolRevision = attemptAuthorityProtocolV2
		}
		fact := &attemptAuthorityFact{
			ProtocolRevision: protocolRevision,
			FactType:         string(transition.Kind),
			Sequence:         s.nextSequence,
			AttemptKey:       key,
			Revision:         expectedRevision + 1,
			PreviousDigest:   expectedHead,
			Transition:       transition,
		}
		if err := prepareAttemptFact(prior, exists, fact, false); err != nil {
			return err
		}
		if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
			return err
		}
		s.nextSequence++
		if err := applyAttemptAuthorityFactValue(*fact, projection, false); err != nil {
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
	case AttemptTransitionControlOwnerBound:
		return state.ControlOwnerBindingDigest
	case AttemptTransitionLaunchAuthorized:
		return state.LaunchAuthorizedDigest
	case AttemptTransitionSupervisorBootstrap:
		return state.SupervisorBootstrapDigest
	case AttemptTransitionProcessSupervisorStarted:
		return state.SupervisorStartedDigest
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
	case AttemptTransitionProcessSupervisorClosed:
		return state.SupervisorClosedDigest
	case AttemptTransitionCleanupCompleted:
		return state.CleanupCompletedDigest
	case AttemptTransitionCleanupReleased:
		return state.CleanupReleasedDigest
	case AttemptTransitionSupervisorIntervention:
		return state.SupervisorInterventionDigest
	default:
		return ""
	}
}

func prepareAttemptFact(prior AttemptAuthorityState, exists bool, fact *attemptAuthorityFact, historicalReplay bool) error {
	t := fact.Transition
	if err := validateTransitionShape(t); err != nil {
		return err
	}
	if exists && (prior.PendingEffectIntentFactDigest != "" || prior.EffectInterventionDigest != "" || prior.SupervisorInterventionDigest != "") {
		// An admitted effect owns the Attempt head until a matching receipt and
		// reconcile decision close it. Exact transition replays are handled before
		// this function and remain read-only.
		return ErrAttemptAuthorityOrder
	}
	if exists && prior.SupervisorPendingIntentDigest != "" && t.Kind != AttemptTransitionControlOwnerBound && t.Kind != AttemptTransitionSupervisorIntervention {
		return ErrAttemptAuthorityOrder
	}
	switch t.Kind {
	case AttemptTransitionOpened:
		return nil
	case AttemptTransitionControlOwnerBound:
		if prior.CleanupReleasedDigest != "" || t.Owner.OwnerEpoch <= prior.Owner.OwnerEpoch {
			return ErrAttemptAuthorityOrder
		}
	case AttemptTransitionLaunchAuthorized:
		if prior.LaunchState != LaunchNotAuthorized || prior.BarrierDigest != "" || prior.AllocationProvisionEffectDigest == "" || prior.AllocationProvisionReceiptDigest == "" {
			return ErrAttemptAuthorityOrder
		}
	case AttemptTransitionSupervisorBootstrap:
		if prior.LaunchState != LaunchUncertain || prior.BarrierDigest != "" || prior.SupervisorBootstrapDigest != "" || prior.SupervisorStartedDigest != "" || prior.ControlOwnerBindingDigest == "" || t.SupervisorBootstrap.Owner != prior.Owner || t.SupervisorBootstrap.LaunchAuthorizedFactDigest != prior.LaunchAuthorizedDigest {
			return ErrAttemptAuthorityOrder
		}
	case AttemptTransitionProcessStarted:
		if prior.LaunchState != LaunchUncertain || prior.BarrierDigest != "" {
			return ErrAttemptAuthorityOrder
		}
		newSupervisorBinding := t.SupervisorBindOutcomeFactDigest != "" || t.SupervisorOutcomeFactDigest != ""
		if !historicalReplay && (!newSupervisorBinding || prior.SupervisorBootstrapDigest == "" || prior.SupervisorStartedDigest == "") {
			return ErrAttemptAuthorityOrder
		}
		if newSupervisorBinding && (validateProcessStartedOutcomeReferences(prior, t) != nil || !zeroSupervisorCommandEvidence(t.SupervisorBindEvidence) || len(t.SupervisorPrecedingEvidence) != 0 || !zeroSupervisorCommandEvidence(t.SupervisorEvidence)) {
			return ErrAttemptAuthorityOrder
		}
		if t.LaunchMaterialsDigest != prior.LaunchMaterialsDigest || t.AgentLaunchSpecDigest != prior.AgentLaunchSpecDigest || !processMatchesRuntime(t.Process, prior.LaunchClosure.RuntimeExecutable) {
			return ErrAttemptAuthorityOrder
		}
		if historicalReplay && !newSupervisorBinding && prior.SupervisorBootstrapDigest != "" && (validateProcessStartedCommandChain(prior, t) != nil || t.SupervisorEvidence.Outcome.State != SupervisorProcessExecStopped || !commandEvidenceMatchesProcess(t.SupervisorEvidence, t.Process)) {
			return ErrAttemptAuthorityOrder
		}
	case AttemptTransitionProcessSupervisorStarted:
		if prior.LaunchState != LaunchUncertain || prior.BarrierDigest != "" || prior.SupervisorStartedDigest != "" || prior.ControlOwnerBindingDigest == "" || t.SupervisorStarted.Owner != prior.Owner || t.SupervisorStarted.LaunchAuthorizedFactDigest != prior.LaunchAuthorizedDigest || prior.SupervisorBootstrapDigest != "" && t.SupervisorStarted.BootstrapPreparedFactDigest != prior.SupervisorBootstrapDigest {
			return ErrAttemptAuthorityOrder
		}
		if !historicalReplay && prior.SupervisorBootstrapDigest == "" {
			return ErrAttemptAuthorityOrder
		}
	case attemptTransitionResultAdmitted:
		if prior.ProcessStartedDigest == "" || prior.BarrierDigest != "" {
			return ErrAttemptAuthorityOrder
		}
		newSupervisorBinding := t.SupervisorOutcomeFactDigest != ""
		if !historicalReplay && (!newSupervisorBinding || prior.SupervisorBootstrapDigest == "") {
			return ErrAttemptAuthorityOrder
		}
		if newSupervisorBinding && (validateBusinessOutcomeReference(prior, t.SupervisorOutcomeFactDigest, processsupervisor.CommandCollect, SupervisorTranscriptCollected) != nil || !zeroSupervisorCommandEvidence(t.SupervisorEvidence) || len(t.SupervisorPrecedingEvidence) != 0) {
			return ErrAttemptAuthorityOrder
		}
		if historicalReplay && !newSupervisorBinding && prior.SupervisorBootstrapDigest != "" && (t.SupervisorEvidence.Command != processsupervisor.CommandCollect || t.SupervisorEvidence.Outcome.State != SupervisorTranscriptCollected || !sameSupervisorChildEvidence(prior.ProcessStartedEvidence, t.SupervisorEvidence)) {
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
		if prior.LaunchState == LaunchUncertain && t.ProcessTerminalKind != ProcessAbsent && t.ProcessTerminalKind != ProcessTerminated && t.ProcessTerminalKind != ProcessIdentityConflict {
			return ErrAttemptAuthorityOrder
		}
		if prior.LaunchState == LaunchNotAuthorized && t.ProcessTerminalKind != ProcessAbsent {
			return ErrAttemptAuthorityOrder
		}
		newSupervisorBinding := t.SupervisorOutcomeFactDigest != ""
		if !historicalReplay && prior.SupervisorBootstrapDigest != "" && !newSupervisorBinding {
			return ErrAttemptAuthorityOrder
		}
		if newSupervisorBinding && (validateBusinessOutcomeReference(prior, t.SupervisorOutcomeFactDigest, "", "") != nil || !zeroSupervisorCommandEvidence(t.SupervisorEvidence) || len(t.SupervisorPrecedingEvidence) != 0 || !terminalCheckpointMatches(prior, t)) {
			return ErrAttemptAuthorityOrder
		}
		if historicalReplay && !newSupervisorBinding && prior.SupervisorBootstrapDigest != "" && !terminalEvidenceMatches(prior, t) {
			return ErrAttemptAuthorityOrder
		}
	case AttemptTransitionAllocationTerminated:
		if prior.ProcessTerminalKind != ProcessAbsent && prior.ProcessTerminalKind != ProcessTerminated || prior.AllocationTerminalDigest != "" || prior.AllocationTerminateEffectDigest == "" || prior.AllocationTerminateReceiptDigest == "" || t.ReceiptDigest != prior.AllocationTerminateReceiptDigest {
			return ErrAttemptAuthorityOrder
		}
	case AttemptTransitionProcessSupervisorClosed:
		closed := t.SupervisorClosed
		if prior.ProcessTerminalDigest == "" || prior.AllocationTerminalDigest == "" || prior.SupervisorStartedDigest == "" || prior.SupervisorClosedDigest != "" || closed.SupervisorStartedFactDigest != prior.SupervisorStartedDigest || closed.ProcessTerminalFactDigest != prior.ProcessTerminalDigest || closed.AllocationTerminatedFactDigest != prior.AllocationTerminalDigest || closed.CleanupBindingDigest != prior.CleanupBindingDigest || closed.TerminalizationID != prior.TerminalizationID || closed.SessionID != prior.SupervisorStarted.Handshake.SessionID || closed.SupervisorProcess != prior.SupervisorStarted.Handshake.SupervisorProcess || closed.Owner != prior.Owner {
			return ErrAttemptAuthorityOrder
		}
		newSupervisorBinding := t.SupervisorOutcomeFactDigest != ""
		if !historicalReplay && (prior.SupervisorBootstrapDigest == "" || !newSupervisorBinding) {
			return ErrAttemptAuthorityOrder
		}
		if newSupervisorBinding && closed.SupervisorAbsence == (SupervisorAbsenceObservation{}) && closed.AuthenticatedSupervisorAbsence == (processsupervisor.SupervisorAbsenceEvidence{}) {
			return ErrAttemptAuthorityOrder
		}
		if newSupervisorBinding && (validateBusinessOutcomeReference(prior, t.SupervisorOutcomeFactDigest, processsupervisor.CommandClose, SupervisorSessionClosed) != nil || !zeroSupervisorCommandEvidence(closed.Mechanics) || len(t.SupervisorPrecedingEvidence) != 0 || !closedCheckpointMatches(prior, t)) {
			return ErrAttemptAuthorityOrder
		}
		if historicalReplay && !newSupervisorBinding && prior.SupervisorBootstrapDigest != "" && (validateSupervisorCommandChain(prior, appendSupervisorEvidence(t.SupervisorPrecedingEvidence, closed.Mechanics), prior.HeadDigest) != nil || !terminalReportsEquivalent(prior.ProcessTerminalEvidence, closed.Mechanics)) {
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
	case AttemptTransitionSupervisorIntervention:
		if prior.SupervisorBootstrapDigest == "" || prior.CleanupReleasedDigest != "" || prior.SupervisorInterventionDigest != "" || t.SupervisorIntervention.Owner != prior.Owner || t.SupervisorIntervention.SessionID != prior.SupervisorBootstrap.SessionID {
			return ErrAttemptAuthorityOrder
		}
		if prior.SupervisorPendingIntentDigest != "" {
			intent := prior.SupervisorPendingIntent
			pending := t.SupervisorIntervention.Pending
			if t.SupervisorIntervention.Reason != SupervisorInterventionCommandUnresolved || pending.SessionID != intent.SessionID || pending.Command != intent.Command || pending.CommandID != intent.CommandID || pending.Sequence != intent.Sequence || pending.PreviousCommandHead != intent.PreviousCommandHead || pending.CurrentAuthorityHead != intent.CurrentAuthorityHead || pending.RequestDigest != intent.RequestDigest {
				return ErrAttemptAuthorityOrder
			}
		} else if t.SupervisorIntervention.Reason == SupervisorInterventionCommandUnresolved {
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
	if t.Kind != AttemptTransitionControlOwnerBound && t.Owner != (CurrentOwnerBinding{}) || t.Kind != AttemptTransitionSupervisorBootstrap && t.SupervisorBootstrap != (SupervisorBootstrapPrepared{}) || t.Kind != AttemptTransitionProcessSupervisorStarted && t.SupervisorStarted != (ProcessSupervisorStarted{}) || t.Kind != AttemptTransitionProcessSupervisorClosed && t.SupervisorClosed != (ProcessSupervisorClosed{}) || t.Kind != AttemptTransitionSupervisorIntervention && t.SupervisorIntervention != (SupervisorIntervention{}) || t.Kind != AttemptTransitionCleanupCompleted && t.SupervisorClosedFactDigest != "" {
		return fmt.Errorf("%w: supervisor authority payload on unrelated transition", ErrAttemptAuthorityConflict)
	}
	if t.Kind != AttemptTransitionProcessStarted && t.Kind != attemptTransitionResultAdmitted && t.Kind != AttemptTransitionProcessTerminal && !zeroSupervisorCommandEvidence(t.SupervisorEvidence) {
		return fmt.Errorf("%w: supervisor command evidence on unrelated transition", ErrAttemptAuthorityConflict)
	}
	if t.Kind != AttemptTransitionProcessStarted && !zeroSupervisorCommandEvidence(t.SupervisorBindEvidence) {
		return fmt.Errorf("%w: supervisor bind evidence on unrelated transition", ErrAttemptAuthorityConflict)
	}
	if t.Kind != AttemptTransitionProcessStarted && t.SupervisorBindOutcomeFactDigest != "" || t.Kind != AttemptTransitionProcessStarted && t.Kind != attemptTransitionResultAdmitted && t.Kind != AttemptTransitionProcessTerminal && t.Kind != AttemptTransitionProcessSupervisorClosed && t.SupervisorOutcomeFactDigest != "" {
		return fmt.Errorf("%w: supervisor outcome reference on unrelated transition", ErrAttemptAuthorityConflict)
	}
	if t.Kind != AttemptTransitionProcessStarted && t.Kind != attemptTransitionResultAdmitted && t.Kind != AttemptTransitionProcessTerminal && t.Kind != AttemptTransitionProcessSupervisorClosed && len(t.SupervisorPrecedingEvidence) != 0 {
		return fmt.Errorf("%w: supervisor preceding evidence on unrelated transition", ErrAttemptAuthorityConflict)
	}
	switch t.Kind {
	case AttemptTransitionOpened:
		if transitionHasAnyPayload(t) {
			return fmt.Errorf("%w: attempt-opened carries unrelated payload", ErrAttemptAuthorityConflict)
		}
		return nil
	case AttemptTransitionControlOwnerBound:
		if t.Owner.Validate() != nil || transitionHasPayloadExceptSupervisor(t) {
			return fmt.Errorf("%w: invalid control-owner-bound transition", ErrAttemptAuthorityConflict)
		}
	case AttemptTransitionLaunchAuthorized:
		if strings.TrimSpace(t.LaunchAuthorizationID) == "" || t.LaunchClosure.Validate() != nil || t.LaunchMaterialsDigest != "" || t.AgentLaunchSpecDigest != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.TerminalizationID != "" || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 {
			return fmt.Errorf("%w: launchAuthorizationId is empty", ErrAttemptAuthorityConflict)
		}
	case AttemptTransitionSupervisorBootstrap:
		if t.SupervisorBootstrap.Validate() != nil || transitionHasPayloadExceptSupervisor(t) {
			return fmt.Errorf("%w: invalid process-supervisor-bootstrap-prepared transition", ErrAttemptAuthorityConflict)
		}
	case AttemptTransitionProcessSupervisorStarted:
		if t.SupervisorStarted.Validate() != nil || transitionHasPayloadExceptSupervisor(t) {
			return fmt.Errorf("%w: invalid process-supervisor-started transition", ErrAttemptAuthorityConflict)
		}
	case AttemptTransitionProcessStarted:
		if strings.TrimSpace(t.CommandID) == "" || t.Process.Validate() != nil || validateObservedAt(t.ObservedAt, t.Process) != nil || !validLaunchDigest(t.LaunchMaterialsDigest) || !validLaunchDigest(t.AgentLaunchSpecDigest) || !zeroLaunchClosure(t.LaunchClosure) || t.LaunchAuthorizationID != "" || t.TerminalizationID != "" || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 {
			return fmt.Errorf("%w: incomplete process-started transition", ErrAttemptAuthorityConflict)
		}
		if !zeroSupervisorCommandEvidence(t.SupervisorBindEvidence) && (t.SupervisorBindEvidence.Validate() != nil || t.SupervisorBindEvidence.Command != processsupervisor.CommandBindAuthority || t.SupervisorBindEvidence.Disposition != "ok") {
			return fmt.Errorf("%w: invalid process-started bind evidence", ErrAttemptAuthorityConflict)
		}
		if validateSupervisorPrecedingEvidence(t.SupervisorPrecedingEvidence, processsupervisor.CommandSpawn, false) != nil || !zeroSupervisorCommandEvidence(t.SupervisorEvidence) && (t.SupervisorEvidence.Validate() != nil || t.SupervisorEvidence.Command != processsupervisor.CommandSpawn || t.SupervisorEvidence.Disposition != "ok" || t.SupervisorEvidence.Outcome.State != SupervisorProcessExecStopped || !commandEvidenceMatchesProcess(t.SupervisorEvidence, t.Process)) {
			return fmt.Errorf("%w: invalid process-started supervisor evidence", ErrAttemptAuthorityConflict)
		}
	case attemptTransitionResultAdmitted:
		if err := requireDigest("admissionFactDigest", t.AdmissionFactDigest); err != nil || t.AdmissionSequence == 0 || t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.TerminalizationID != "" || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" {
			return fmt.Errorf("%w: invalid result admission binding", ErrAttemptAuthorityConflict)
		}
		if validateSupervisorPrecedingEvidence(t.SupervisorPrecedingEvidence, processsupervisor.CommandCollect, false) != nil || !zeroSupervisorCommandEvidence(t.SupervisorEvidence) && (t.SupervisorEvidence.Validate() != nil || t.SupervisorEvidence.Command != processsupervisor.CommandCollect || t.SupervisorEvidence.Disposition != "ok" || t.SupervisorEvidence.Outcome.State != SupervisorTranscriptCollected) {
			return fmt.Errorf("%w: invalid result collect evidence", ErrAttemptAuthorityConflict)
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
		if validateSupervisorPrecedingEvidence(t.SupervisorPrecedingEvidence, "", true) != nil || !zeroSupervisorCommandEvidence(t.SupervisorEvidence) && (t.SupervisorEvidence.Validate() != nil || t.SupervisorEvidence.Disposition != "ok" || t.SupervisorEvidence.Command != processsupervisor.CommandInspect && t.SupervisorEvidence.Command != processsupervisor.CommandTerminate) {
			return fmt.Errorf("%w: invalid process-terminal supervisor evidence", ErrAttemptAuthorityConflict)
		}
	case AttemptTransitionAllocationTerminated:
		if strings.TrimSpace(t.TerminalizationID) == "" || t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 {
			return fmt.Errorf("%w: terminalizationId is empty", ErrAttemptAuthorityConflict)
		}
		if err := requireDigest("receiptDigest", t.ReceiptDigest); err != nil {
			return fmt.Errorf("%w: %v", ErrAttemptAuthorityConflict, err)
		}
	case AttemptTransitionProcessSupervisorClosed:
		if t.SupervisorClosed.Validate() != nil || t.TerminalizationID != t.SupervisorClosed.TerminalizationID || t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 {
			return fmt.Errorf("%w: invalid process-supervisor-closed transition", ErrAttemptAuthorityConflict)
		}
		if validateSupervisorPrecedingEvidence(t.SupervisorPrecedingEvidence, processsupervisor.CommandClose, false) != nil {
			return fmt.Errorf("%w: invalid supervisor-close preceding evidence", ErrAttemptAuthorityConflict)
		}
	case AttemptTransitionCleanupCompleted, AttemptTransitionCleanupReleased:
		if strings.TrimSpace(t.TerminalizationID) == "" || t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 {
			return fmt.Errorf("%w: terminalizationId is empty", ErrAttemptAuthorityConflict)
		}
		if t.Kind == AttemptTransitionCleanupCompleted && t.SupervisorClosedFactDigest != "" && requireDigest("supervisorClosedFactDigest", t.SupervisorClosedFactDigest) != nil {
			return fmt.Errorf("%w: invalid supervisorClosedFactDigest", ErrAttemptAuthorityConflict)
		}
	case AttemptTransitionSupervisorIntervention:
		if t.SupervisorIntervention.Validate() != nil || transitionHasPayloadExceptSupervisor(t) {
			return fmt.Errorf("%w: invalid process-supervisor-intervention-required transition", ErrAttemptAuthorityConflict)
		}
	default:
		return fmt.Errorf("%w: unknown transition %q", ErrAttemptAuthorityConflict, t.Kind)
	}
	return nil
}

func transitionHasAnyPayload(t AttemptTransition) bool {
	return t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.TerminalizationID != "" || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 || !zeroLaunchClosure(t.LaunchClosure) || t.LaunchMaterialsDigest != "" || t.AgentLaunchSpecDigest != "" || t.Owner != (CurrentOwnerBinding{}) || t.SupervisorBootstrap != (SupervisorBootstrapPrepared{}) || t.SupervisorStarted != (ProcessSupervisorStarted{}) || t.SupervisorBindOutcomeFactDigest != "" || t.SupervisorOutcomeFactDigest != "" || !zeroSupervisorCommandEvidence(t.SupervisorBindEvidence) || len(t.SupervisorPrecedingEvidence) != 0 || !zeroSupervisorCommandEvidence(t.SupervisorEvidence) || t.SupervisorClosed != (ProcessSupervisorClosed{}) || t.SupervisorIntervention != (SupervisorIntervention{}) || t.SupervisorClosedFactDigest != ""
}

func transitionHasPayloadExceptSupervisor(t AttemptTransition) bool {
	return t.LaunchAuthorizationID != "" || t.CommandID != "" || t.ObservedAt != "" || t.Process != (ProcessObservation{}) || t.TerminalizationID != "" || t.EligibilityTerminal != (EligibilityTerminal{}) || t.ProcessTerminalKind != "" || t.ObservationDigest != "" || t.ReceiptDigest != "" || t.AdmissionFactDigest != "" || t.AdmissionSequence != 0 || !zeroLaunchClosure(t.LaunchClosure) || t.LaunchMaterialsDigest != "" || t.AgentLaunchSpecDigest != "" || t.SupervisorBindOutcomeFactDigest != "" || t.SupervisorOutcomeFactDigest != "" || !zeroSupervisorCommandEvidence(t.SupervisorBindEvidence) || len(t.SupervisorPrecedingEvidence) != 0 || !zeroSupervisorCommandEvidence(t.SupervisorEvidence) || t.SupervisorClosedFactDigest != ""
}

func validateSupervisorPrecedingEvidence(evidence []SupervisorCommandEvidence, command processsupervisor.CommandName, allowTerminalCommands bool) error {
	for _, item := range evidence {
		if item.Validate() != nil {
			return ErrAttemptAuthorityConflict
		}
		if allowTerminalCommands {
			if item.Command != processsupervisor.CommandInspect && item.Command != processsupervisor.CommandTerminate {
				return ErrAttemptAuthorityConflict
			}
			if item.Disposition == "ok" && item.Outcome.State != SupervisorProcessRunning && item.Outcome.State != SupervisorProcessExecStopped {
				return ErrAttemptAuthorityConflict
			}
		} else if item.Command != command || item.Disposition != "rejected" {
			return ErrAttemptAuthorityConflict
		}
	}
	return nil
}

func zeroLaunchClosure(closure launchidentity.ClosureV1) bool {
	return reflect.DeepEqual(closure, launchidentity.ClosureV1{})
}

func validLaunchDigest(value string) bool { return requireDigest("launchDigest", value) == nil }

func processMatchesRuntime(process ProcessObservation, runtime launchidentity.ObjectV1) bool {
	return process.ExecutablePath == runtime.CanonicalPath && process.ExecutableDevice == runtime.Device && process.ExecutableInode == runtime.Inode && process.ExecutableSize == runtime.Size && process.ExecutableType == runtime.FileType && process.ExecutableOwner == runtime.UID && process.ExecutableGroup == runtime.GID && process.ExecutableMode == runtime.Mode && process.ExecutableLinkCount == runtime.LinkCount && process.ExecutableSHA256 == runtime.RawSHA256
}

func terminalEvidenceMatches(prior AttemptAuthorityState, transition AttemptTransition) bool {
	evidence := transition.SupervisorEvidence
	if evidence.Validate() != nil || evidence.SessionID != prior.SupervisorStarted.Handshake.SessionID || evidence.CurrentAuthorityHead != prior.HeadDigest || evidence.ObservationDigest != transition.ObservationDigest || !sameSupervisorChildEvidence(prior.ProcessStartedEvidence, evidence) || !zeroSupervisorCommandEvidence(prior.CommittedResultCollect) && !sameSupervisorChildEvidence(prior.CommittedResultCollect, evidence) {
		return false
	}
	switch transition.ProcessTerminalKind {
	case ProcessAbsent:
		return evidence.Outcome.State == SupervisorProcessAbsent
	case ProcessTerminated:
		return evidence.Outcome.State == SupervisorProcessExited
	case ProcessIdentityConflict:
		return evidence.Outcome.State == SupervisorProcessIdentityConflict
	default:
		return false
	}
}

func appendSupervisorEvidence(prefix []SupervisorCommandEvidence, final SupervisorCommandEvidence) []SupervisorCommandEvidence {
	chain := make([]SupervisorCommandEvidence, 0, len(prefix)+1)
	chain = append(chain, prefix...)
	return append(chain, final)
}

func validateProcessStartedCommandChain(prior AttemptAuthorityState, transition AttemptTransition) error {
	bind := transition.SupervisorBindEvidence
	if bind.Validate() != nil || bind.Command != processsupervisor.CommandBindAuthority || bind.SessionID != prior.SupervisorStarted.Handshake.SessionID || bind.Sequence != prior.SupervisorStarted.Handshake.CommandSequence+1 || bind.PreviousCommandHead != prior.SupervisorStarted.Handshake.CommandHead || bind.CurrentAuthorityHead != prior.SupervisorStarted.Handshake.CurrentAuthorityHead || bind.BoundAuthorityHead != prior.HeadDigest {
		return ErrAttemptAuthorityOrder
	}
	anchor := prior
	anchor.SupervisorCommandSequence, anchor.SupervisorCommandHead = bind.Sequence, bind.CommandHead
	anchor.SupervisorCommandIDs = []string{bind.CommandID}
	return validateSupervisorCommandChain(anchor, appendSupervisorEvidence(transition.SupervisorPrecedingEvidence, transition.SupervisorEvidence), prior.HeadDigest)
}

func validateSupervisorCommandChain(prior AttemptAuthorityState, chain []SupervisorCommandEvidence, authorityHead string) error {
	if len(chain) == 0 || requireDigest("supervisorCommandHead", prior.SupervisorCommandHead) != nil {
		return ErrAttemptAuthorityOrder
	}
	sequence, head := prior.SupervisorCommandSequence, prior.SupervisorCommandHead
	seen := make(map[string]struct{}, len(prior.SupervisorCommandIDs)+len(chain))
	for _, commandID := range prior.SupervisorCommandIDs {
		if !supervisorEvidenceID.MatchString(commandID) {
			return ErrAttemptAuthorityConflict
		}
		seen[commandID] = struct{}{}
	}
	for _, evidence := range chain {
		if evidence.Validate() != nil || evidence.SessionID != prior.SupervisorStarted.Handshake.SessionID || evidence.Sequence != sequence+1 || evidence.PreviousCommandHead != head || evidence.CurrentAuthorityHead != authorityHead {
			return ErrAttemptAuthorityOrder
		}
		if _, duplicate := seen[evidence.CommandID]; duplicate {
			return ErrAttemptAuthorityConflict
		}
		seen[evidence.CommandID] = struct{}{}
		sequence, head = evidence.Sequence, evidence.CommandHead
	}
	return nil
}

func sameSupervisorChildEvidence(left, right SupervisorCommandEvidence) bool {
	return left.Outcome.Process == right.Outcome.Process && left.Outcome.RuntimeObjectDigest == right.Outcome.RuntimeObjectDigest && left.Outcome.WorkingObjectDigest == right.Outcome.WorkingObjectDigest && left.Outcome.SourceGateRevision == right.Outcome.SourceGateRevision && left.Outcome.ExactSetDigest == right.Outcome.ExactSetDigest
}

func terminalReportsEquivalent(terminal, closed SupervisorCommandEvidence) bool {
	left, right := terminal.Outcome, closed.Outcome
	return left.Process == right.Process && left.MechanicsState == right.MechanicsState && left.ObserverIdentity == right.ObserverIdentity && left.ObservedAt == right.ObservedAt && left.RuntimeObjectDigest == right.RuntimeObjectDigest && left.WorkingObjectDigest == right.WorkingObjectDigest && left.SourceGateRevision == right.SourceGateRevision && left.ExactSetDigest == right.ExactSetDigest && left.ExitCode == right.ExitCode && left.Signal == right.Signal && left.StdoutDigest == right.StdoutDigest && left.StderrDigest == right.StderrDigest && left.StdoutBytes == right.StdoutBytes && left.StderrBytes == right.StderrBytes && left.TranscriptTruncated == right.TranscriptTruncated
}

func advanceSupervisorCommandState(state *AttemptAuthorityState, chain ...SupervisorCommandEvidence) {
	for _, evidence := range chain {
		state.SupervisorCommandSequence = evidence.Sequence
		state.SupervisorCommandHead = evidence.CommandHead
		state.SupervisorCommandIDs = append(state.SupervisorCommandIDs, evidence.CommandID)
	}
}

func validateSupervisorReconnectAgainstState(state AttemptAuthorityState, owner CurrentOwnerBinding, evidence SupervisorReconnectEvidence) error {
	if evidence.Validate() != nil || owner != state.Owner || evidence.Previous != state.SupervisorMechanicsAnchor || evidence.Current.OwnerEpoch != owner.OwnerEpoch || evidence.Current.CurrentAuthorityHead != state.HeadDigest || evidence.Current.Authority != supervisorAuthorityTuple(state.Identity) {
		return ErrAttemptAuthorityConflict
	}
	pending := evidence.Pending
	if state.SupervisorPendingIntentDigest == "" {
		if pending != (processsupervisor.PendingReplayEvidence{}) || evidence.Reconciliation != processsupervisor.ReconciliationUnchanged || evidence.MechanicsLocked {
			return ErrAttemptAuthorityOrder
		}
		return nil
	}
	intent := state.SupervisorPendingIntent
	if pending.ProtocolRevision != intent.ProtocolRevision || pending.SessionID != intent.SessionID || pending.Command != intent.Command || pending.CommandID != intent.CommandID || pending.Sequence != intent.Sequence || pending.PreviousCommandDigest != intent.PreviousCommandHead || pending.CurrentAuthorityHead != intent.CurrentAuthorityHead || pending.RequestDigest != intent.RequestDigest || pending.Deadline != intent.Deadline || evidence.Previous != intent.PreCommand {
		return ErrAttemptAuthorityConflict
	}
	return nil
}

func validateSupervisorCommandIntentAgainstState(state AttemptAuthorityState, intent SupervisorCommandIntent) error {
	if intent.Validate() != nil || intent.SessionID != state.SupervisorStarted.Handshake.SessionID || intent.Sequence != state.SupervisorCommandSequence+1 || intent.PreviousCommandHead != state.SupervisorCommandHead {
		return ErrAttemptAuthorityOrder
	}
	pre, prior := intent.PreCommand, state.SupervisorMechanicsAnchor
	if prior.Validate() != nil || pre != prior || pre.OwnerEpoch != state.Owner.OwnerEpoch {
		return ErrAttemptAuthorityOrder
	}
	for _, commandID := range state.SupervisorCommandIDs {
		if commandID == intent.CommandID {
			return ErrAttemptAuthorityConflict
		}
	}
	if len(state.SupervisorCommandCheckpoints) != 0 {
		latest := state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1].Evidence
		if latest.Disposition == "ok" {
			switch latest.Command {
			case processsupervisor.CommandSpawn:
				if state.ProcessStartedDigest == "" {
					return ErrAttemptAuthorityOrder
				}
			case processsupervisor.CommandCollect:
				if state.CommittedResultFactDigest == "" && state.BarrierDigest == "" {
					return ErrAttemptAuthorityOrder
				}
			case processsupervisor.CommandInspect, processsupervisor.CommandTerminate:
				// A successful Inspect can truthfully report that the process is
				// still running or exec-stopped. That checkpoint closes the
				// command, but is not a business terminal fact and must not block
				// the following Terminate. Closed terminal outcomes remain gated
				// until their exact process-terminal transition is committed.
				if latest.Outcome.State != SupervisorProcessRunning && latest.Outcome.State != SupervisorProcessExecStopped && state.ProcessTerminalDigest == "" {
					return ErrAttemptAuthorityOrder
				}
			case processsupervisor.CommandClose:
				if state.SupervisorClosedDigest == "" {
					return ErrAttemptAuthorityOrder
				}
			}
		}
	}
	rebuild := intent.Rebuild
	if intent.Command == processsupervisor.CommandBindAuthority {
		if state.SupervisorBoundAuthorityHead != "" || pre.CurrentAuthorityHead != state.SupervisorStarted.Handshake.CurrentAuthorityHead || intent.CurrentAuthorityHead != pre.CurrentAuthorityHead || rebuild.OwnerEpoch != state.Owner.OwnerEpoch || rebuild.PreviousAuthorityHead != state.SupervisorStarted.Handshake.CurrentAuthorityHead || rebuild.AuthorityHead != state.SupervisorStartedDigest || rebuild.SupervisorStartedFactDigest != state.SupervisorStartedDigest {
			return ErrAttemptAuthorityOrder
		}
		return nil
	}
	if state.SupervisorBoundAuthorityHead != state.SupervisorStartedDigest || intent.CurrentAuthorityHead != state.HeadDigest || pre.CurrentAuthorityHead != state.SupervisorMechanicsAuthorityHead {
		return ErrAttemptAuthorityOrder
	}
	switch intent.Command {
	case processsupervisor.CommandSpawn:
		if pre.CurrentAuthorityHead != intent.CurrentAuthorityHead || state.ProcessStartedDigest != "" || rebuild.SupervisorStartedFactDigest != state.SupervisorStartedDigest || rebuild.LaunchAuthorizedFactDigest != state.LaunchAuthorizedDigest || rebuild.LaunchMaterialsDigest != state.LaunchMaterialsDigest || rebuild.AgentLaunchSpecDigest != state.AgentLaunchSpecDigest {
			return ErrAttemptAuthorityOrder
		}
	case processsupervisor.CommandResume:
		if state.ProcessStartedDigest == "" || state.BarrierDigest != "" || state.ProcessTerminalDigest != "" || rebuild.ProcessStartedFactDigest != state.ProcessStartedDigest {
			return ErrAttemptAuthorityOrder
		}
	case processsupervisor.CommandCollect:
		if state.ProcessStartedDigest == "" || state.BarrierDigest != "" || state.CommittedResultFactDigest != "" || rebuild.ProcessStartedFactDigest != state.ProcessStartedDigest || rebuild.LastObservationDigest != supervisorLastObservation(state) {
			return ErrAttemptAuthorityOrder
		}
	case processsupervisor.CommandInspect, processsupervisor.CommandTerminate:
		if state.BarrierDigest == "" || state.ProcessTerminalDigest != "" || rebuild.TerminalizationBarrierDigest != state.BarrierDigest || rebuild.TerminalizationID != state.TerminalizationID || rebuild.TerminalGeneration != uint64(state.TerminalGeneration) || rebuild.CleanupBindingDigest != state.CleanupBindingDigest || rebuild.ProcessStartedFactDigest != state.ProcessStartedDigest || rebuild.LastObservationDigest != supervisorLastObservation(state) {
			return ErrAttemptAuthorityOrder
		}
	case processsupervisor.CommandClose:
		if state.ProcessTerminalDigest == "" || state.AllocationTerminalDigest == "" || state.SupervisorClosedDigest != "" || rebuild.ProcessTerminalFactDigest != state.ProcessTerminalDigest || rebuild.AllocationTerminatedFactDigest != state.AllocationTerminalDigest || rebuild.CleanupBindingDigest != state.CleanupBindingDigest {
			return ErrAttemptAuthorityOrder
		}
	default:
		return ErrAttemptAuthorityOrder
	}
	return nil
}

func validateSupervisorCommandOutcomeAgainstIntent(state AttemptAuthorityState, evidence SupervisorCommandEvidence) error {
	intent := state.SupervisorPendingIntent
	if evidence.Validate() != nil || evidence.SessionID != intent.SessionID || evidence.Command != intent.Command || evidence.CommandID != intent.CommandID || evidence.Sequence != intent.Sequence || evidence.PreviousCommandHead != intent.PreviousCommandHead || evidence.CurrentAuthorityHead != intent.CurrentAuthorityHead || evidence.RequestDigest != intent.RequestDigest || evidence.PreCommand != intent.PreCommand {
		return ErrAttemptAuthorityConflict
	}
	if state.SupervisorReconnectFactDigest != "" && state.SupervisorCommandRecoveryHead == state.SupervisorReconnectFactDigest && state.SupervisorReconnect.Pending != (processsupervisor.PendingReplayEvidence{}) {
		if state.SupervisorReconnect.MechanicsLocked || evidence.PostCommand != state.SupervisorReconnect.Current || evidence.PreCommand != state.SupervisorReconnect.Previous {
			return ErrAttemptAuthorityConflict
		}
	}
	if evidence.Disposition == "rejected" {
		return nil
	}
	switch intent.Command {
	case processsupervisor.CommandBindAuthority:
		if evidence.BoundAuthorityHead != intent.Rebuild.AuthorityHead {
			return ErrAttemptAuthorityConflict
		}
	case processsupervisor.CommandSpawn:
		if evidence.Outcome.State != SupervisorProcessExecStopped || evidence.Outcome.RuntimeObjectDigest != intent.Rebuild.RuntimeObjectDigest || evidence.Outcome.WorkingObjectDigest != intent.Rebuild.WorkingObjectDigest {
			return ErrAttemptAuthorityConflict
		}
	case processsupervisor.CommandResume:
		if evidence.Outcome.State != SupervisorProcessRunning || !sameSupervisorChildEvidence(state.ProcessStartedEvidence, evidence) {
			return ErrAttemptAuthorityConflict
		}
	case processsupervisor.CommandCollect:
		if evidence.Outcome.State != SupervisorTranscriptCollected || !sameSupervisorChildEvidence(state.ProcessStartedEvidence, evidence) {
			return ErrAttemptAuthorityConflict
		}
	case processsupervisor.CommandInspect, processsupervisor.CommandTerminate:
		if evidence.Outcome.State != SupervisorProcessRunning && evidence.Outcome.State != SupervisorProcessExecStopped && evidence.Outcome.State != SupervisorProcessExited && evidence.Outcome.State != SupervisorProcessAbsent && evidence.Outcome.State != SupervisorProcessIdentityConflict || !sameSupervisorChildEvidence(state.ProcessStartedEvidence, evidence) {
			return ErrAttemptAuthorityConflict
		}
	case processsupervisor.CommandClose:
		if evidence.Outcome.State != SupervisorSessionClosed || !sameSupervisorChildEvidence(state.ProcessStartedEvidence, evidence) {
			return ErrAttemptAuthorityConflict
		}
	default:
		return ErrAttemptAuthorityConflict
	}
	return nil
}

func supervisorLastObservation(state AttemptAuthorityState) string {
	for index := len(state.SupervisorCommandCheckpoints) - 1; index >= 0; index-- {
		evidence := state.SupervisorCommandCheckpoints[index].Evidence
		if evidence.Disposition == "ok" && evidence.Outcome != (SupervisorProcessOutcome{}) {
			return evidence.ObservationDigest
		}
	}
	return ""
}

func supervisorCheckpointEvidence(state AttemptAuthorityState, digest string) (SupervisorCommandEvidence, bool) {
	for _, checkpoint := range state.SupervisorCommandCheckpoints {
		if checkpoint.FactDigest == digest {
			return checkpoint.Evidence, true
		}
	}
	return SupervisorCommandEvidence{}, false
}

func validateBusinessOutcomeReference(state AttemptAuthorityState, digest string, command processsupervisor.CommandName, outcome SupervisorProcessState) error {
	if requireDigest("supervisorOutcomeFactDigest", digest) != nil || len(state.SupervisorCommandCheckpoints) == 0 {
		return ErrAttemptAuthorityConflict
	}
	latest := state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1]
	if latest.FactDigest != digest || latest.Evidence.Disposition != "ok" || command != "" && latest.Evidence.Command != command || outcome != "" && latest.Evidence.Outcome.State != outcome {
		return ErrAttemptAuthorityConflict
	}
	return nil
}

func validateProcessStartedOutcomeReferences(state AttemptAuthorityState, transition AttemptTransition) error {
	bind, found := supervisorCheckpointEvidence(state, transition.SupervisorBindOutcomeFactDigest)
	if !found || bind.Command != processsupervisor.CommandBindAuthority || bind.Disposition != "ok" || bind.BoundAuthorityHead != state.SupervisorStartedDigest {
		return ErrAttemptAuthorityConflict
	}
	if validateBusinessOutcomeReference(state, transition.SupervisorOutcomeFactDigest, processsupervisor.CommandSpawn, SupervisorProcessExecStopped) != nil {
		return ErrAttemptAuthorityConflict
	}
	spawn, _ := supervisorCheckpointEvidence(state, transition.SupervisorOutcomeFactDigest)
	if !commandEvidenceMatchesProcess(spawn, transition.Process) {
		return ErrAttemptAuthorityConflict
	}
	return nil
}

func terminalCheckpointMatches(state AttemptAuthorityState, transition AttemptTransition) bool {
	evidence, found := supervisorCheckpointEvidence(state, transition.SupervisorOutcomeFactDigest)
	if !found || evidence.ObservationDigest != transition.ObservationDigest || !sameSupervisorChildEvidence(state.ProcessStartedEvidence, evidence) {
		return false
	}
	switch transition.ProcessTerminalKind {
	case ProcessTerminated:
		return evidence.Outcome.State == SupervisorProcessExited
	case ProcessAbsent:
		return evidence.Outcome.State == SupervisorProcessAbsent
	case ProcessIdentityConflict:
		return evidence.Outcome.State == SupervisorProcessIdentityConflict
	default:
		return false
	}
}

func closedCheckpointMatches(state AttemptAuthorityState, transition AttemptTransition) bool {
	evidence, found := supervisorCheckpointEvidence(state, transition.SupervisorOutcomeFactDigest)
	closed := transition.SupervisorClosed
	return found && evidence.Command == processsupervisor.CommandClose && evidence.RequestDigest == closed.CloseIntentDigest && evidence.ReceiptDigest == closed.CloseReceiptDigest && evidence.ObservationDigest == closed.CloseObservationDigest && evidence.CommandHead == closed.FinalCommandHead && terminalReportsEquivalent(state.ProcessTerminalEvidence, evidence)
}

func exactSupervisorOutcomeReplay(storedDigest string, storedPreceding []SupervisorCommandEvidence, storedEvidence SupervisorCommandEvidence, transition AttemptTransition) bool {
	if storedDigest != transition.SupervisorOutcomeFactDigest {
		return false
	}
	if transition.SupervisorOutcomeFactDigest != "" {
		// Current supervisor-backed transitions carry only the exact durable
		// outcome-fact reference. Projection materializes the referenced evidence
		// into AttemptAuthorityState, so comparing that materialized value with the
		// intentionally empty transport fields would make an exact replay fail.
		// Keep the replay strict: reference-mode transport evidence must remain
		// empty and the referenced digest must match byte-for-byte.
		return len(transition.SupervisorPrecedingEvidence) == 0 && zeroSupervisorCommandEvidence(transition.SupervisorEvidence)
	}
	return reflect.DeepEqual(storedPreceding, transition.SupervisorPrecedingEvidence) && storedEvidence == transition.SupervisorEvidence
}

func exactTransitionReplay(state AttemptAuthorityState, exists bool, t AttemptTransition) (AttemptAuthorityState, bool) {
	if !exists || state.Identity != t.Identity {
		return AttemptAuthorityState{}, false
	}
	switch t.Kind {
	case AttemptTransitionOpened:
		return state, state.OpenedDigest != ""
	case AttemptTransitionControlOwnerBound:
		return state, state.ControlOwnerBindingDigest != "" && state.Owner == t.Owner
	case AttemptTransitionLaunchAuthorized:
		stored, err := t.LaunchClosure.Stored()
		return state, err == nil && state.LaunchAuthorizationID == t.LaunchAuthorizationID && state.LaunchAuthorizedDigest != "" && state.LaunchClosure == stored
	case AttemptTransitionSupervisorBootstrap:
		return state, state.SupervisorBootstrapDigest != "" && state.SupervisorBootstrap == t.SupervisorBootstrap
	case AttemptTransitionProcessSupervisorStarted:
		return state, state.SupervisorStartedDigest != "" && state.SupervisorStarted == t.SupervisorStarted
	case AttemptTransitionProcessStarted:
		return state, state.ProcessStartedDigest != "" && state.CommandID == t.CommandID && state.ObservedAt == t.ObservedAt && state.Process == t.Process && state.LaunchMaterialsDigest == t.LaunchMaterialsDigest && state.AgentLaunchSpecDigest == t.AgentLaunchSpecDigest && state.ProcessStartedBindOutcomeDigest == t.SupervisorBindOutcomeFactDigest && state.SupervisorBindEvidence == t.SupervisorBindEvidence && exactSupervisorOutcomeReplay(state.ProcessStartedOutcomeDigest, state.ProcessStartedPreceding, state.ProcessStartedEvidence, t)
	case attemptTransitionResultAdmitted:
		return state, state.CommittedResultFactDigest == t.AdmissionFactDigest && state.CommittedResultSequence == t.AdmissionSequence && exactSupervisorOutcomeReplay(state.CommittedResultOutcomeDigest, state.CommittedResultPreceding, state.CommittedResultCollect, t)
	case AttemptTransitionTerminalizationBarrier:
		return state, state.BarrierDigest != "" && state.TerminalizationID == t.TerminalizationID && state.EligibilityTerminal == t.EligibilityTerminal
	case AttemptTransitionProcessTerminal:
		return state, state.ProcessTerminalDigest != "" && state.TerminalizationID == t.TerminalizationID && state.ProcessTerminalKind == t.ProcessTerminalKind && state.ProcessTerminalObservation == t.ObservationDigest && exactSupervisorOutcomeReplay(state.ProcessTerminalOutcomeDigest, state.ProcessTerminalPreceding, state.ProcessTerminalEvidence, t)
	case AttemptTransitionAllocationTerminated:
		return state, state.AllocationTerminalDigest != "" && state.TerminalizationID == t.TerminalizationID && state.AllocationReceiptDigest == t.ReceiptDigest
	case AttemptTransitionProcessSupervisorClosed:
		return state, state.SupervisorClosedDigest != "" && state.SupervisorClosedOutcomeDigest == t.SupervisorOutcomeFactDigest && reflect.DeepEqual(state.SupervisorClosedPreceding, t.SupervisorPrecedingEvidence) && state.SupervisorClosed == t.SupervisorClosed
	case AttemptTransitionCleanupCompleted:
		return state, state.CleanupCompletedDigest != "" && state.TerminalizationID == t.TerminalizationID && (t.SupervisorClosedFactDigest == "" || t.SupervisorClosedFactDigest == state.SupervisorClosedDigest)
	case AttemptTransitionCleanupReleased:
		return state, state.CleanupReleasedDigest != "" && state.TerminalizationID == t.TerminalizationID
	case AttemptTransitionSupervisorIntervention:
		return state, state.SupervisorInterventionDigest != "" && state.SupervisorIntervention == t.SupervisorIntervention
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
	if transition.Kind == AttemptTransitionOpened {
		return AttemptAppendResult{}, fmt.Errorf("%w: fresh attempt-opened requires an active reservation", ErrAttemptAuthorityConflict)
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
	if state.PendingEffectIntentFactDigest != "" || state.EffectInterventionDigest != "" || state.SupervisorPendingIntentDigest != "" || state.SupervisorInterventionDigest != "" {
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
			// LaunchUncertain can still have an exact held child: launch-authorized
			// precedes spawn and process-started CAS. The non-bearer cleanup tuple
			// only authorizes the processcontrol handle that already owns that
			// birth/FD/wait identity; restart never reconstructs a kill handle.
			return state.LaunchState == LaunchStarted || state.LaunchState == LaunchUncertain
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
	case AttemptTransitionProcessSupervisorClosed:
		return operation == CleanupReconcile
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
	case state.SupervisorClosedDigest != "":
		return kind == AttemptTransitionCleanupCompleted || exactReplay && kind == AttemptTransitionProcessSupervisorClosed
	case state.AllocationTerminalDigest != "":
		return kind == AttemptTransitionProcessSupervisorClosed || exactReplay && kind == AttemptTransitionAllocationTerminated
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
	return s.compareAndAppendCleanup(ctx, verifier, expectedRevision, expectedHead, request, transition, false)
}

func (s *ingressDurableStore) compareAndAppendCleanup(ctx context.Context, verifier CurrentRunAuthorityVerifier, expectedRevision uint64, expectedHead string, request CleanupAuthorizationRequest, transition AttemptTransition, ownerAuthorized bool) (AttemptAppendResult, error) {
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
		if transition.Kind == AttemptTransitionProcessSupervisorClosed && !ownerAuthorized {
			return ErrControlOwnerNotCurrent
		}
		if transition.Kind == AttemptTransitionCleanupCompleted && (state.SupervisorClosedDigest == "" || transition.SupervisorClosedFactDigest != state.SupervisorClosedDigest) {
			return ErrCleanupUnauthorized
		}
		replay, exactReplay := exactTransitionReplay(state, true, transition)
		if !exactReplay && (state.PendingEffectIntentFactDigest != "" || state.EffectInterventionDigest != "" || state.SupervisorInterventionDigest != "") {
			// Pending/intervention effect authority closes every new cleanup
			// successor. Preserve read-only exact replay, but reject before the
			// lower-level transition-order checker so callers receive the cleanup
			// port's stable unauthorized result and cannot mistake it for a
			// retryable sequencing gap.
			return ErrCleanupUnauthorized
		}
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
		admitted:                    make(map[string]admittedEntry),
		attempts:                    make(map[string]AttemptAuthorityState),
		reservations:                make(map[string]AttemptReservationState),
		reservationKeys:             make(map[string]string),
		attemptsByReservation:       make(map[string]AttemptAuthorityState),
		controlOwners:               make(map[string]ControlOwnerState),
		effects:                     make(map[string]EffectAuthorityState),
		allocations:                 make(map[string]allocationAuthorityState),
		preparedExecutions:          make(map[string]PreparedExecutionV1),
		preparedExecutionKeys:       make(map[string]string),
		legacyPreparedExecutionKeys: make(map[string]string),
		existingWorktreeFacts:       nil,
		effectCommands:              make(map[string]string),
		effectIdempotency:           make(map[string]string),
		effectMarkers:               make(map[string]string),
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
	if fact.ProtocolRevision != attemptAuthorityProtocolRevision && fact.ProtocolRevision != attemptAuthorityProtocolV2 || fact.Sequence != wantSequence || fact.FactType != string(fact.Transition.Kind) {
		return ErrAttemptAuthorityConflict
	}
	if fact.ProtocolRevision == attemptAuthorityProtocolRevision {
		if fact.SchemaRevision != "" || fact.ReservationFactDigest != "" || fact.AttemptOrdinal != 0 {
			return ErrAttemptAuthorityConflict
		}
	} else if fact.Transition.Kind == AttemptTransitionOpened {
		if fact.SchemaRevision != attemptOpenedSchemaV2 || requireDigest("reservationFactDigest", fact.ReservationFactDigest) != nil || fact.AttemptOrdinal == 0 {
			return ErrAttemptAuthorityConflict
		}
	} else if fact.SchemaRevision != "" || fact.ReservationFactDigest != "" || fact.AttemptOrdinal != 0 {
		return ErrAttemptAuthorityConflict
	}
	stored := fact.Digest
	fact.Digest = ""
	digest, err := canonicalDigest(fact)
	if err != nil || stored == "" || digest != stored {
		return ErrAttemptAuthorityConflict
	}
	fact.Digest = stored
	return applyAttemptAuthorityFactValue(fact, in, true)
}

func applyAttemptAuthorityFactValue(fact attemptAuthorityFact, in *Ingress, historicalReplay bool) error {
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
	if err := validateSupervisorTransitionAgainstProjection(in, prior, exists, fact.Transition, true); err != nil {
		return err
	}
	prepared := fact
	if err := prepareAttemptFact(prior, exists, &prepared, historicalReplay); err != nil {
		return err
	}
	if prepared.AdmissionClosed != fact.AdmissionClosed || prepared.TerminalGeneration != fact.TerminalGeneration || prepared.CleanupBindingDigest != fact.CleanupBindingDigest || prepared.Transition.AdmissionFactDigest != fact.Transition.AdmissionFactDigest || prepared.Transition.AdmissionSequence != fact.Transition.AdmissionSequence {
		return ErrAttemptAuthorityConflict
	}
	state := prior
	if exists && prior.ProtocolRevision != fact.ProtocolRevision {
		return ErrAttemptAuthorityConflict
	}
	state.ProtocolRevision = fact.ProtocolRevision
	state.Identity = fact.Transition.Identity
	state.Revision = fact.Revision
	state.HeadDigest = fact.Digest
	t := fact.Transition
	switch t.Kind {
	case AttemptTransitionOpened:
		if fact.ProtocolRevision == attemptAuthorityProtocolV2 {
			reservation, ok := in.reservations[fact.ReservationFactDigest]
			ready := reservation.Reservation.Ready
			identity := fact.Transition.Identity
			if !ok || reservation.Status != AttemptReservationActive ||
				reservation.Reservation.AttemptID != identity.AttemptID || reservation.Reservation.AttemptOrdinal != fact.AttemptOrdinal ||
				ready.AuthorityNamespaceID != identity.AuthorityNamespaceID || ready.TaskID != identity.TaskID || ready.RunID != identity.RunID ||
				ready.OrchestratorID != identity.OrchestratorID || ready.ReadyAuthorityHead != identity.RunAuthorityDigest {
				return ErrAttemptAuthorityConflict
			}
			state.OpenedSchemaRevision = fact.SchemaRevision
			state.ReservationFactDigest = fact.ReservationFactDigest
			state.AttemptOrdinal = fact.AttemptOrdinal
		}
		state.OpenedDigest = fact.Digest
		state.LaunchState = LaunchNotAuthorized
	case AttemptTransitionControlOwnerBound:
		state.Owner = t.Owner
		state.ControlOwnerBindingDigest = fact.Digest
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
	case AttemptTransitionSupervisorBootstrap:
		state.SupervisorBootstrap = t.SupervisorBootstrap
		state.SupervisorBootstrapDigest = fact.Digest
	case AttemptTransitionProcessSupervisorStarted:
		state.SupervisorStarted = t.SupervisorStarted
		state.SupervisorStartedDigest = fact.Digest
		if state.SupervisorBootstrapDigest != "" {
			state.SupervisorCommandSequence = t.SupervisorStarted.Handshake.CommandSequence
			state.SupervisorCommandHead = t.SupervisorStarted.Handshake.CommandHead
			state.SupervisorCommandIDs = nil
			state.SupervisorMechanicsAuthorityHead = fact.Digest
			request := state.SupervisorBootstrap.Request
			handshake := t.SupervisorStarted.Handshake
			state.SupervisorMechanicsAnchor = SupervisorMechanicsAnchor{SessionID: handshake.SessionID, SessionNonceDigest: handshake.SessionNonceDigest, Authority: request.Authority, OwnerEpoch: handshake.OwnerEpoch, CurrentAuthorityHead: handshake.CurrentAuthorityHead, CommandSequence: handshake.CommandSequence, CommandHead: handshake.CommandHead, JournalSequence: handshake.JournalSequence, JournalHead: handshake.JournalHead, UID: request.Core.UID, GID: request.Core.GID, FixedBinary: handshake.SupervisorBinary, ControlSocket: handshake.ControlSocket, ControlFiles: handshake.ControlFiles}
		}
	case AttemptTransitionProcessStarted:
		state.LaunchState = LaunchStarted
		state.CommandID, state.ObservedAt, state.Process = t.CommandID, t.ObservedAt, t.Process
		state.ProcessStartedBindOutcomeDigest, state.ProcessStartedOutcomeDigest = t.SupervisorBindOutcomeFactDigest, t.SupervisorOutcomeFactDigest
		state.SupervisorBindEvidence = t.SupervisorBindEvidence
		state.ProcessStartedPreceding = append([]SupervisorCommandEvidence(nil), t.SupervisorPrecedingEvidence...)
		state.ProcessStartedEvidence = t.SupervisorEvidence
		if state.SupervisorBootstrapDigest != "" && t.SupervisorOutcomeFactDigest != "" {
			state.ProcessStartedEvidence, _ = supervisorCheckpointEvidence(state, t.SupervisorOutcomeFactDigest)
		} else if state.SupervisorBootstrapDigest != "" {
			advanceSupervisorCommandState(&state, t.SupervisorBindEvidence)
			advanceSupervisorCommandState(&state, t.SupervisorPrecedingEvidence...)
			advanceSupervisorCommandState(&state, t.SupervisorEvidence)
		}
		state.ProcessStartedDigest = fact.Digest
	case attemptTransitionResultAdmitted:
		state.CommittedResultFactDigest = t.AdmissionFactDigest
		state.CommittedResultSequence = t.AdmissionSequence
		state.CommittedResultOutcomeDigest = t.SupervisorOutcomeFactDigest
		state.CommittedResultPreceding = append([]SupervisorCommandEvidence(nil), t.SupervisorPrecedingEvidence...)
		state.CommittedResultCollect = t.SupervisorEvidence
		if state.SupervisorBootstrapDigest != "" && t.SupervisorOutcomeFactDigest != "" {
			state.CommittedResultCollect, _ = supervisorCheckpointEvidence(state, t.SupervisorOutcomeFactDigest)
		} else if state.SupervisorBootstrapDigest != "" {
			advanceSupervisorCommandState(&state, t.SupervisorPrecedingEvidence...)
			advanceSupervisorCommandState(&state, t.SupervisorEvidence)
		}
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
		state.ProcessTerminalOutcomeDigest = t.SupervisorOutcomeFactDigest
		state.ProcessTerminalPreceding = append([]SupervisorCommandEvidence(nil), t.SupervisorPrecedingEvidence...)
		state.ProcessTerminalEvidence = t.SupervisorEvidence
		if state.SupervisorBootstrapDigest != "" && t.SupervisorOutcomeFactDigest != "" {
			state.ProcessTerminalEvidence, _ = supervisorCheckpointEvidence(state, t.SupervisorOutcomeFactDigest)
		} else if state.SupervisorBootstrapDigest != "" {
			advanceSupervisorCommandState(&state, t.SupervisorPrecedingEvidence...)
			advanceSupervisorCommandState(&state, t.SupervisorEvidence)
		}
	case AttemptTransitionAllocationTerminated:
		if t.TerminalizationID != state.TerminalizationID {
			return ErrAttemptAuthorityConflict
		}
		state.AllocationTerminalDigest, state.AllocationReceiptDigest = fact.Digest, t.ReceiptDigest
	case AttemptTransitionProcessSupervisorClosed:
		if t.TerminalizationID != state.TerminalizationID {
			return ErrAttemptAuthorityConflict
		}
		state.SupervisorClosedPreceding = append([]SupervisorCommandEvidence(nil), t.SupervisorPrecedingEvidence...)
		state.SupervisorClosedOutcomeDigest = t.SupervisorOutcomeFactDigest
		state.SupervisorClosed, state.SupervisorClosedDigest = t.SupervisorClosed, fact.Digest
		if state.SupervisorBootstrapDigest != "" && t.SupervisorOutcomeFactDigest == "" {
			advanceSupervisorCommandState(&state, t.SupervisorPrecedingEvidence...)
			advanceSupervisorCommandState(&state, t.SupervisorClosed.Mechanics)
		}
	case AttemptTransitionCleanupCompleted:
		if t.TerminalizationID != state.TerminalizationID {
			return ErrAttemptAuthorityConflict
		}
		if state.SupervisorClosedDigest != "" && t.SupervisorClosedFactDigest != state.SupervisorClosedDigest {
			return ErrAttemptAuthorityConflict
		}
		state.CleanupCompletedDigest = fact.Digest
	case AttemptTransitionCleanupReleased:
		if t.TerminalizationID != state.TerminalizationID {
			return ErrAttemptAuthorityConflict
		}
		state.CleanupReleasedDigest = fact.Digest
	case AttemptTransitionSupervisorIntervention:
		state.SupervisorIntervention = t.SupervisorIntervention
		state.SupervisorInterventionDigest = fact.Digest
	default:
		return ErrAttemptAuthorityConflict
	}
	in.attempts[key] = state
	if state.ReservationFactDigest != "" {
		if priorAttempt, exists := in.attemptsByReservation[state.ReservationFactDigest]; exists && priorAttempt.Identity != state.Identity {
			return ErrAttemptAuthorityConflict
		}
		in.attemptsByReservation[state.ReservationFactDigest] = state
	}
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
