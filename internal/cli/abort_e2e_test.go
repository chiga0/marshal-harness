package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

const abortFixtureRemoteURL = "https://example.invalid/marshal-abort.git"

type abortFixture struct {
	repositoryRoot string
	stateRoot      string
	runDirectory   string
	worktreePath   string
	taskID         string
	runID          string
	baseSHA        string
	store          *runstore.Store
}

func newAbortFixture(t *testing.T, target domain.State) abortFixture {
	t.Helper()
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	repositoryRoot, err = filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	runGitCLI(t, repositoryRoot, "init", "-q", "-b", "main")
	runGitCLI(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGitCLI(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCLI(t, repositoryRoot, "add", "README.md")
	runGitCLI(t, repositoryRoot, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(runGitCLI(t, repositoryRoot, "rev-parse", "HEAD"))
	location, err := marshalRepository.Discover(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := location.Init(); err != nil {
		t.Fatal(err)
	}
	manager, err := gitworktree.Open(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(location.StateRoot, "abort-task", base)
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := worktree.Path
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repositoryRoot, "worktree", "remove", "--force", worktreePath).Run()
	})

	const (
		taskID = "abort-task"
		runID  = "abort-run-01"
	)
	store := runstore.New(location.StateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	var transitions [][2]domain.State
	switch target {
	case domain.StateRunning:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}}
	case domain.StateRetryPending:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateRetryPending}}
	default:
		transitions = nil
	}
	state := domain.NewRunState(taskID, runID, time.Unix(100, 0).UTC())
	for index, transition := range transitions {
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: fmt.Sprintf("event:%d", index+1),
			RunID: runID, Sequence: uint64(index + 1), Type: "run.transition", StateFrom: transition[0],
			StateTo: transition[1], Timestamp: time.Unix(int64(101+index), 0).UTC(), Payload: map[string]any{},
		}
		if transition[1] == domain.StateRunning {
			event.AttemptID = "attempt:1"
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
	}
	state.State, state.Sequence = target, uint64(len(transitions))
	if target != domain.StateCreated {
		state.CurrentAttemptID, state.AttemptsUsed = "attempt:1", 1
	}
	state.BaseSHA, state.WorktreePath = base, worktreePath
	runDirectory := filepath.Join(location.StateRoot, "runs", runID)
	if target == domain.StateRetryPending {
		state.SpecDigest = writeAbortFrozenInput(t, runDirectory, "task-spec.json", cliPlanningTask(t, repositoryRoot, taskID, abortFixtureRemoteURL))
		state.PolicyDigest = writeAbortFrozenInput(t, runDirectory, "policy-snapshot.json", cliPlanningPolicy(t, taskID, runID))
		state.CapabilityDigest = writeAbortFrozenInput(t, runDirectory, "capability-snapshot.json", readCLIFixture(t, "examples/happy-path/capability-snapshot.json"))
	}
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")
	return abortFixture{repositoryRoot: repositoryRoot, stateRoot: location.StateRoot, runDirectory: runDirectory, worktreePath: worktreePath, taskID: taskID, runID: runID, baseSHA: base, store: store}
}

func writeAbortFrozenInput(t *testing.T, runDirectory, name string, value map[string]any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestTaskAbortTerminatesRetryPendingRun(t *testing.T) {
	fixture := newAbortFixture(t, domain.StateRetryPending)
	const reason = "run abandoned after strategy change"
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"task", "abort", "--run", fixture.runID, "--actor", "op:1", "--reason", reason, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("abort exit = %d, stderr = %s", exit, stderr.String())
	}
	var result struct {
		Status         string       `json:"status"`
		RunID          string       `json:"runId"`
		State          domain.State `json:"state"`
		TerminalReason string       `json:"terminalReason"`
		Actor          string       `json:"actor"`
		Sequence       uint64       `json:"sequence"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode abort output: %v", err)
	}
	if result.Status != "aborted" || result.RunID != fixture.runID || result.State != domain.StateBlocked || result.TerminalReason != lifecycle.AbortTerminalReason || result.Actor != "op:1" || result.Sequence != 5 {
		t.Fatalf("abort result = %+v", result)
	}

	state, err := fixture.store.Inspect(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != domain.StateBlocked || state.TerminalReason != lifecycle.AbortTerminalReason || state.Sequence != 5 {
		t.Fatalf("aborted state = %+v", state)
	}
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil || len(events) != 5 {
		t.Fatalf("journal events = %d, err = %v", len(events), err)
	}
	last := events[len(events)-1]
	if last.Type != lifecycle.AbortEventType || last.StateFrom != domain.StateRetryPending || last.StateTo != domain.StateBlocked {
		t.Fatalf("abort event = %+v", last)
	}
	if last.Actor == nil || last.Actor.Type != domain.ControlSourceTypeHuman || last.Actor.ID != "op:1" {
		t.Fatalf("abort actor = %+v", last.Actor)
	}
	if last.Payload["terminalReason"] != lifecycle.AbortTerminalReason || last.Payload["reason"] != reason {
		t.Fatalf("abort payload = %+v", last.Payload)
	}

	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	outcomeData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "outcome.json"))
	if err != nil {
		t.Fatalf("outcome missing: %v", err)
	}
	if err := validator.Validate(domain.KindOutcome, outcomeData); err != nil {
		t.Fatalf("outcome invalid: %v", err)
	}
	var outcome domain.OutcomeBundle
	if err := json.Unmarshal(outcomeData, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != domain.StateBlocked || outcome.Verdict != "abort" || outcome.Summary != reason || outcome.TaskID != fixture.taskID || outcome.RunID != fixture.runID || outcome.FinalReviewRound < 1 || outcome.FindingCount != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.FinalReviewDigest == "" || outcome.FinalEvidenceDigest == "" {
		t.Fatalf("outcome lacks evidence binding: %+v", outcome)
	}
	for _, name := range []string{"outcome.md", "result.md"} {
		info, err := os.Stat(filepath.Join(fixture.runDirectory, name))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("terminal record %s missing: %v", name, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(fixture.worktreePath, "README.md"))
	if err != nil || string(data) != "base\n" {
		t.Fatalf("abort touched the worktree: %v %q", err, string(data))
	}

	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "abort", "--run", fixture.runID, "--actor", "op:1", "--reason", "second attempt"}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "终态") {
		t.Fatalf("repeat abort exit = %d, stderr = %q", exit, stderr.String())
	}
	repeatEvents, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil || len(repeatEvents) != 5 {
		t.Fatalf("repeat abort modified journal: %d events, err = %v", len(repeatEvents), err)
	}
	repeatOutcome, err := os.ReadFile(filepath.Join(fixture.runDirectory, "outcome.json"))
	if err != nil || !bytes.Equal(repeatOutcome, outcomeData) {
		t.Fatalf("repeat abort modified outcome: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"doctor", "--run", fixture.runID, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("doctor exit = %d, stderr = %s", exit, stderr.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || report.Run == nil || report.Run.Status != "ok" || len(report.Run.Findings) != 0 {
		t.Fatalf("doctor report = %+v", report)
	}
	if report.Run.State == nil || report.Run.State.State != domain.StateBlocked || report.Run.State.TerminalReason != lifecycle.AbortTerminalReason {
		t.Fatalf("doctor run state = %+v", report.Run.State)
	}
	if report.Run.SnapshotSequence != 5 || report.Run.JournalSequence != 5 {
		t.Fatalf("doctor sequences = %d/%d", report.Run.SnapshotSequence, report.Run.JournalSequence)
	}

	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "cleanup", "--run", fixture.runID, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("cleanup preview exit = %d, stderr = %s", exit, stderr.String())
	}
	var preview struct {
		RunID   string `json:"runId"`
		Applied bool   `json:"applied"`
		Targets []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Applied || len(preview.Targets) != 1 || preview.Targets[0].Kind != "managed-worktree" || preview.Targets[0].Path != fixture.worktreePath {
		t.Fatalf("cleanup preview = %+v", preview)
	}

	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "cleanup", "--run", fixture.runID, "--apply", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("cleanup apply exit = %d, stderr = %s", exit, stderr.String())
	}
	if _, err := os.Lstat(fixture.worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after cleanup: %v", err)
	}
}

func TestTaskAbortRejectsOtherStates(t *testing.T) {
	t.Run("created run", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateCreated)
		var stdout, stderr bytes.Buffer
		if exit := Run([]string{"task", "abort", "--run", fixture.runID, "--actor", "op:1", "--reason", "abandoned"}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "仅允许从 RETRY_PENDING") {
			t.Fatalf("abort exit = %d, stderr = %q", exit, stderr.String())
		}
		state, err := fixture.store.Inspect(fixture.runID)
		if err != nil || state.State != domain.StateCreated || state.Sequence != 0 {
			t.Fatalf("state mutated: %+v, err = %v", state, err)
		}
		for _, name := range []string{"events.jsonl", "outcome.json", "result.md"} {
			if _, err := os.Stat(filepath.Join(fixture.runDirectory, name)); !os.IsNotExist(err) {
				t.Fatalf("rejected abort wrote %s: %v", name, err)
			}
		}
	})
	t.Run("running run", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateRunning)
		var stdout, stderr bytes.Buffer
		if exit := Run([]string{"task", "abort", "--run", fixture.runID, "--actor", "op:1", "--reason", "abandoned"}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "仅允许从 RETRY_PENDING") {
			t.Fatalf("abort exit = %d, stderr = %q", exit, stderr.String())
		}
		events, _, err := fixture.store.ReadEvents(fixture.runID)
		if err != nil || len(events) != 3 {
			t.Fatalf("rejected abort modified journal: %d events, err = %v", len(events), err)
		}
		state, err := fixture.store.Inspect(fixture.runID)
		if err != nil || state.State != domain.StateRunning {
			t.Fatalf("state mutated: %+v, err = %v", state, err)
		}
	})
}

func TestTaskAbortUsageAndIdentityValidation(t *testing.T) {
	for name, args := range map[string][]string{
		"none":           {"task", "abort"},
		"missing run":    {"task", "abort", "--actor", "op:1", "--reason", "r"},
		"missing actor":  {"task", "abort", "--run", "abort-run", "--reason", "r"},
		"missing reason": {"task", "abort", "--run", "abort-run", "--actor", "op:1"},
		"blank reason":   {"task", "abort", "--run", "abort-run", "--actor", "op:1", "--reason", "   "},
		"blank actor":    {"task", "abort", "--run", "abort-run", "--actor", "  ", "--reason", "r"},
		"positional":     {"task", "abort", "--run", "abort-run", "--actor", "op:1", "--reason", "r", "extra"},
		"invalid run id": {"task", "abort", "--run", "../escape", "--actor", "op:1", "--reason", "r"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := Run(args, strings.NewReader(""), &stdout, &stderr)
			if exit != ExitUsage {
				t.Fatalf("Run(%v) exit = %d, want %d; stderr = %q", args, exit, ExitUsage, stderr.String())
			}
			if name == "invalid run id" {
				if stderr.String() != "终止失败：Run ID、操作者或原因无效。\n" {
					t.Fatalf("stderr = %q", stderr.String())
				}
				return
			}
			if !strings.Contains(stderr.String(), "marshal task abort --run RUN_ID --actor ID --reason TEXT") {
				t.Fatalf("usage missing from stderr: %q", stderr.String())
			}
		})
	}
}
