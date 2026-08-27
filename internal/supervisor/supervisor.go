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
	"github.com/chiga0/marshal-harness/internal/explain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/recovery"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/gofrs/flock"
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
const DefaultStalenessThreshold = lifecycle.DefaultDriverStalenessThreshold

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
	// LeaseHeld is the actual OS ownership observation. DriverAlive may also
	// remain true during the short journal-age grace window, but a held lease
	// always wins over event age so a legitimate long attempt is never
	// declared dead merely because it emitted no recent lifecycle event.
	LeaseHeld bool
	// SkipReason is non-empty when the Run could not be inspected with
	// journal consistency (for example a corrupted snapshot or journal) and
	// was therefore skipped for decision-making.
	SkipReason string
}

// SkipReasonExcluded is the stable DecisionRecord.SkipReason for candidates
// listed in the supervise-exclude list (issue #100): they are never
// re-dispatched by supervise.
const SkipReasonExcluded = "excluded by supervise-exclude list"

// DecisionRecord describes one dispatch decision taken by Supervise.
// SkipReason is non-empty when a dispatch candidate was deliberately not
// started (exclusion list or write-domain conflict); Started and Error then
// both stay zero values.
type DecisionRecord struct {
	RunID      string
	State      domain.State
	Action     Action
	Started    bool
	Error      string
	SkipReason string
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
	// reviveRetryPending gates the RETRY_PENDING opt-in introduced by
	// issue #100: false (the default) leaves RETRY_PENDING Runs alone.
	reviveRetryPending bool
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

// WithReviveRetryPending opts back into the pre-issue-#100 behaviour of
// automatically reviving RETRY_PENDING Runs. The default (false) never
// re-dispatches RETRY_PENDING Runs: revival requires this explicit opt-in,
// exposed on the supervise command as --revive-retry-pending.
func WithReviveRetryPending(enabled bool) Option {
	return func(s *Supervisor) {
		s.reviveRetryPending = enabled
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
		s.executor = commandExecutor{stateRoot: s.stateRoot, readinessTimeout: 10 * time.Second}
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
		leaseHeld, leaseErr := store.LeaseHeld(runID)
		if leaseErr != nil {
			statuses = append(statuses, RunStatus{RunID: runID, State: state.State, SkipReason: fmt.Sprintf("ownership probe failed: %v", leaseErr)})
			continue
		}
		driverAlive := s.driverAlive(state, leaseHeld)
		// A recent journal event is normally a conservative grace signal. When
		// the durable owner record proves that its PID has exited, the grace
		// signal is no longer valid and the supervisor may recover immediately.
		if !leaseHeld && isActiveState(state.State) {
			if ownerAlive, ownerErr := store.LeaseOwnerProcessAlive(runID); ownerErr == nil && !ownerAlive {
				driverAlive = false
			}
		}
		statuses = append(statuses, RunStatus{RunID: runID, State: state.State, LeaseHeld: leaseHeld, DriverAlive: driverAlive})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].RunID < statuses[j].RunID })
	return statuses, nil
}

// driverAlive applies the conservative liveness signal: only Runs in an
// actively-driven state can be alive at all. A held OS lease is affirmative
// owner evidence and always wins, including for a long attempt with no new
// journal event. Without a held lease, the recent-event window is only a
// race-avoidance grace period; once it expires the driver is considered
// dead. Runs waiting for dispatch or in any other state always report false.
func (s *Supervisor) driverAlive(state domain.RunState, leaseHeld bool) bool {
	switch state.State {
	case domain.StateRunning, domain.StateVerifying, domain.StatePublishing:
	default:
		return false
	}
	if leaseHeld {
		return true
	}
	if state.UpdatedAt.IsZero() {
		return false
	}
	return s.now().Sub(state.UpdatedAt.UTC()) <= s.stalenessThreshold
}

func isActiveState(state domain.State) bool {
	switch state {
	case domain.StateRunning, domain.StateVerifying, domain.StatePublishing:
		return true
	default:
		return false
	}
}

// Decide maps one scanned RunStatus to the supervisor Action. It is a pure
// function and never touches Run state: READY and REWORK_REQUESTED wait for
// a worker driver; RETRY_PENDING waits for a worker driver only when the
// supervisor was constructed with WithReviveRetryPending — since issue #100
// RETRY_PENDING Runs are never revived automatically by default; PUBLISHING
// with a dead driver is re-published; everything else is left alone.
func (s *Supervisor) Decide(status RunStatus) Action {
	if status.RunID == "" || status.SkipReason != "" {
		return ActionNone
	}
	// A child may hold the Run lease before appending worker.started. Treat
	// that ownership as an admitted in-flight driver in every source state;
	// otherwise a second supervisor could duplicate the spawn in this gap.
	if status.LeaseHeld {
		return ActionNone
	}
	switch status.State {
	case domain.StateReady, domain.StateReworkRequested:
		return ActionRunWorker
	case domain.StateRetryPending:
		if s.reviveRetryPending {
			return ActionRunWorker
		}
		return ActionNone
	case domain.StateRunning:
		if status.DriverAlive {
			return ActionNone
		}
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
// DecisionRecord per dispatch decision, deterministically sorted by RunID:
// started actions, failed starts and candidates skipped before dispatch
// (exclusion list or write-domain conflict) each carry their own record. A
// failed spawn is recorded on its own record and never aborts the rest of
// the round.
//
// Issue #100 gates every re-dispatch fail-closed. The round first loads the
// supervise-exclude list: an existing but unreadable list aborts the whole
// round with ErrExcludeListUnreadable and zero dispatches; any candidate
// listed in it is skipped forever. Before starting a driver the round then
// checks the candidate's frozen TaskSpec write domain against every
// in-flight Run (non-terminal state with a live driver) and skips the
// candidate on any path overlap, directory-prefix or wildcard containment.
func (s *Supervisor) Supervise(ctx context.Context) ([]DecisionRecord, error) {
	// Serialize scan + admission + spawn with the existing repository-wide
	// coordination lock used by worktree lifecycle operations. This closes
	// the cross-process TOCTOU where two supervisors could scan the same
	// authority snapshot and both admit overlapping write domains. Drivers
	// acquire their Run lease before attempting the worktree lock, so holding
	// this lock across Start cannot invert the ownership order.
	coordination, err := s.acquireCoordinationLock(ctx)
	if err != nil {
		return nil, err
	}
	defer coordination.Unlock()
	excluded, err := loadExcludeList(s.stateRoot)
	if err != nil {
		// Fail closed: report the read failure and re-dispatch nothing.
		return nil, err
	}
	statuses, err := s.Scan(ctx)
	if err != nil {
		return nil, err
	}
	inflight := make([]string, 0, len(statuses))
	for _, status := range statuses {
		if status.SkipReason != "" || status.State.Terminal() || (!status.DriverAlive && !status.LeaseHeld) {
			continue
		}
		inflight = append(inflight, status.RunID)
	}
	inflightDomains := make(map[string][]string, len(inflight))
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
		if _, isExcluded := excluded[status.RunID]; isExcluded {
			record.SkipReason = SkipReasonExcluded
			records = append(records, record)
			continue
		}
		if reason := s.writeDomainConflictReason(status.RunID, inflight, inflightDomains); reason != "" {
			record.SkipReason = reason
			records = append(records, record)
			continue
		}
		recoverDeadDriver := action == ActionRunWorker && status.State == domain.StateRunning && !status.DriverAlive && !status.LeaseHeld
		if recoverDeadDriver {
			// ADR 0053 决策 5：死 driver 分派唯一经由单一恢复模型判定。
			// ActionNewAttempt 且无需幂等键对账时才允许立即接管；其余决策
			// （无效输入、binding 损伤、ambiguous side effect）一律 fail
			// closed——不派生 driver，把对账交给 `marshal explain run`。
			if decision, decisionErr := s.recoveryDecision(status.RunID); decisionErr != nil {
				record.SkipReason = fmt.Sprintf("recovery decision unavailable: %v", decisionErr)
				records = append(records, record)
				continue
			} else if decision.Action != recovery.ActionNewAttempt || decision.RequiresReconcile {
				record.SkipReason = fmt.Sprintf("recovery decision blocks re-dispatch (action=%s rationale=%s); reconcile via `marshal explain run %s`", decision.Action, decision.Rationale, status.RunID)
				records = append(records, record)
				continue
			}
		}
		if startErr := s.executor.Start(ctx, s.commandArgv(action, status.RunID, recoverDeadDriver)); startErr != nil {
			record.Error = startErr.Error()
		} else {
			record.Started = true
			// A driver successfully admitted in this round is immediately part
			// of the in-flight write-domain set. This prevents two candidates
			// selected from the same scan (for example READY plus orphaned
			// RUNNING) from being spawned concurrently into overlapping paths.
			inflight = append(inflight, status.RunID)
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].RunID < records[j].RunID })
	return records, nil
}

// recoveryDecision 以 supervisor 自身 staleness 窗口装配权威事实并返回
// 单一恢复模型的 Decision（ADR 0053 决策 5）。只读，不改写任何 Run 状态；
// driver 死亡判定与恢复决策必须使用同一观测窗口，结论才单调自洽。
func (s *Supervisor) recoveryDecision(runID string) (recovery.Decision, error) {
	x, err := explain.AssembleWithStaleness(s.stateRoot, runID, s.now(), s.stalenessThreshold)
	if x == nil {
		return recovery.Decision{}, err
	}
	if err != nil {
		return x.Decision, err
	}
	return x.Decision, nil
}

func (s *Supervisor) acquireCoordinationLock(ctx context.Context) (*flock.Flock, error) {
	locks := filepath.Join(s.stateRoot, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return nil, fmt.Errorf("supervisor: create coordination directory: %w", err)
	}
	lock := flock.New(filepath.Join(locks, "repository.lock"))
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("supervisor: acquire repository coordination lock: %w", err)
	}
	if !locked {
		return nil, errors.New("supervisor: repository coordination lock unavailable")
	}
	return lock, nil
}

// writeDomainConflictReason returns a non-empty reason when the candidate
// Run must not be re-dispatched because its frozen TaskSpec write domain
// (scope.allowPaths) overlaps the write domain of at least one in-flight
// Run. Every undecidable input fails closed and yields a skip: an
// unreadable task-spec.json on either side counts as a conflict. The check
// only reads the frozen task-spec.json files and never modifies them. An
// empty return value means no conflict was detected; with no in-flight Runs
// the check passes without reading any spec.
func (s *Supervisor) writeDomainConflictReason(candidateID string, inflightIDs []string, domainCache map[string][]string) string {
	if len(inflightIDs) == 0 {
		return ""
	}
	candidatePaths, err := readSpecAllowPaths(s.stateRoot, candidateID)
	if err != nil {
		return fmt.Sprintf("write-domain check failed closed for candidate %s: %v", candidateID, err)
	}
	for _, runID := range inflightIDs {
		inflightPaths, cached := domainCache[runID]
		if !cached {
			var readErr error
			inflightPaths, readErr = readSpecAllowPaths(s.stateRoot, runID)
			if readErr != nil {
				return fmt.Sprintf("write-domain check failed closed for in-flight run %s: %v", runID, readErr)
			}
			domainCache[runID] = inflightPaths
		}
		if allowPathsConflict(candidatePaths, inflightPaths) {
			return fmt.Sprintf("write-domain conflict with in-flight run %s", runID)
		}
	}
	return ""
}

// commandArgv builds the exact marshal CLI invocation for one action. All
// Run state semantics stay inside that child process; the supervisor only
// supplies argv.
func (s *Supervisor) commandArgv(action Action, runID string, recoverDeadDriver bool) []string {
	switch action {
	case ActionRunWorker:
		args := []string{s.marshalBinary, "task", "run", "--run", runID, "--through-verify"}
		if recoverDeadDriver {
			args = append(args, "--recover-dead-driver")
		}
		return append(args, "--json")
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
type commandExecutor struct {
	stateRoot        string
	readinessTimeout time.Duration
}

// Start starts the child and does not acknowledge dispatch until the child
// holds the authoritative Run lease (or exits after completing synchronously).
// A child that misses the bounded readiness window is killed and reaped while
// the repository coordination lock is still held, so another supervisor can
// never race a late, unreserved child.
func (executor commandExecutor) Start(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return errors.New("supervisor: empty argv")
	}
	runID := ""
	for index, argument := range argv {
		if argument == "--run" && index+1 < len(argv) {
			runID = argv[index+1]
			break
		}
	}
	if runID == "" || executor.stateRoot == "" {
		return errors.New("supervisor: child readiness requires state root and run ID")
	}
	command := exec.CommandContext(context.WithoutCancel(ctx), argv[0], argv[1:]...)
	if err := command.Start(); err != nil {
		return err
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	timeout := executor.readinessTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	store := runstore.New(executor.stateRoot)
	for {
		select {
		case err := <-waited:
			if err != nil {
				return fmt.Errorf("supervisor: child exited before lease readiness: %w", err)
			}
			return errors.New("supervisor: child exited before acquiring the Run lease")
		case <-ticker.C:
			held, err := store.LeaseHeld(runID)
			if err == nil && held {
				return nil
			}
		case <-timer.C:
			_ = command.Process.Kill()
			<-waited
			return errors.New("supervisor: child did not acquire the Run lease before readiness timeout")
		case <-ctx.Done():
			_ = command.Process.Kill()
			<-waited
			return ctx.Err()
		}
	}
}
