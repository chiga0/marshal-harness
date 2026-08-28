// Package processcontrol owns exact process launch and terminalization. It is
// deliberately authority-agnostic: production composition adapts the durable
// Attempt authority store to the narrow interface declared here.
package processcontrol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
)

var (
	ErrUnsupported      = errors.New("process control is unsupported on this platform")
	ErrIdentityConflict = errors.New("process identity conflict")
	ErrLaunchUncertain  = errors.New("process launch is uncertain")
	ErrAuthority        = errors.New("process authority rejected")
	ErrStillRunning     = errors.New("process group is still running")
	ErrClosed           = errors.New("process control handle is closed")
)

// AuthorityRef is the non-bearer tuple that a production adapter maps to the
// durable RB1 AttemptIdentity. It is intentionally not a copy of RB1's on-disk
// schema, avoiding a second persistence contract in this package.
type AuthorityRef struct {
	AuthorityNamespaceID  authority.AuthorityNamespaceId
	AuthorityNamespaceRef string
	AttemptKey            string
	TaskID                string
	RunID                 string
	AttemptID             string
	AllocationID          string
	LeaseID               string
	LeaseDigest           string
	DispatchGeneration    uint64
	FencingTokenDigest    string
	OrchestratorID        string
	RunAuthorityDigest    string
}

type AppendResult struct {
	Appended         bool
	Revision         uint64
	HeadDigest       string
	TransitionDigest string
}

type LaunchAuthorityRequest struct {
	Authority        AuthorityRef
	ExpectedRevision uint64
	ExpectedHead     string
	LaunchID         string
	Closure          launchidentity.ClosureV1
}

type ProcessStartedAuthorityRequest struct {
	Authority             AuthorityRef
	ExpectedRevision      uint64
	ExpectedHead          string
	LaunchTransition      string
	CommandID             string
	ObservedAt            string
	Observation           ProcessObservation
	LaunchMaterialsDigest string
	AgentLaunchSpecDigest string
}

type ControlOperation string

const (
	OperationInspect          ControlOperation = "inspect"
	OperationReconcile        ControlOperation = "reconcile"
	OperationCleanupInspect   ControlOperation = "cleanup-inspect"
	OperationCleanupReconcile ControlOperation = "cleanup-reconcile"
	OperationSignalTERM       ControlOperation = "signal-term"
	OperationSignalKILL       ControlOperation = "signal-kill"
)

// CleanupRef is non-bearer evidence from the durable terminalization barrier.
// Signal operations are forbidden without this exact tuple.
type CleanupRef struct {
	TerminalizationID    string
	TerminalGeneration   int64
	CleanupBindingDigest string
}

func (ref CleanupRef) validate() error {
	if strings.TrimSpace(ref.TerminalizationID) == "" || ref.TerminalGeneration < 1 || !validSHA256(ref.CleanupBindingDigest) {
		return ErrAuthority
	}
	return nil
}

type ControlAuthorization struct {
	Authority         AuthorityRef
	Operation         ControlOperation
	ObservationDigest string
	Cleanup           CleanupRef
}

// AttemptAuthority is the sole authority seam. AuthorizeLaunch and
// RecordProcessStarted must delegate to RB1 CompareAndAppend. Only a fresh
// Appended=true launch authorization permits a process spawn.
type AttemptAuthority interface {
	AuthorizeLaunch(context.Context, LaunchAuthorityRequest) (AppendResult, error)
	RecordProcessStarted(context.Context, ProcessStartedAuthorityRequest) (AppendResult, error)
	WithCurrentAuthority(context.Context, ControlAuthorization, func() error) error
}

type ObjectObservation struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Size   int64  `json:"size"`
	Nlink  uint64 `json:"nlink"`
	SHA256 string `json:"sha256,omitempty"`
}

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

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

type LaunchRequest struct {
	Authority                AuthorityRef
	ExpectedRevision         uint64
	ExpectedHead             string
	LaunchID                 string
	CommandID                string
	Arguments                []string
	Environment              []string
	WorkingDirectory         string
	ExecutablePath           string
	ExpectedExecutableSHA256 string
	Closure                  launchidentity.ClosureV1
	// Materials is the rejected pre-ADR0058 partial-manifest seam.
	Materials []LaunchMaterial
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
}

type LaunchMaterial struct {
	Role           string
	CanonicalPath  string
	ExpectedSHA256 string
}

type ProcessState string

const (
	ProcessLive             ProcessState = "live"
	ProcessAbsent           ProcessState = "absent"
	ProcessIdentityConflict ProcessState = "identity-conflict"
	ProcessLaunchUncertain  ProcessState = "launch-uncertain"
)

type Inspection struct {
	State       ProcessState
	Observation ProcessObservation
	// ExitKnown is true only for the live coordinator that retained the exact
	// leader wait right. Restart reconciliation deliberately cannot invent an
	// exit disposition.
	ExitKnown bool
	ExitCode  int
	Signal    string
}

type platformCoordinator interface {
	launch(context.Context, LaunchRequest) (platformProcess, error)
	reconcile(context.Context, AuthorityRef, ProcessObservation, CleanupRef) (Inspection, error)
}

type platformProcess interface {
	inspect(context.Context) (Inspection, error)
	wait(context.Context) (Inspection, error)
	terminate(context.Context, CleanupRef, time.Duration) (Inspection, error)
	close() error
	observation() ProcessObservation
}

type Coordinator struct{ platform platformCoordinator }

// New constructs the production platform coordinator around one fixed Marshal
// image. Darwin requires an absolute, symlink-free path; other platforms fail
// closed.
func New(authority AttemptAuthority, fixedMarshalPath string) (*Coordinator, error) {
	platform, err := newPlatformCoordinator(authority, fixedMarshalPath)
	if err != nil {
		return nil, err
	}
	return &Coordinator{platform: platform}, nil
}

func (coordinator *Coordinator) Launch(ctx context.Context, request LaunchRequest) (*Process, error) {
	if coordinator == nil || coordinator.platform == nil {
		return nil, ErrUnsupported
	}
	platform, err := coordinator.platform.launch(ctx, request)
	if err != nil {
		if platform != nil {
			return &Process{platform: platform}, err
		}
		return nil, err
	}
	return &Process{platform: platform}, nil
}

// Reconcile inspects a persisted observation after restart. It never recreates
// a kill-capable Process because the original wait right, held FDs, and vnode
// guards are gone.
func (coordinator *Coordinator) Reconcile(ctx context.Context, authority AuthorityRef, observation ProcessObservation) (Inspection, error) {
	if coordinator == nil || coordinator.platform == nil {
		return Inspection{}, ErrUnsupported
	}
	return coordinator.platform.reconcile(ctx, authority, observation, CleanupRef{})
}

// ReconcileCleanup is the only post-barrier restart inspection. It remains
// read-only and never recreates a signal-capable process handle.
func (coordinator *Coordinator) ReconcileCleanup(ctx context.Context, authority AuthorityRef, observation ProcessObservation, cleanup CleanupRef) (Inspection, error) {
	if coordinator == nil || coordinator.platform == nil {
		return Inspection{}, ErrUnsupported
	}
	if err := cleanup.validate(); err != nil {
		return Inspection{}, err
	}
	return coordinator.platform.reconcile(ctx, authority, observation, cleanup)
}

type Process struct{ platform platformProcess }

func (process *Process) Observation() ProcessObservation {
	if process == nil || process.platform == nil {
		return ProcessObservation{}
	}
	return process.platform.observation()
}

func (process *Process) Inspect(ctx context.Context) (Inspection, error) {
	if process == nil || process.platform == nil {
		return Inspection{}, ErrClosed
	}
	return process.platform.inspect(ctx)
}

func (process *Process) Wait(ctx context.Context) (Inspection, error) {
	if process == nil || process.platform == nil {
		return Inspection{}, ErrClosed
	}
	return process.platform.wait(ctx)
}

func (process *Process) Terminate(ctx context.Context, cleanup CleanupRef, grace time.Duration) (Inspection, error) {
	if process == nil || process.platform == nil {
		return Inspection{}, ErrClosed
	}
	if grace < 0 {
		return Inspection{}, fmt.Errorf("%w: negative grace", ErrStillRunning)
	}
	if err := cleanup.validate(); err != nil {
		return Inspection{}, err
	}
	return process.platform.terminate(ctx, cleanup, grace)
}

func (process *Process) Close() error {
	if process == nil || process.platform == nil {
		return ErrClosed
	}
	err := process.platform.close()
	if err == nil {
		process.platform = nil
	}
	return err
}
