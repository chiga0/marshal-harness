package cli

import (
	"bytes"
	"encoding/json"
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

const superviseFixtureTaskID = "task-supervise-cli-fixture"

// superviseFixtureBinary writes a fake marshal binary (a 0700 shell script)
// that records the argv it receives — one argument per line, excluding
// argv[0] — into argvFile. The write is atomic (rename) so polling readers
// never observe a partial record.
func superviseFixtureBinary(t *testing.T, argvFile string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "marshal-supervise-fixture")
	script := "#!/bin/sh\n" +
		"{ printf '%s\\n' \"$@\"; } > \"" + argvFile + ".tmp\"\n" +
		"mv \"" + argvFile + ".tmp\" \"" + argvFile + "\"\n" +
		"run_id=''\n" +
		"previous=''\n" +
		"for argument in \"$@\"; do if [ \"$previous\" = '--run' ]; then run_id=\"$argument\"; break; fi; previous=\"$argument\"; done\n" +
		"MARSHAL_CLI_HELPER_STATE_ROOT=\"$PWD/.marshal\" MARSHAL_CLI_HELPER_RUN_ID=\"$run_id\" MARSHAL_CLI_HELPER_RELEASE=\"$PWD/.marshal/.supervise-fixture-release-$run_id\" exec \"" + os.Args[0] + "\" -test.run '^TestCLISuperviseLeaseHelper$'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLISuperviseLeaseHelper(t *testing.T) {
	root, runID, release := os.Getenv("MARSHAL_CLI_HELPER_STATE_ROOT"), os.Getenv("MARSHAL_CLI_HELPER_RUN_ID"), os.Getenv("MARSHAL_CLI_HELPER_RELEASE")
	if root == "" || runID == "" || release == "" {
		return
	}
	lease, err := runstore.New(root).Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(release); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("probe supervise fixture release: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("supervise fixture release was not acknowledged")
}

// newSuperviseRepository prepares a hermetic git repository with initialised
// Marshal state, chdirs into it for the remainder of the test and returns
// the repository root and state root.
func newSuperviseRepository(t *testing.T) (string, string) {
	t.Helper()
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	runGit(t, repositoryRoot, "init", "-q")
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}
	return repositoryRoot, filepath.Join(repositoryRoot, ".marshal")
}

// superviseTransitionEventType maps a lifecycle transition target to the
// journal event type the lifecycle reducer accepts for it.
func superviseTransitionEventType(to domain.State) string {
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

// seedSuperviseRun builds a real runstore fixture under stateRoot whose
// journal walks the lifecycle from CREATED through path, with every journal
// event stamped at lastEventAt so driver staleness is deterministic.
func seedSuperviseRun(t *testing.T, stateRoot, runID string, path []domain.State, lastEventAt time.Time) {
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
	snapshot := domain.NewRunState(superviseFixtureTaskID, runID, lastEventAt)
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
			Type:       superviseTransitionEventType(to),
			StateFrom:  from,
			StateTo:    to,
			Timestamp:  lastEventAt,
			Payload:    map[string]any{"fixture": "supervise-cli-test"},
		}
		// 真实 journal 中 worker.* 事件恒带 attemptId；single recovery
		// model（ADR 0053 决策 5）装配要求非空。
		if to == domain.StateRunning || to == domain.StateRetryPending {
			event.AttemptID = "attempt-" + runID
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatalf("append event %d for %s: %v", index+1, runID, err)
		}
		from = to
	}
}

// waitForArgvFile polls until the fake marshal binary has recorded its argv
// and returns one entry per received argument.
func waitForArgvFile(t *testing.T, path string) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			lines := []string{}
			for _, line := range strings.Split(string(data), "\n") {
				if line != "" {
					lines = append(lines, line)
				}
			}
			return lines
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fake marshal binary never recorded argv at %s", path)
	return nil
}

// waitForLeaseReleased proves that the detached fake child reached the
// readiness point observed by the supervisor and then finished releasing its
// Run lease. Tests must not return while that child can still write below a
// t.TempDir-backed state root, otherwise cleanup can race with the lease owner.
func waitForLeaseReleased(t *testing.T, stateRoot, runID string) {
	t.Helper()
	store := runstore.New(stateRoot)
	held, err := store.LeaseHeld(runID)
	if err != nil {
		t.Fatalf("probe lease for %s: %v", runID, err)
	}
	if !held {
		t.Fatalf("lease for %s was not held after detached driver readiness", runID)
	}
	release := filepath.Join(stateRoot, ".supervise-fixture-release-"+runID)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release supervise fixture for %s: %v", runID, err)
	}
	t.Cleanup(func() { _ = os.Remove(release) })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		held, err = store.LeaseHeld(runID)
		if err != nil {
			t.Fatalf("probe lease release for %s: %v", runID, err)
		}
		if !held {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("detached fake child did not release lease for %s", runID)
}

// assertNoArgvFile fails the test if the fake marshal binary records argv
// within a short grace period after the supervise round returned.
func assertNoArgvFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("fake marshal binary unexpectedly recorded argv at %s", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestSuperviseOnceDispatchesReadyRunToWorker(t *testing.T) {
	_, stateRoot := newSuperviseRepository(t)
	const runID = "run-supervise-ready"
	seedSuperviseRun(t, stateRoot, runID, []domain.State{domain.StatePlanned, domain.StateReady}, time.Now().UTC())
	argvFile := filepath.Join(t.TempDir(), "argv-record")
	binary := superviseFixtureBinary(t, argvFile)

	var stdout, stderr bytes.Buffer
	exit := Run([]string{"supervise", "--once", "--marshal-binary", binary}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("supervise --once exit = %d, stderr = %s", exit, stderr.String())
	}
	waitForLeaseReleased(t, stateRoot, runID)
	gotArgv := waitForArgvFile(t, argvFile)
	wantArgv := []string{"task", "run", "--run", runID, "--through-verify", "--json"}
	if !reflect.DeepEqual(gotArgv, wantArgv) {
		t.Fatalf("fake binary argv = %v, want %v", gotArgv, wantArgv)
	}
	output := stdout.String()
	if !strings.Contains(output, runID) || !strings.Contains(output, "run-worker") {
		t.Fatalf("stdout missing run-worker decision record: %s", output)
	}
}

func TestSuperviseOnceRetriesPublishForDeadDriver(t *testing.T) {
	_, stateRoot := newSuperviseRepository(t)
	const runID = "run-supervise-publishing"
	publishingPath := []domain.State{
		domain.StatePlanned, domain.StateReady, domain.StateRunning,
		domain.StateVerifying, domain.StateReviewPending, domain.StatePublishing,
	}
	// Two hours old exceeds the 30 minute staleness threshold, so the
	// PUBLISHING driver is provably dead.
	seedSuperviseRun(t, stateRoot, runID, publishingPath, time.Now().UTC().Add(-2*time.Hour))
	argvFile := filepath.Join(t.TempDir(), "argv-record")
	binary := superviseFixtureBinary(t, argvFile)

	var stdout, stderr bytes.Buffer
	exit := Run([]string{"supervise", "--once", "--marshal-binary", binary}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("supervise --once exit = %d, stderr = %s", exit, stderr.String())
	}
	waitForLeaseReleased(t, stateRoot, runID)
	gotArgv := waitForArgvFile(t, argvFile)
	wantArgv := []string{"task", "publish", "--run", runID, "--json"}
	if !reflect.DeepEqual(gotArgv, wantArgv) {
		t.Fatalf("fake binary argv = %v, want %v", gotArgv, wantArgv)
	}
	output := stdout.String()
	if !strings.Contains(output, runID) || !strings.Contains(output, "retry-publish") {
		t.Fatalf("stdout missing retry-publish decision record: %s", output)
	}
}

func TestSuperviseOnceReturnsDeadRunningRunToCore(t *testing.T) {
	_, stateRoot := newSuperviseRepository(t)
	const runID = "run-supervise-orphan"
	seedSuperviseRun(t, stateRoot, runID, []domain.State{
		domain.StatePlanned, domain.StateReady, domain.StateRunning,
	}, time.Now().UTC().Add(-2*time.Hour))
	argvFile := filepath.Join(t.TempDir(), "argv-record")
	binary := superviseFixtureBinary(t, argvFile)

	var stdout, stderr bytes.Buffer
	exit := Run([]string{"supervise", "--once", "--marshal-binary", binary}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("supervise --once exit = %d, stderr = %s", exit, stderr.String())
	}
	waitForLeaseReleased(t, stateRoot, runID)
	gotArgv := waitForArgvFile(t, argvFile)
	wantArgv := []string{"task", "run", "--run", runID, "--through-verify", "--recover-dead-driver", "--json"}
	if !reflect.DeepEqual(gotArgv, wantArgv) {
		t.Fatalf("fake binary argv = %v, want %v", gotArgv, wantArgv)
	}
	if output := stdout.String(); !strings.Contains(output, runID) || !strings.Contains(output, "run-worker") {
		t.Fatalf("stdout missing orphan run-worker decision record: %s", output)
	}
}

func TestSuperviseOnceLeavesTerminalAndAliveRunsAlone(t *testing.T) {
	_, stateRoot := newSuperviseRepository(t)
	now := time.Now().UTC()
	terminalPath := []domain.State{
		domain.StatePlanned, domain.StateReady, domain.StateRunning,
		domain.StateVerifying, domain.StateReviewPending, domain.StateAccepted,
	}
	seedSuperviseRun(t, stateRoot, "run-supervise-accepted", terminalPath, now)
	alivePath := []domain.State{domain.StatePlanned, domain.StateReady, domain.StateRunning}
	seedSuperviseRun(t, stateRoot, "run-supervise-alive", alivePath, now)
	argvFile := filepath.Join(t.TempDir(), "argv-record")
	binary := superviseFixtureBinary(t, argvFile)

	var stdout, stderr bytes.Buffer
	exit := Run([]string{"supervise", "--once", "--marshal-binary", binary}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("supervise --once exit = %d, stderr = %s", exit, stderr.String())
	}
	assertNoArgvFile(t, argvFile)
	if !strings.Contains(stdout.String(), "无可监督的 Run。") {
		t.Fatalf("stdout = %q, want the no-op supervise message", stdout.String())
	}
}

func TestSuperviseOnceWithoutSupervisableRuns(t *testing.T) {
	newSuperviseRepository(t)
	argvFile := filepath.Join(t.TempDir(), "argv-record")
	binary := superviseFixtureBinary(t, argvFile)

	var stdout, stderr bytes.Buffer
	exit := Run([]string{"supervise", "--once", "--marshal-binary", binary}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("supervise --once exit = %d, stderr = %s", exit, stderr.String())
	}
	assertNoArgvFile(t, argvFile)
	if !strings.Contains(stdout.String(), "无可监督的 Run。") {
		t.Fatalf("stdout = %q, want the no-op supervise message", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	exit = Run([]string{"supervise", "--once", "--json", "--marshal-binary", binary}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("supervise --once --json exit = %d, stderr = %s", exit, stderr.String())
	}
	var decisions []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decisions); err != nil {
		t.Fatalf("decode supervise JSON: %v\n%s", err, stdout.String())
	}
	if len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want empty list", decisions)
	}
}

func TestSuperviseRejectsInvalidIntervalAndPositionalArguments(t *testing.T) {
	newSuperviseRepository(t)
	for _, args := range [][]string{
		{"supervise", "--interval", "0s"},
		{"supervise", "--interval=-30s"},
		{"supervise", "--once", "--interval", "0s"},
		{"supervise", "--interval=not-a-duration"},
		{"supervise", "extra-positional"},
	} {
		var stdout, stderr bytes.Buffer
		exit := Run(args, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitUsage {
			t.Fatalf("Run(%v) exit = %d, want %d; stderr = %s", args, exit, ExitUsage, stderr.String())
		}
	}
}

func TestSuperviseOnceJSONCarriesCompleteDecisionFields(t *testing.T) {
	_, stateRoot := newSuperviseRepository(t)
	const runID = "run-supervise-json"
	seedSuperviseRun(t, stateRoot, runID, []domain.State{domain.StatePlanned, domain.StateReady}, time.Now().UTC())
	argvFile := filepath.Join(t.TempDir(), "argv-record")
	binary := superviseFixtureBinary(t, argvFile)

	var stdout, stderr bytes.Buffer
	exit := Run([]string{"supervise", "--once", "--json", "--marshal-binary", binary}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("supervise --once --json exit = %d, stderr = %s", exit, stderr.String())
	}
	waitForLeaseReleased(t, stateRoot, runID)
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("decode supervise JSON: %v\n%s", err, stdout.String())
	}
	if len(raw) != 1 {
		t.Fatalf("decisions = %d, want 1: %s", len(raw), stdout.String())
	}
	for _, field := range []string{"runId", "state", "action", "started"} {
		if _, ok := raw[0][field]; !ok {
			t.Fatalf("decision JSON missing field %q: %s", field, stdout.String())
		}
	}
	var decisions []struct {
		RunID   string `json:"runId"`
		State   string `json:"state"`
		Action  string `json:"action"`
		Started bool   `json:"started"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decisions); err != nil {
		t.Fatalf("decode supervise decisions: %v\n%s", err, stdout.String())
	}
	decision := decisions[0]
	if decision.RunID != runID || decision.State != string(domain.StateReady) || decision.Action != "run-worker" || !decision.Started || decision.Error != "" {
		t.Fatalf("decision = %+v, want READY run-worker started without error", decision)
	}
	// The JSON record describes a real spawn; make sure the child process
	// actually received the worker argv.
	gotArgv := waitForArgvFile(t, argvFile)
	wantArgv := []string{"task", "run", "--run", runID, "--through-verify", "--json"}
	if !reflect.DeepEqual(gotArgv, wantArgv) {
		t.Fatalf("fake binary argv = %v, want %v", gotArgv, wantArgv)
	}
}

// TestSuperviseOnceRecoversAfterRealProcessKill 验证真实进程 kill 中段注入
// 的端到端恢复（R6-TOP1）：一个 RUNNING run 的 lease 被真实获取并释放（模拟
// driver 被 kill 后 lease 释放），加上时间戳超过 staleness 阈值后，
// supervise --once 经 lease owner 死活探针判定 driver 死亡并接管恢复。
//
// 与 TestSuperviseOnceReturnsDeadRunningRunToCore 的区别：后者仅 seed
// 陈旧 journal 不获取 lease；此测试获取真实 lease 再释放，证明 lease
// 生命周期与恢复路径的正确交互。
func TestSuperviseOnceRecoversAfterRealProcessKill(t *testing.T) {
	_, stateRoot := newSuperviseRepository(t)
	const runID = "run-supervise-real-kill"
	// seed 一个 RUNNING run，时间戳是 2 小时前（超过 staleness 阈值）。
	seedSuperviseRun(t, stateRoot, runID, []domain.State{
		domain.StatePlanned, domain.StateReady, domain.StateRunning,
	}, time.Now().UTC().Add(-2*time.Hour))

	// 获取 lease，模拟 driver 曾经持有 lease。
	store := runstore.New(stateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if held, _ := store.LeaseHeld(runID); !held {
		t.Fatal("lease not held after acquire")
	}
	// 释放 lease 模拟 driver 被 kill 后 lease 释放。
	lease.Release()
	// 确认 lease 已释放。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		held, _ := store.LeaseHeld(runID)
		if !held {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if held, _ := store.LeaseHeld(runID); held {
		t.Fatal("lease still held after release")
	}

	argvFile := filepath.Join(t.TempDir(), "argv-record")
	binary := superviseFixtureBinary(t, argvFile)

	var stdout, stderr bytes.Buffer
	exit := Run([]string{"supervise", "--once", "--marshal-binary", binary}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("supervise --once exit = %d, stderr = %s", exit, stderr.String())
	}
	waitForLeaseReleased(t, stateRoot, runID)
	// 恢复路径应派发 --recover-dead-driver。
	gotArgv := waitForArgvFile(t, argvFile)
	wantArgv := []string{"task", "run", "--run", runID, "--through-verify", "--recover-dead-driver", "--json"}
	if !reflect.DeepEqual(gotArgv, wantArgv) {
		t.Fatalf("fake binary argv = %v, want %v (recover-dead-driver)", gotArgv, wantArgv)
	}
	output := stdout.String()
	if !strings.Contains(output, runID) || !strings.Contains(output, "run-worker") {
		t.Fatalf("stdout missing orphan run-worker decision record: %s", output)
	}
}
