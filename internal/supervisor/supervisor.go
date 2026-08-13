package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// Action is the closed set of actions the supervisor may take for one Run.
type Action string

const (
	// ActionNone means no supervisor intervention is required for the Run.
	ActionNone Action = "none"
	// ActionRunWorker spawns a `marshal task run` driver for the Run.
	ActionRunWorker Action = "run-worker"
	// ActionRetryPublish spawns a `marshal task publish` driver for the Run.
	ActionRetryPublish Action = "retry-publish"
)

// String returns the stable string form of the Action.
func (a Action) String() string { return string(a) }

// DefaultStalenessThreshold is how long an actively-driven Run may go
// without a fresh journal event before its driver is considered dead.
const DefaultStalenessThreshold = 30 * time.Minute

var (
	// ErrMarshalBinaryUnavailable is the fixed sentinel returned by New when
	// the configured marshal CLI binary does not exist or is not executable.
	ErrMarshalBinaryUnavailable = errors.New("supervisor: marshal binary unavailable")
	// ErrInvalidInterval is returned by Loop when the supervise interval is
	// not positive.
	ErrInvalidInterval = errors.New("supervisor: supervise interval must be positive")
)

// RunStatus is the supervisor's read-only view of one scanned Run.
type RunStatus struct {
	RunID       string
	State       domain.State
	DriverAlive bool
	// SkipReason is non-empty when the Run could not be inspected with
	// journal consistency (for example a corrupted snapshot or journal) and
	// was therefore skipped for decision-making.
	SkipReason string
}

// DecisionRecord describes one dispatch decision taken by Supervise.
type DecisionRecord struct {
	RunID   string
	State   domain.State
	Action  Action
	Started bool
	Error   string
}

// Executor starts one marshal CLI child process. The production
// implementation is commandExecutor (exec.CommandContext based); tests
// inject a fake through WithExecutor, mirroring the verification runner's
// injection style.
type Executor interface {
	Start(ctx context.Context, argv []string) error
}

// Supervisor scans the Marshal run store under stateRoot, decides which Runs
// need a driver process and spawns those drivers through the marshal CLI
// binary at marshalBinary. It never writes Run state itself.
type Supervisor struct {
	stateRoot          string
	marshalBinary      string
	stalenessThreshold time.Duration
	now                func() time.Time
	executor           Executor
}

// Option customises a Supervisor constructed by New.
type Option func(*Supervisor)

// WithExecutor injects a custom Executor (test seam). A nil executor is
// ignored and the production implementation is kept.
func WithExecutor(executor Executor) Option {
	return func(s *Supervisor) {
		if executor != nil {
			s.executor = executor
		}
	}
}

// WithStalenessThreshold overrides the driver liveness staleness threshold.
// Non-positive values are ignored and the default is kept.
func WithStalenessThreshold(threshold time.Duration) Option {
	return func(s *Supervisor) {
		if threshold > 0 {
			s.stalenessThreshold = threshold
		}
	}
}

// WithClock injects the clock used for staleness evaluation (test seam). A
// nil clock is ignored and the wall clock is kept.
func WithClock(now func() time.Time) Option {
	return func(s *Supervisor) {
		if now != nil {
			s.now = now
		}
	}
}

// New constructs a Supervisor rooted at stateRoot that spawns drivers via
// marshalBinary. marshalBinary must exist and be executable; otherwise the
// fixed sentinel ErrMarshalBinaryUnavailable is returned.
func New(stateRoot, marshalBinary string, opts ...Option) (*Supervisor, error) {
	if stateRoot == "" {
		return nil, errors.New("supervisor: state root must not be empty")
	}
	info, err := os.Stat(marshalBinary)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMarshalBinaryUnavailable, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%w: %s is a directory", ErrMarshalBinaryUnavailable, marshalBinary)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("%w: %s is not executable", ErrMarshalBinaryUnavailable, marshalBinary)
	}
	s := &Supervisor{stateRoot: stateRoot, marshalBinary: marshalBinary}
	for _, opt := range opts {
		opt(s)
	}
	if s.stalenessThreshold <= 0 {
		s.stalenessThreshold = DefaultStalenessThreshold
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.executor == nil {
		s.executor = commandExecutor{}
	}
	return s, nil
}

// Scan walks stateRoot/runs/* and returns one RunStatus per run directory,
// deterministically sorted by RunID. The authoritative state of each Run is
// read through runstore.Inspect so snapshot and journal consistency is
// enforced. Runs whose inspection fails are not fatal for the round: they
// are reported with a non-empty SkipReason instead.
func (s *Supervisor) Scan(ctx context.Context) ([]RunStatus, error) {
	runsDir := filepath.Join(s.stateRoot, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []RunStatus{}, nil
		}
		return nil, fmt.Errorf("supervisor: read runs directory: %w", err)
	}
	store := runstore.New(s.stateRoot)
	statuses := make([]RunStatus, 0, len(entries))
	for _, entry := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if !entry.IsDir() {
			continue
		}
		runID := entry.Name()
		state, inspectErr := store.Inspect(runID)
		if inspectErr != nil {
			statuses = append(statuses, RunStatus{RunID: runID, SkipReason: fmt.Sprintf("inspect failed: %v", inspectErr)})
			continue
		}
		statuses = append(statuses, RunStatus{RunID: runID, State: state.State, DriverAlive: s.driverAlive(state)})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].RunID < statuses[j].RunID })
	return statuses, nil
}

// driverAlive applies the conservative liveness signal: only Runs in an
// actively-driven state can be alive at all, and only while their most
// recent journal event (reflected in RunState.UpdatedAt after inspection)
// is within the staleness threshold. Runs waiting for dispatch or in any
// other state have no driver to probe and always report false.
func (s *Supervisor) driverAlive(state domain.RunState) bool {
	switch state.State {
	case domain.StateRunning, domain.StateVerifying, domain.StatePublishing:
	default:
		return false
	}
	if state.UpdatedAt.IsZero() {
		return false
	}
	return s.now().Sub(state.UpdatedAt.UTC()) <= s.stalenessThreshold
}

// Decide maps one scanned RunStatus to the supervisor Action. It is a pure
// function and never touches Run state: READY, REWORK_REQUESTED and
// RETRY_PENDING wait for a worker driver; PUBLISHING with a dead driver is
// re-published; everything else is left alone.
func (s *Supervisor) Decide(status RunStatus) Action {
	if status.RunID == "" || status.SkipReason != "" {
		return ActionNone
	}
	switch status.State {
	case domain.StateReady, domain.StateReworkRequested, domain.StateRetryPending:
		return ActionRunWorker
	case domain.StatePublishing:
		if status.DriverAlive {
			return ActionNone
		}
		return ActionRetryPublish
	default:
		return ActionNone
	}
}

// Supervise performs one scan-decide-dispatch round and returns one
// DecisionRecord per non-none action, deterministically sorted by RunID. A
// failed spawn is recorded on its own record and never aborts the rest of
// the round.
func (s *Supervisor) Supervise(ctx context.Context) ([]DecisionRecord, error) {
	statuses, err := s.Scan(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]DecisionRecord, 0, len(statuses))
	for _, status := range statuses {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return records, ctxErr
		}
		action := s.Decide(status)
		if action == ActionNone {
			continue
		}
		record := DecisionRecord{RunID: status.RunID, State: status.State, Action: action}
		if startErr := s.executor.Start(ctx, s.commandArgv(action, status.RunID)); startErr != nil {
			record.Error = startErr.Error()
		} else {
			record.Started = true
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].RunID < records[j].RunID })
	return records, nil
}

// commandArgv builds the exact marshal CLI invocation for one action. All
// Run state semantics stay inside that child process; the supervisor only
// supplies argv.
func (s *Supervisor) commandArgv(action Action, runID string) []string {
	switch action {
	case ActionRunWorker:
		return []string{s.marshalBinary, "task", "run", "--run", runID, "--through-verify", "--json"}
	case ActionRetryPublish:
		return []string{s.marshalBinary, "task", "publish", "--run", runID, "--json"}
	default:
		return nil
	}
}

// Loop repeats Supervise every interval until ctx is done. A context
// cancellation returns nil; any other Supervise error is returned.
func (s *Supervisor) Loop(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return ErrInvalidInterval
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := s.Supervise(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// commandExecutor is the production Executor: it starts each marshal CLI
// invocation as a child process via exec.CommandContext.
//
// The child is deliberately detached from the scan context with
// context.WithoutCancel: spawned drivers must outlive the supervise round
// that started them, including the supervisor itself stopping. Killing the
// drivers together with the supervisor context would reproduce exactly the
// silent-death failure mode the supervisor exists to eliminate.
type commandExecutor struct{}

// Start starts the child process and reaps it asynchronously; it never
// blocks on the child's completion.
func (commandExecutor) Start(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return errors.New("supervisor: empty argv")
	}
	command := exec.CommandContext(context.WithoutCancel(ctx), argv[0], argv[1:]...)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
