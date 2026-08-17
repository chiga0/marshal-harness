package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

const fixtureTaskID = "task-supervisor-fixture"

// fixtureNow is the injected supervisor clock so staleness tests never wait
// on the wall clock.
var fixtureNow = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

// fixtureStaleAge exceeds DefaultStalenessThreshold, so a Run whose most
// recent journal event is this old has a dead driver.
var fixtureStaleAge = DefaultStalenessThreshold + time.Minute

func writeFakeMarshalBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "marshal-fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write fake marshal binary: %v", err)
	}
	return path
}

func newSupervisor(t *testing.T, stateRoot string, executor Executor, opts ...Option) (*Supervisor, string) {
	t.Helper()
	binary := writeFakeMarshalBinary(t)
	options := append([]Option{WithExecutor(executor), WithClock(func() time.Time { return fixtureNow })}, opts...)
	supervisor, err := New(stateRoot, binary, options...)
	if err != nil {
		t.Fatalf("construct supervisor: %v", err)
	}
	return supervisor, binary
}

func pathTo(states ...domain.State) []domain.State { return append([]domain.State(nil), states...) }

func transitionEventType(to domain.State) string {
	switch to {
	case domain.StatePlanned:
		return "task.planned"
	case domain.StateReady:
		return "task.ready"
	case domain.StateRunning:
		return "worker.started"
	case domain.StateRetryPending:
		return "worker.retry-pending"
	case domain.StateVerifying:
		return "worker.finished"
	case domain.StateReviewPending:
		return "verification.completed"
	case domain.StateReworkRequested:
		return "review.rework-requested"
	case domain.StatePublishing:
		return "publication.started"
	case domain.StateAccepted:
		return "run.accepted"
	case domain.StateRejected:
		return "run.rejected"
	case domain.StateBlocked:
		return "run.blocked"
	default:
		return "run.transition"
	}
}

// seedRun builds a real runstore fixture under stateRoot whose journal walks
// the lifecycle from CREATED through path, with every journal event stamped
// at lastEventAt so staleness evaluation is deterministic.
func seedRun(t *testing.T, stateRoot, runID string, path []domain.State, lastEventAt time.Time) {
	t.Helper()
	store := runstore.New(stateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatalf("acquire lease for %s: %v", runID, err)
	}
	defer func() {
		if err := lease.Release(); err != nil {
			t.Fatalf("release lease for %s: %v", runID, err)
		}
	}()
	snapshot := domain.NewRunState(fixtureTaskID, runID, lastEventAt)
	if err := store.WriteSnapshot(lease, snapshot); err != nil {
		t.Fatalf("write snapshot for %s: %v", runID, err)
	}
	from := domain.StateCreated
	for index, to := range path {
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindRunEvent,
			EventID:    fmt.Sprintf("evt-%s-%d", runID, index+1),
			RunID:      runID,
			Sequence:   uint64(index + 1),
			Type:       transitionEventType(to),
			StateFrom:  from,
			StateTo:    to,
			Timestamp:  lastEventAt,
			Payload:    map[string]any{"fixture": "supervisor-test"},
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatalf("append event %d for %s: %v", index+1, runID, err)
		}
		from = to
	}
}

// fakeExecutor records every argv it starts and can refuse one specific Run.
type fakeExecutor struct {
	started   [][]string
	attempts  int
	failRunID string
	onStart   func()
}

type executorFunc func(context.Context, []string) error

func (f executorFunc) Start(ctx context.Context, argv []string) error { return f(ctx, argv) }

func (f *fakeExecutor) Start(_ context.Context, argv []string) error {
	f.attempts++
	if f.failRunID != "" && runIDFromArgv(argv) == f.failRunID {
		return errors.New("fake executor: start refused for " + f.failRunID)
	}
	f.started = append(f.started, append([]string(nil), argv...))
	if f.onStart != nil {
		f.onStart()
	}
	return nil
}

func runIDFromArgv(argv []string) string {
	for index, part := range argv {
		if part == "--run" && index+1 < len(argv) {
			return argv[index+1]
		}
	}
	return ""
}

func TestActionStrings(t *testing.T) {
	cases := []struct {
		action Action
		want   string
	}{
		{ActionRunWorker, "run-worker"},
		{ActionRetryPublish, "retry-publish"},
		{ActionNone, "none"},
	}
	for _, tc := range cases {
		if got := tc.action.String(); got != tc.want {
			t.Fatalf("Action(%q).String() = %q, want %q", string(tc.action), got, tc.want)
		}
	}
}

func TestNewValidatesMarshalBinary(t *testing.T) {
	t.Run("missing binary fails with fixed sentinel", func(t *testing.T) {
		_, err := New(t.TempDir(), filepath.Join(t.TempDir(), "missing-marshal"))
		if !errors.Is(err, ErrMarshalBinaryUnavailable) {
			t.Fatalf("New with missing binary: err = %v, want ErrMarshalBinaryUnavailable", err)
		}
	})
	t.Run("directory instead of binary fails with fixed sentinel", func(t *testing.T) {
		_, err := New(t.TempDir(), t.TempDir())
		if !errors.Is(err, ErrMarshalBinaryUnavailable) {
			t.Fatalf("New with directory binary: err = %v, want ErrMarshalBinaryUnavailable", err)
		}
	})
	t.Run("binary without execute permission fails with fixed sentinel", func(t *testing.T) {
		flat := filepath.Join(t.TempDir(), "marshal-flat")
		if err := os.WriteFile(flat, []byte("not executable"), 0o600); err != nil {
			t.Fatalf("write flat binary: %v", err)
		}
		_, err := New(t.TempDir(), flat)
		if !errors.Is(err, ErrMarshalBinaryUnavailable) {
			t.Fatalf("New with non-executable binary: err = %v, want ErrMarshalBinaryUnavailable", err)
		}
	})
	t.Run("empty state root fails", func(t *testing.T) {
		_, err := New("", writeFakeMarshalBinary(t))
		if err == nil {
			t.Fatal("New with empty state root must fail")
		}
	})
	t.Run("valid binary applies defaults", func(t *testing.T) {
		supervisor, err := New(t.TempDir(), writeFakeMarshalBinary(t))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if supervisor.stalenessThreshold != DefaultStalenessThreshold {
			t.Fatalf("stalenessThreshold = %v, want default %v", supervisor.stalenessThreshold, DefaultStalenessThreshold)
		}
		if supervisor.executor == nil || supervisor.now == nil {
			t.Fatal("New must install default executor and clock")
		}
	})
}

func TestDecideMatrix(t *testing.T) {
	supervisor, _ := newSupervisor(t, t.TempDir(), &fakeExecutor{})
	cases := []struct {
		name   string
		status RunStatus
		want   Action
	}{
		{name: "ready waits for worker", status: RunStatus{RunID: "run-x", State: domain.StateReady}, want: ActionRunWorker},
		{name: "rework requested returns to worker", status: RunStatus{RunID: "run-x", State: domain.StateReworkRequested}, want: ActionRunWorker},
		{name: "retry pending is left alone by default", status: RunStatus{RunID: "run-x", State: domain.StateRetryPending}, want: ActionNone},
		{name: "publishing with dead driver retries publish", status: RunStatus{RunID: "run-x", State: domain.StatePublishing, DriverAlive: false}, want: ActionRetryPublish},
		{name: "publishing with live driver is left alone", status: RunStatus{RunID: "run-x", State: domain.StatePublishing, DriverAlive: true}, want: ActionNone},
		{name: "running with live driver is left alone", status: RunStatus{RunID: "run-x", State: domain.StateRunning, DriverAlive: true}, want: ActionNone},
		{name: "running with dead driver returns to core", status: RunStatus{RunID: "run-x", State: domain.StateRunning, DriverAlive: false}, want: ActionRunWorker},
		{name: "review pending is left alone", status: RunStatus{RunID: "run-x", State: domain.StateReviewPending}, want: ActionNone},
		{name: "published is left alone", status: RunStatus{RunID: "run-x", State: domain.StatePublished}, want: ActionNone},
		{name: "ci pending is left alone", status: RunStatus{RunID: "run-x", State: domain.StateCIPending}, want: ActionNone},
		{name: "accepted is terminal", status: RunStatus{RunID: "run-x", State: domain.StateAccepted}, want: ActionNone},
		{name: "rejected is terminal", status: RunStatus{RunID: "run-x", State: domain.StateRejected}, want: ActionNone},
		{name: "blocked is terminal", status: RunStatus{RunID: "run-x", State: domain.StateBlocked}, want: ActionNone},
		{name: "skipped run is never dispatched", status: RunStatus{RunID: "run-x", State: domain.StateReady, SkipReason: "inspect failed: corrupted"}, want: ActionNone},
		{name: "empty run id is never dispatched", status: RunStatus{State: domain.StateReady}, want: ActionNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := supervisor.Decide(tc.status); got != tc.want {
				t.Fatalf("Decide(%+v) = %q, want %q", tc.status, got.String(), tc.want.String())
			}
		})
	}
	t.Run("retry pending revives only with explicit opt-in", func(t *testing.T) {
		reviving, _ := newSupervisor(t, t.TempDir(), &fakeExecutor{}, WithReviveRetryPending(true))
		status := RunStatus{RunID: "run-x", State: domain.StateRetryPending}
		if got := reviving.Decide(status); got != ActionRunWorker {
			t.Fatalf("Decide with WithReviveRetryPending = %q, want %q", got.String(), ActionRunWorker.String())
		}
		if got := supervisor.Decide(status); got != ActionNone {
			t.Fatalf("Decide without opt-in = %q, want %q", got.String(), ActionNone.String())
		}
	})
}

func TestScanStateMatrix(t *testing.T) {
	publishingPath := func() []domain.State {
		return pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StatePublishing)
	}
	cases := []struct {
		name       string
		path       []domain.State
		age        time.Duration
		wantState  domain.State
		wantAlive  bool
		wantAction Action
	}{
		{name: "ready run waits for dispatch", path: pathTo(domain.StatePlanned, domain.StateReady), age: 0, wantState: domain.StateReady, wantAlive: false, wantAction: ActionRunWorker},
		{name: "rework requested run waits for dispatch", path: pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StateReworkRequested), age: 0, wantState: domain.StateReworkRequested, wantAlive: false, wantAction: ActionRunWorker},
		{name: "retry pending run stays undispatched by default", path: pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateRetryPending), age: 0, wantState: domain.StateRetryPending, wantAlive: false, wantAction: ActionNone},
		{name: "publishing with dead driver retries publish", path: publishingPath(), age: fixtureStaleAge, wantState: domain.StatePublishing, wantAlive: false, wantAction: ActionRetryPublish},
		{name: "publishing with live driver is left alone", path: publishingPath(), age: time.Minute, wantState: domain.StatePublishing, wantAlive: true, wantAction: ActionNone},
		{name: "publishing exactly at threshold stays alive", path: publishingPath(), age: DefaultStalenessThreshold, wantState: domain.StatePublishing, wantAlive: true, wantAction: ActionNone},
		{name: "running with live driver is left alone", path: pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning), age: time.Minute, wantState: domain.StateRunning, wantAlive: true, wantAction: ActionNone},
		{name: "running with dead driver returns to core", path: pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning), age: fixtureStaleAge, wantState: domain.StateRunning, wantAlive: false, wantAction: ActionRunWorker},
		{name: "verifying with live driver is left alone", path: pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying), age: time.Minute, wantState: domain.StateVerifying, wantAlive: true, wantAction: ActionNone},
		{name: "accepted run is terminal", path: pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StateAccepted), age: 0, wantState: domain.StateAccepted, wantAlive: false, wantAction: ActionNone},
		{name: "rejected run is terminal", path: pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StateRejected), age: 0, wantState: domain.StateRejected, wantAlive: false, wantAction: ActionNone},
		{name: "blocked run is terminal", path: pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateBlocked), age: 0, wantState: domain.StateBlocked, wantAlive: false, wantAction: ActionNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			seedRun(t, root, "run-matrix", tc.path, fixtureNow.Add(-tc.age))
			supervisor, _ := newSupervisor(t, root, &fakeExecutor{})
			statuses, err := supervisor.Scan(context.Background())
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(statuses) != 1 {
				t.Fatalf("Scan returned %d statuses, want 1: %+v", len(statuses), statuses)
			}
			status := statuses[0]
			if status.RunID != "run-matrix" {
				t.Fatalf("status.RunID = %q, want run-matrix", status.RunID)
			}
			if status.SkipReason != "" {
				t.Fatalf("status.SkipReason = %q, want none", status.SkipReason)
			}
			if status.State != tc.wantState {
				t.Fatalf("status.State = %q, want %q", status.State, tc.wantState)
			}
			if status.DriverAlive != tc.wantAlive {
				t.Fatalf("status.DriverAlive = %v, want %v", status.DriverAlive, tc.wantAlive)
			}
			if got := supervisor.Decide(status); got != tc.wantAction {
				t.Fatalf("Decide = %q, want %q", got.String(), tc.wantAction.String())
			}
		})
	}
}

func TestScanWithoutRunsDirectory(t *testing.T) {
	supervisor, _ := newSupervisor(t, t.TempDir(), &fakeExecutor{})
	statuses, err := supervisor.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan on empty state root: %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("statuses = %+v, want none", statuses)
	}
}

func TestScanHeldLeaseKeepsStaleLongAttemptAlive(t *testing.T) {
	root := t.TempDir()
	seedRun(t, root, "run-long", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning), fixtureNow.Add(-fixtureStaleAge))
	lease, err := runstore.New(root).Acquire("run-long")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	supervisor, _ := newSupervisor(t, root, &fakeExecutor{})
	statuses, err := supervisor.Scan(context.Background())
	if err != nil || len(statuses) != 1 {
		t.Fatalf("Scan = %+v err=%v", statuses, err)
	}
	if !statuses[0].LeaseHeld || !statuses[0].DriverAlive || supervisor.Decide(statuses[0]) != ActionNone {
		t.Fatalf("stale long-running owner was treated as dead: %+v", statuses[0])
	}
}

func TestScanSkipsBrokenRuns(t *testing.T) {
	root := t.TempDir()
	seedRun(t, root, "run-healthy", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)

	brokenSnapshot := filepath.Join(root, "runs", "run-broken-snapshot")
	if err := os.MkdirAll(brokenSnapshot, 0o700); err != nil {
		t.Fatalf("mkdir broken snapshot dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(brokenSnapshot, "state.json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write broken snapshot: %v", err)
	}

	seedRun(t, root, "run-broken-journal", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	journal, err := os.OpenFile(filepath.Join(root, "runs", "run-broken-journal", "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open journal for corruption: %v", err)
	}
	if _, err := journal.WriteString("this line is not a journal record\n"); err != nil {
		t.Fatalf("corrupt journal: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}

	missingSnapshot := filepath.Join(root, "runs", "run-missing-snapshot")
	if err := os.MkdirAll(missingSnapshot, 0o700); err != nil {
		t.Fatalf("mkdir missing snapshot dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(missingSnapshot, "events.jsonl"), []byte(""), 0o600); err != nil {
		t.Fatalf("write empty journal: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "runs", "stray-file"), []byte("stray"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	fake := &fakeExecutor{}
	supervisor, binary := newSupervisor(t, root, fake)
	statuses, err := supervisor.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan must not fail on broken runs: %v", err)
	}
	wantOrder := []string{"run-broken-journal", "run-broken-snapshot", "run-healthy", "run-missing-snapshot"}
	if len(statuses) != len(wantOrder) {
		t.Fatalf("Scan returned %d statuses, want %d: %+v", len(statuses), len(wantOrder), statuses)
	}
	for index, want := range wantOrder {
		if statuses[index].RunID != want {
			t.Fatalf("statuses[%d].RunID = %q, want %q", index, statuses[index].RunID, want)
		}
	}
	healthy := statuses[2]
	if healthy.SkipReason != "" || healthy.State != domain.StateReady {
		t.Fatalf("healthy run = %+v, want READY without skip reason", healthy)
	}
	for _, index := range []int{0, 1, 3} {
		if !strings.HasPrefix(statuses[index].SkipReason, "inspect failed:") {
			t.Fatalf("statuses[%d].SkipReason = %q, want inspect failure marker", index, statuses[index].SkipReason)
		}
		if statuses[index].State != "" || statuses[index].DriverAlive {
			t.Fatalf("statuses[%d] = %+v, skipped run must carry no state", index, statuses[index])
		}
	}

	records, err := supervisor.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise over broken runs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v, want exactly the healthy READY run", records)
	}
	if records[0].RunID != "run-healthy" || records[0].Action != ActionRunWorker || !records[0].Started || records[0].Error != "" {
		t.Fatalf("records[0] = %+v, healthy run must be dispatched cleanly", records[0])
	}
	wantArgv := []string{binary, "task", "run", "--run", "run-healthy", "--through-verify", "--json"}
	if len(fake.started) != 1 || !reflect.DeepEqual(fake.started[0], wantArgv) {
		t.Fatalf("started argv = %v, want %v", fake.started, wantArgv)
	}
}

func TestSuperviseSpawnsExactArgv(t *testing.T) {
	root := t.TempDir()
	seedRun(t, root, "run-alpha", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	seedRun(t, root, "run-beta", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StatePublishing), fixtureNow.Add(-fixtureStaleAge))
	seedRun(t, root, "run-gamma", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StatePublishing), fixtureNow.Add(-time.Minute))
	// run-gamma is in-flight (live driver), so the candidates carry disjoint
	// frozen write domains to pass the issue #100 conflict check.
	writeTaskSpecFixture(t, root, "run-alpha", []string{"src/alpha/**"})
	writeTaskSpecFixture(t, root, "run-beta", []string{"src/beta/**"})
	writeTaskSpecFixture(t, root, "run-gamma", []string{"src/gamma/**"})
	fake := &fakeExecutor{}
	supervisor, binary := newSupervisor(t, root, fake)
	records, err := supervisor.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	wantRecords := []DecisionRecord{
		{RunID: "run-alpha", State: domain.StateReady, Action: ActionRunWorker, Started: true},
		{RunID: "run-beta", State: domain.StatePublishing, Action: ActionRetryPublish, Started: true},
	}
	if !reflect.DeepEqual(records, wantRecords) {
		t.Fatalf("records = %+v, want %+v", records, wantRecords)
	}
	wantStarted := [][]string{
		{binary, "task", "run", "--run", "run-alpha", "--through-verify", "--json"},
		{binary, "task", "publish", "--run", "run-beta", "--json"},
	}
	if !reflect.DeepEqual(fake.started, wantStarted) {
		t.Fatalf("started argv = %v, want %v", fake.started, wantStarted)
	}
	if fake.attempts != 2 {
		t.Fatalf("executor attempts = %d, want 2 (live publishing run must not be touched)", fake.attempts)
	}
}

func TestSuperviseDoesNotWriteRunState(t *testing.T) {
	root := t.TempDir()
	seedRun(t, root, "run-readonly", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	dir := filepath.Join(root, "runs", "run-readonly")
	snapshotBefore, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("read snapshot before: %v", err)
	}
	journalBefore, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read journal before: %v", err)
	}
	supervisor, _ := newSupervisor(t, root, &fakeExecutor{})
	if _, err := supervisor.Supervise(context.Background()); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	snapshotAfter, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("read snapshot after: %v", err)
	}
	journalAfter, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read journal after: %v", err)
	}
	if !bytes.Equal(snapshotBefore, snapshotAfter) {
		t.Fatal("Supervise modified state.json; supervisor must be read-only plus spawn")
	}
	if !bytes.Equal(journalBefore, journalAfter) {
		t.Fatal("Supervise modified events.jsonl; supervisor must be read-only plus spawn")
	}
}

func TestTwoSupervisorsSerializeOverlappingAdmission(t *testing.T) {
	root := t.TempDir()
	seedRun(t, root, "run-a", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	seedRun(t, root, "run-b", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	writeTaskSpecFixture(t, root, "run-a", []string{"shared/**"})
	writeTaskSpecFixture(t, root, "run-b", []string{"shared/file.go"})
	started := make(chan struct{})
	release := make(chan struct{})
	var owner *runstore.Lease
	firstExec := executorFunc(func(_ context.Context, argv []string) error {
		if runIDFromArgv(argv) == "run-a" {
			var err error
			owner, err = runstore.New(root).Acquire("run-a")
			if err != nil {
				return err
			}
			close(started)
			<-release
		}
		return nil
	})
	secondFake := &fakeExecutor{}
	first, _ := newSupervisor(t, root, firstExec)
	second, _ := newSupervisor(t, root, secondFake)
	firstDone := make(chan error, 1)
	go func() { _, err := first.Supervise(context.Background()); firstDone <- err }()
	<-started
	secondDone := make(chan error, 1)
	go func() { _, err := second.Supervise(context.Background()); secondDone <- err }()
	select {
	case err := <-secondDone:
		t.Fatalf("second supervisor escaped repository coordination lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	if secondFake.attempts != 0 {
		t.Fatalf("second supervisor dispatched %d overlapping runs", secondFake.attempts)
	}
}

func TestSuperviseIsolatesStartFailures(t *testing.T) {
	root := t.TempDir()
	seedRun(t, root, "run-a", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	seedRun(t, root, "run-b", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	seedRun(t, root, "run-c", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	writeTaskSpecFixture(t, root, "run-a", []string{"a/**"})
	writeTaskSpecFixture(t, root, "run-b", []string{"b/**"})
	writeTaskSpecFixture(t, root, "run-c", []string{"c/**"})
	fake := &fakeExecutor{failRunID: "run-a"}
	supervisor, _ := newSupervisor(t, root, fake)
	records, err := supervisor.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %+v, want 3 dispatch decisions", records)
	}
	failed := records[0]
	if failed.RunID != "run-a" || failed.Started || failed.Error == "" {
		t.Fatalf("run-a record = %+v, want failed start with non-empty error", failed)
	}
	if !strings.Contains(failed.Error, "fake executor") {
		t.Fatalf("run-a error = %q, want the executor failure recorded on the record", failed.Error)
	}
	for _, record := range records[1:] {
		if !record.Started || record.Error != "" {
			t.Fatalf("record %+v, want remaining actions dispatched normally despite the run-a failure", record)
		}
	}
	if len(fake.started) != 2 {
		t.Fatalf("executor started %d processes, want 2", len(fake.started))
	}
}

func TestLoopExitsCleanlyOnCancel(t *testing.T) {
	t.Run("already cancelled context", func(t *testing.T) {
		root := t.TempDir()
		seedRun(t, root, "run-loop", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
		fake := &fakeExecutor{}
		supervisor, _ := newSupervisor(t, root, fake)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := supervisor.Loop(ctx, time.Minute); err != nil {
			t.Fatalf("Loop with cancelled context = %v, want nil", err)
		}
		if fake.attempts != 0 {
			t.Fatalf("Loop supervised %d starts after cancellation, want none", fake.attempts)
		}
	})
	t.Run("cancel during first round", func(t *testing.T) {
		root := t.TempDir()
		seedRun(t, root, "run-loop", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fake := &fakeExecutor{onStart: cancel}
		supervisor, _ := newSupervisor(t, root, fake)
		if err := supervisor.Loop(ctx, time.Hour); err != nil {
			t.Fatalf("Loop = %v, want nil on cancellation", err)
		}
		if fake.attempts != 1 {
			t.Fatalf("Loop supervised %d starts, want exactly 1 before cancellation", fake.attempts)
		}
	})
	t.Run("non-positive interval rejected", func(t *testing.T) {
		supervisor, _ := newSupervisor(t, t.TempDir(), &fakeExecutor{})
		for _, interval := range []time.Duration{0, -time.Second} {
			if err := supervisor.Loop(context.Background(), interval); !errors.Is(err, ErrInvalidInterval) {
				t.Fatalf("Loop(interval=%v) = %v, want ErrInvalidInterval", interval, err)
			}
		}
	})
}

func TestSuperviseOrdersRecordsDeterministically(t *testing.T) {
	root := t.TempDir()
	for _, runID := range []string{"run-c", "run-a", "run-b"} {
		seedRun(t, root, runID, pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
		writeTaskSpecFixture(t, root, runID, []string{runID + "/**"})
	}
	fake := &fakeExecutor{}
	supervisor, _ := newSupervisor(t, root, fake)
	statuses, err := supervisor.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	wantIDs := []string{"run-a", "run-b", "run-c"}
	if len(statuses) != len(wantIDs) {
		t.Fatalf("Scan returned %d statuses, want %d", len(statuses), len(wantIDs))
	}
	for index, want := range wantIDs {
		if statuses[index].RunID != want {
			t.Fatalf("Scan order[%d] = %q, want %q", index, statuses[index].RunID, want)
		}
	}
	records, err := supervisor.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	if len(records) != len(wantIDs) {
		t.Fatalf("records = %+v, want %d entries", records, len(wantIDs))
	}
	for index, want := range wantIDs {
		if records[index].RunID != want {
			t.Fatalf("record order[%d] = %q, want %q", index, records[index].RunID, want)
		}
		if got := runIDFromArgv(fake.started[index]); got != want {
			t.Fatalf("start order[%d] targets %q, want %q", index, got, want)
		}
	}
}

// writeExcludeListFixture writes the supervise-exclude list at its fixed
// stateRoot-relative location (issue #100).
func writeExcludeListFixture(t *testing.T, stateRoot, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(stateRoot, ".marshal"), 0o700); err != nil {
		t.Fatalf("mkdir exclude list directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, excludeListRelativePath), []byte(content), 0o600); err != nil {
		t.Fatalf("write exclude list: %v", err)
	}
}

// writeTaskSpecFixture writes a minimal frozen task-spec.json carrying the
// scope.allowPaths write domain for one seeded Run.
func writeTaskSpecFixture(t *testing.T, stateRoot, runID string, allowPaths []string) {
	t.Helper()
	runDir := filepath.Join(stateRoot, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run directory for task spec: %v", err)
	}
	entries := make([]string, 0, len(allowPaths))
	for _, entry := range allowPaths {
		entries = append(entries, fmt.Sprintf("%q", entry))
	}
	spec := fmt.Sprintf(`{"apiVersion":"marshal.dev/v1alpha1","kind":"Task","metadata":{"id":%q,"title":"supervisor fixture"},"scope":{"allowPaths":[%s]}}`, fixtureTaskID, strings.Join(entries, ","))
	if err := os.WriteFile(filepath.Join(runDir, "task-spec.json"), []byte(spec), 0o600); err != nil {
		t.Fatalf("write task-spec.json for %s: %v", runID, err)
	}
}

func TestSuperviseExcludeListBlocksRedispatchOfDeadDrivers(t *testing.T) {
	root := t.TempDir()
	seedRun(t, root, "run-excluded-worker", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	seedRun(t, root, "run-excluded-publish", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StatePublishing), fixtureNow.Add(-fixtureStaleAge))
	seedRun(t, root, "run-dispatched", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	writeExcludeListFixture(t, root, "# excluded even though their drivers are dead\nrun-excluded-worker\nrun-excluded-publish\n")
	fake := &fakeExecutor{}
	supervisor, binary := newSupervisor(t, root, fake)
	records, err := supervisor.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %+v, want 3 decision records", records)
	}
	dispatched := records[0]
	if dispatched.RunID != "run-dispatched" || dispatched.Action != ActionRunWorker || !dispatched.Started || dispatched.SkipReason != "" || dispatched.Error != "" {
		t.Fatalf("unlisted record = %+v, want a clean run-worker dispatch", dispatched)
	}
	wantSkipped := []struct {
		runID  string
		action Action
	}{
		{"run-excluded-publish", ActionRetryPublish},
		{"run-excluded-worker", ActionRunWorker},
	}
	for index, want := range wantSkipped {
		record := records[index+1]
		if record.RunID != want.runID || record.Action != want.action || record.Started || record.Error != "" || record.SkipReason != SkipReasonExcluded {
			t.Fatalf("excluded record = %+v, want %s skipped via the exclude list", record, want.runID)
		}
	}
	wantArgv := []string{binary, "task", "run", "--run", "run-dispatched", "--through-verify", "--json"}
	if len(fake.started) != 1 || !reflect.DeepEqual(fake.started[0], wantArgv) {
		t.Fatalf("started argv = %v, want %v", fake.started, wantArgv)
	}
}

func TestSuperviseExcludeListMissingKeepsLegacyRedispatch(t *testing.T) {
	root := t.TempDir()
	seedRun(t, root, "run-ready", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	seedRun(t, root, "run-publishing-dead", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StatePublishing), fixtureNow.Add(-fixtureStaleAge))
	writeTaskSpecFixture(t, root, "run-ready", []string{"ready/**"})
	writeTaskSpecFixture(t, root, "run-publishing-dead", []string{"publishing/**"})
	fake := &fakeExecutor{}
	supervisor, _ := newSupervisor(t, root, fake)
	records, err := supervisor.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise without an exclude list: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v, want both dead-driver runs re-dispatched", records)
	}
	for _, record := range records {
		if !record.Started || record.Error != "" || record.SkipReason != "" {
			t.Fatalf("record = %+v, want a clean dispatch when no exclude list exists", record)
		}
	}
	if len(fake.started) != 2 {
		t.Fatalf("executor started %d processes, want 2", len(fake.started))
	}
}

func TestSuperviseExcludeListReadFailureFailsClosedRound(t *testing.T) {
	root := t.TempDir()
	seedRun(t, root, "run-ready", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	seedRun(t, root, "run-publishing-dead", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StatePublishing), fixtureNow.Add(-fixtureStaleAge))
	// A directory occupying the list path makes the read fail
	// deterministically regardless of the test user's permissions.
	if err := os.MkdirAll(filepath.Join(root, ".marshal", "supervise-exclude"), 0o700); err != nil {
		t.Fatalf("create directory at exclude list path: %v", err)
	}
	fake := &fakeExecutor{}
	supervisor, _ := newSupervisor(t, root, fake)
	records, err := supervisor.Supervise(context.Background())
	if !errors.Is(err, ErrExcludeListUnreadable) {
		t.Fatalf("Supervise with unreadable exclude list: err = %v, want ErrExcludeListUnreadable", err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %+v, want zero dispatch decisions", records)
	}
	if fake.attempts != 0 {
		t.Fatalf("executor attempts = %d, want zero re-dispatch on exclude list read failure", fake.attempts)
	}
}

func TestSuperviseRetryPendingDefaultSkipsAndOptInRevives(t *testing.T) {
	retryPath := func() []domain.State {
		return pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateRetryPending)
	}
	t.Run("default leaves retry pending undispatched", func(t *testing.T) {
		root := t.TempDir()
		seedRun(t, root, "run-retry", retryPath(), fixtureNow)
		fake := &fakeExecutor{}
		supervisor, _ := newSupervisor(t, root, fake)
		records, err := supervisor.Supervise(context.Background())
		if err != nil {
			t.Fatalf("Supervise: %v", err)
		}
		if len(records) != 0 {
			t.Fatalf("records = %+v, want RETRY_PENDING left alone by default", records)
		}
		if fake.attempts != 0 {
			t.Fatalf("executor attempts = %d, want none for RETRY_PENDING by default", fake.attempts)
		}
	})
	t.Run("opt-in flag revives retry pending", func(t *testing.T) {
		root := t.TempDir()
		seedRun(t, root, "run-retry", retryPath(), fixtureNow)
		fake := &fakeExecutor{}
		supervisor, binary := newSupervisor(t, root, fake, WithReviveRetryPending(true))
		records, err := supervisor.Supervise(context.Background())
		if err != nil {
			t.Fatalf("Supervise with revival opt-in: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("records = %+v, want the RETRY_PENDING run revived", records)
		}
		record := records[0]
		if record.RunID != "run-retry" || record.State != domain.StateRetryPending || record.Action != ActionRunWorker || !record.Started || record.SkipReason != "" || record.Error != "" {
			t.Fatalf("record = %+v, want a clean revival dispatch", record)
		}
		wantArgv := []string{binary, "task", "run", "--run", "run-retry", "--through-verify", "--json"}
		if len(fake.started) != 1 || !reflect.DeepEqual(fake.started[0], wantArgv) {
			t.Fatalf("started argv = %v, want %v", fake.started, wantArgv)
		}
	})
}

func TestSuperviseWriteDomainConflictSkipsCandidate(t *testing.T) {
	cases := []struct {
		name           string
		candidatePaths []string
		inflightPaths  []string
	}{
		{name: "overlap", candidatePaths: []string{"README.md", "src/app/main.go"}, inflightPaths: []string{"src/app/main.go"}},
		{name: "prefix", candidatePaths: []string{"src"}, inflightPaths: []string{"src/app/main.go"}},
		{name: "wildcard", candidatePaths: []string{"docs/**"}, inflightPaths: []string{"docs/guide.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			seedRun(t, root, "run-candidate", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
			seedRun(t, root, "run-inflight", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning), fixtureNow.Add(-time.Minute))
			writeTaskSpecFixture(t, root, "run-candidate", tc.candidatePaths)
			writeTaskSpecFixture(t, root, "run-inflight", tc.inflightPaths)
			fake := &fakeExecutor{}
			supervisor, _ := newSupervisor(t, root, fake)
			records, err := supervisor.Supervise(context.Background())
			if err != nil {
				t.Fatalf("Supervise: %v", err)
			}
			if len(records) != 1 {
				t.Fatalf("records = %+v, want exactly the skipped candidate", records)
			}
			record := records[0]
			if record.RunID != "run-candidate" || record.Started || record.Error != "" {
				t.Fatalf("record = %+v, want the conflicting candidate skipped", record)
			}
			if !strings.Contains(record.SkipReason, "write-domain conflict with in-flight run run-inflight") {
				t.Fatalf("SkipReason = %q, want the write-domain conflict with run-inflight", record.SkipReason)
			}
			if fake.attempts != 0 {
				t.Fatalf("executor attempts = %d, want none for a conflicting candidate", fake.attempts)
			}
		})
	}
}

func TestSuperviseWriteDomainNoConflictDispatches(t *testing.T) {
	root := t.TempDir()
	seedRun(t, root, "run-candidate", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	seedRun(t, root, "run-inflight", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning), fixtureNow.Add(-time.Minute))
	writeTaskSpecFixture(t, root, "run-candidate", []string{"src/**", "internal/tool.go"})
	writeTaskSpecFixture(t, root, "run-inflight", []string{"docs/guide.md", "tools/*"})
	fake := &fakeExecutor{}
	supervisor, binary := newSupervisor(t, root, fake)
	records, err := supervisor.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v, want the disjoint candidate dispatched", records)
	}
	record := records[0]
	if record.RunID != "run-candidate" || record.Action != ActionRunWorker || !record.Started || record.SkipReason != "" || record.Error != "" {
		t.Fatalf("record = %+v, want a clean dispatch for disjoint write domains", record)
	}
	wantArgv := []string{binary, "task", "run", "--run", "run-candidate", "--through-verify", "--json"}
	if len(fake.started) != 1 || !reflect.DeepEqual(fake.started[0], wantArgv) {
		t.Fatalf("started argv = %v, want %v", fake.started, wantArgv)
	}
}

func TestSuperviseWriteDomainFailsClosedOnUnreadableSpecs(t *testing.T) {
	assertFailClosedSkip := func(t *testing.T, records []DecisionRecord, fake *fakeExecutor) {
		t.Helper()
		if len(records) != 1 {
			t.Fatalf("records = %+v, want exactly the skipped candidate", records)
		}
		if records[0].Started || records[0].Error != "" || !strings.Contains(records[0].SkipReason, "failed closed") {
			t.Fatalf("record = %+v, want a fail-closed skip", records[0])
		}
		if fake.attempts != 0 {
			t.Fatalf("executor attempts = %d, want none for an undecidable candidate", fake.attempts)
		}
	}
	t.Run("candidate spec missing", func(t *testing.T) {
		root := t.TempDir()
		seedRun(t, root, "run-candidate", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
		seedRun(t, root, "run-inflight", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning), fixtureNow.Add(-time.Minute))
		writeTaskSpecFixture(t, root, "run-inflight", []string{"src/**"})
		fake := &fakeExecutor{}
		supervisor, _ := newSupervisor(t, root, fake)
		records, err := supervisor.Supervise(context.Background())
		if err != nil {
			t.Fatalf("Supervise: %v", err)
		}
		assertFailClosedSkip(t, records, fake)
	})
	t.Run("inflight spec missing", func(t *testing.T) {
		root := t.TempDir()
		seedRun(t, root, "run-candidate", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
		seedRun(t, root, "run-inflight", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning), fixtureNow.Add(-time.Minute))
		writeTaskSpecFixture(t, root, "run-candidate", []string{"src/**"})
		fake := &fakeExecutor{}
		supervisor, _ := newSupervisor(t, root, fake)
		records, err := supervisor.Supervise(context.Background())
		if err != nil {
			t.Fatalf("Supervise: %v", err)
		}
		assertFailClosedSkip(t, records, fake)
	})
}

func TestSuperviseWriteDomainIgnoresDeadAndTerminalRuns(t *testing.T) {
	root := t.TempDir()
	seedRun(t, root, "run-candidate", pathTo(domain.StatePlanned, domain.StateReady), fixtureNow)
	// Identical write domains everywhere, but the only other Runs have a
	// dead driver or reached a terminal state: neither is in-flight, so
	// nothing blocks the candidate.
	seedRun(t, root, "run-dead-driver", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning), fixtureNow.Add(-fixtureStaleAge))
	seedRun(t, root, "run-terminal", pathTo(domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StateAccepted), fixtureNow)
	writeTaskSpecFixture(t, root, "run-candidate", []string{"src/app/main.go"})
	writeTaskSpecFixture(t, root, "run-dead-driver", []string{"src/app/main.go"})
	writeTaskSpecFixture(t, root, "run-terminal", []string{"src/app/main.go"})
	fake := &fakeExecutor{}
	supervisor, binary := newSupervisor(t, root, fake)
	records, err := supervisor.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v, want candidate dispatched and overlapping orphan skipped", records)
	}
	record := records[0]
	if record.RunID != "run-candidate" || record.Action != ActionRunWorker || !record.Started || record.SkipReason != "" || record.Error != "" {
		t.Fatalf("record = %+v, want a clean dispatch when only dead or terminal runs share the domain", record)
	}
	wantArgv := []string{binary, "task", "run", "--run", "run-candidate", "--through-verify", "--json"}
	if len(fake.started) != 1 || !reflect.DeepEqual(fake.started[0], wantArgv) {
		t.Fatalf("started argv = %v, want %v", fake.started, wantArgv)
	}
	if records[1].RunID != "run-dead-driver" || records[1].Started || !strings.Contains(records[1].SkipReason, "write-domain conflict") {
		t.Fatalf("orphan overlap record = %+v, want fail-closed skip", records[1])
	}
}
