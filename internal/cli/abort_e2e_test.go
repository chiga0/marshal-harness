package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/review"
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
	sequence       uint64
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
	case domain.StatePlanned:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}}
	case domain.StateReady:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}}
	case domain.StateRunning:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}}
	case domain.StateRetryPending:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateRetryPending}}
	case domain.StateVerifying:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateVerifying}}
	case domain.StateReworkRequested:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateVerifying}, {domain.StateVerifying, domain.StateReviewPending}, {domain.StateReviewPending, domain.StateReworkRequested}}
	case domain.StatePublishing:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateVerifying}, {domain.StateVerifying, domain.StateReviewPending}, {domain.StateReviewPending, domain.StatePublishing}}
	case domain.StatePublished:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateVerifying}, {domain.StateVerifying, domain.StateReviewPending}, {domain.StateReviewPending, domain.StatePublishing}, {domain.StatePublishing, domain.StatePublished}}
	case domain.StateCIPending:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateVerifying}, {domain.StateVerifying, domain.StateReviewPending}, {domain.StateReviewPending, domain.StatePublishing}, {domain.StatePublishing, domain.StatePublished}, {domain.StatePublished, domain.StateCIPending}}
	case domain.StateAccepted:
		transitions = [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateVerifying}, {domain.StateVerifying, domain.StateReviewPending}, {domain.StateReviewPending, domain.StateAccepted}}
	default:
		transitions = nil
	}
	state := domain.NewRunState(taskID, runID, time.Unix(100, 0).UTC())
	enteredRunning := false
	for index, transition := range transitions {
		if transition[1] == domain.StateRunning {
			enteredRunning = true
		}
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
	if enteredRunning {
		state.CurrentAttemptID, state.AttemptsUsed = "attempt:1", 1
	}
	state.BaseSHA, state.WorktreePath = base, worktreePath
	runDirectory := filepath.Join(location.StateRoot, "runs", runID)
	if target != domain.StateCreated {
		state.SpecDigest = writeAbortFrozenInput(t, runDirectory, "task-spec.json", cliPlanningTask(t, repositoryRoot, taskID, abortFixtureRemoteURL))
		state.PolicyDigest = writeAbortFrozenInput(t, runDirectory, "policy-snapshot.json", cliPlanningPolicy(t, taskID, runID))
		capabilityDigest := writeAbortFrozenInput(t, runDirectory, "capability-snapshot.json", readCLIFixture(t, "examples/happy-path/capability-snapshot.json"))
		if target != domain.StatePlanned {
			// A PLANNED run has not frozen the capability identity yet; the
			// file exists (planning writes it before the PLANNED event) but
			// the snapshot field stays an explicit absence.
			state.CapabilityDigest = capabilityDigest
		}
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
	return abortFixture{repositoryRoot: repositoryRoot, stateRoot: location.StateRoot, runDirectory: runDirectory, worktreePath: worktreePath, taskID: taskID, runID: runID, baseSHA: base, sequence: uint64(len(transitions)), store: store}
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

func readAbortFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func runAbortCommand(runID, actor, reason string, jsonOutput bool, stdout, stderr *bytes.Buffer) int {
	args := []string{"task", "abort", "--run", runID, "--actor", actor, "--reason", reason}
	if jsonOutput {
		args = append(args, "--json")
	}
	return Run(args, strings.NewReader(""), stdout, stderr)
}

type abortJSONResult struct {
	Status         string       `json:"status"`
	RunID          string       `json:"runId"`
	State          domain.State `json:"state"`
	TerminalReason string       `json:"terminalReason"`
	Actor          string       `json:"actor"`
	Sequence       uint64       `json:"sequence"`
}

func TestTaskAbortTerminatesRetryPendingRun(t *testing.T) {
	fixture := newAbortFixture(t, domain.StateRetryPending)
	const reason = "run abandoned after strategy change"
	var stdout, stderr bytes.Buffer
	exit := runAbortCommand(fixture.runID, "op:1", reason, true, &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("abort exit = %d, stderr = %s", exit, stderr.String())
	}
	var result abortJSONResult
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
	if last.AttemptID != "attempt:1" {
		t.Fatalf("RETRY_PENDING abort must preserve the attempt identity: %+v", last)
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

	// An identical repeat request is idempotent: it returns the existing
	// terminal result and never appends or rewrites evidence.
	stdout.Reset()
	stderr.Reset()
	if exit := runAbortCommand(fixture.runID, "op:1", reason, true, &stdout, &stderr); exit != ExitOK {
		t.Fatalf("idempotent repeat abort exit = %d, stderr = %s", exit, stderr.String())
	}
	var repeatResult abortJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &repeatResult); err != nil {
		t.Fatalf("decode repeat abort output: %v", err)
	}
	if repeatResult.Status != "aborted" || repeatResult.State != domain.StateBlocked || repeatResult.TerminalReason != lifecycle.AbortTerminalReason || repeatResult.Actor != "op:1" || repeatResult.Sequence != 5 {
		t.Fatalf("idempotent repeat result = %+v", repeatResult)
	}
	repeatEvents, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil || len(repeatEvents) != 5 {
		t.Fatalf("repeat abort modified journal: %d events, err = %v", len(repeatEvents), err)
	}
	repeatOutcome, err := os.ReadFile(filepath.Join(fixture.runDirectory, "outcome.json"))
	if err != nil || !bytes.Equal(repeatOutcome, outcomeData) {
		t.Fatalf("repeat abort modified outcome: %v", err)
	}

	// A divergent request against the same abort-closed slot is a
	// deterministic conflict with zero writes.
	stdout.Reset()
	stderr.Reset()
	if exit := runAbortCommand(fixture.runID, "op:1", "second attempt", false, &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "终态") {
		t.Fatalf("conflicting repeat abort exit = %d, stderr = %q", exit, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exit := runAbortCommand(fixture.runID, "op:2", reason, false, &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "终态") {
		t.Fatalf("foreign-actor repeat abort exit = %d, stderr = %q", exit, stderr.String())
	}
	conflictEvents, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil || len(conflictEvents) != 5 {
		t.Fatalf("conflicting repeat abort modified journal: %d events, err = %v", len(conflictEvents), err)
	}
	conflictOutcome, err := os.ReadFile(filepath.Join(fixture.runDirectory, "outcome.json"))
	if err != nil || !bytes.Equal(conflictOutcome, outcomeData) {
		t.Fatalf("conflicting repeat abort modified outcome: %v", err)
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

// assertPreAttemptAbortEvidence verifies the frozen ADR 0029 evidence chain
// of one successful pre-attempt abort against the authoritative storage.
func assertPreAttemptAbortEvidence(t *testing.T, fixture abortFixture, source domain.State, reason string, journalPrefix []byte) {
	t.Helper()
	state, err := fixture.store.Inspect(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != domain.StateAborted || state.TerminalReason != lifecycle.PreAttemptAbortTerminalReason || state.Sequence != fixture.sequence+1 {
		t.Fatalf("pre-attempt abort state = %+v", state)
	}
	if state.CurrentAttemptID != "" || state.AttemptsUsed != 0 {
		t.Fatalf("pre-attempt abort invented attempt identity: %+v", state)
	}
	if state.SpecDigest == "" || state.PolicyDigest == "" {
		t.Fatalf("pre-attempt abort dropped frozen digests: %+v", state)
	}
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil || uint64(len(events)) != fixture.sequence+1 {
		t.Fatalf("journal events = %d, err = %v", len(events), err)
	}
	last := events[len(events)-1]
	if last.Type != lifecycle.AbortEventType || last.StateFrom != source || last.StateTo != domain.StateAborted || last.AttemptID != "" {
		t.Fatalf("pre-attempt abort event = %+v", last)
	}
	if last.Actor == nil || last.Actor.Type != domain.ControlSourceTypeHuman || last.Actor.ID != "op:1" {
		t.Fatalf("pre-attempt abort actor = %+v", last.Actor)
	}
	if last.Payload["terminalReason"] != lifecycle.PreAttemptAbortTerminalReason || last.Payload["reason"] != reason || len(last.Payload) != 2 {
		t.Fatalf("pre-attempt abort payload = %+v", last.Payload)
	}
	// The journal is append-only byte for byte: the pre-abort prefix survives
	// unchanged and exactly one line was appended.
	journalData := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "events.jsonl"))
	if len(journalData) <= len(journalPrefix) || !bytes.Equal(journalData[:len(journalPrefix)], journalPrefix) {
		t.Fatalf("pre-attempt abort rewrote existing journal bytes")
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
	payloadData, err := json.Marshal(map[string]any{"terminalReason": lifecycle.PreAttemptAbortTerminalReason, "reason": reason})
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := canonical.DigestJSON(payloadData)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != domain.StateAborted || outcome.Verdict != "abort" || outcome.Summary != reason || outcome.TaskID != fixture.taskID || outcome.RunID != fixture.runID || outcome.FindingCount != 0 || outcome.FinalReviewRound < 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.FinalReviewDigest != wantDigest || outcome.FinalEvidenceDigest != wantDigest {
		t.Fatalf("outcome evidence digest not bound to the abort payload: %+v", outcome)
	}
	resultData := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "result.md"))
	if !strings.Contains(string(resultData), string(domain.StateAborted)) || !strings.Contains(string(resultData), lifecycle.PreAttemptAbortTerminalReason) {
		t.Fatalf("result.md does not record the ABORTED terminal fact: %q", string(resultData))
	}
	for _, name := range []string{"attempts", "publication-intent.json", "publication-record.json", "publications"} {
		if _, err := os.Lstat(filepath.Join(fixture.runDirectory, name)); !os.IsNotExist(err) {
			t.Fatalf("pre-attempt abort created %s: %v", name, err)
		}
	}
	worktreeData, err := os.ReadFile(filepath.Join(fixture.worktreePath, "README.md"))
	if err != nil || string(worktreeData) != "base\n" {
		t.Fatalf("pre-attempt abort touched the worktree: %v %q", err, string(worktreeData))
	}
}

func TestTaskAbortTerminatesReadyRunBeforeAnyAttempt(t *testing.T) {
	fixture := newAbortFixture(t, domain.StateReady)
	journalPrefix := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "events.jsonl"))
	frozenInputs := map[string][]byte{}
	for _, name := range []string{"task-spec.json", "policy-snapshot.json", "capability-snapshot.json"} {
		frozenInputs[name] = readAbortFileBytes(t, filepath.Join(fixture.runDirectory, name))
	}
	const reason = "ready run abandoned after a preflight failure"
	var stdout, stderr bytes.Buffer
	if exit := runAbortCommand(fixture.runID, "op:1", reason, true, &stdout, &stderr); exit != ExitOK {
		t.Fatalf("abort exit = %d, stderr = %s", exit, stderr.String())
	}
	var result abortJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode abort output: %v", err)
	}
	if result.Status != "aborted" || result.RunID != fixture.runID || result.State != domain.StateAborted || result.TerminalReason != lifecycle.PreAttemptAbortTerminalReason || result.Actor != "op:1" || result.Sequence != fixture.sequence+1 {
		t.Fatalf("abort result = %+v", result)
	}
	assertPreAttemptAbortEvidence(t, fixture, domain.StateReady, reason, journalPrefix)
	for name, want := range frozenInputs {
		if got := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, name)); !bytes.Equal(got, want) {
			t.Fatalf("pre-attempt abort rewrote frozen input %s", name)
		}
	}

	// status, doctor and cleanup preview agree on ABORTED +
	// aborted-before-attempt.
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "status", "--run", fixture.runID, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("status exit = %d, stderr = %s", exit, stderr.String())
	}
	var statusState domain.RunState
	if err := json.Unmarshal(stdout.Bytes(), &statusState); err != nil {
		t.Fatal(err)
	}
	if statusState.State != domain.StateAborted || statusState.TerminalReason != lifecycle.PreAttemptAbortTerminalReason {
		t.Fatalf("status state = %+v", statusState)
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
	if report.Run.State == nil || report.Run.State.State != domain.StateAborted || report.Run.State.TerminalReason != lifecycle.PreAttemptAbortTerminalReason {
		t.Fatalf("doctor run state = %+v", report.Run.State)
	}
	if report.Run.SnapshotSequence != fixture.sequence+1 || report.Run.JournalSequence != fixture.sequence+1 {
		t.Fatalf("doctor sequences = %d/%d", report.Run.SnapshotSequence, report.Run.JournalSequence)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "cleanup", "--run", fixture.runID, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("cleanup preview exit = %d, stderr = %s", exit, stderr.String())
	}
	var preview struct {
		Applied bool `json:"applied"`
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

	// Identical repeat: idempotent, zero additional writes.
	stdout.Reset()
	stderr.Reset()
	if exit := runAbortCommand(fixture.runID, "op:1", reason, true, &stdout, &stderr); exit != ExitOK {
		t.Fatalf("idempotent repeat exit = %d, stderr = %s", exit, stderr.String())
	}
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil || uint64(len(events)) != fixture.sequence+1 {
		t.Fatalf("idempotent repeat appended events: %d", len(events))
	}
}

func TestTaskAbortTerminatesPlannedRunBeforeAnyAttempt(t *testing.T) {
	fixture := newAbortFixture(t, domain.StatePlanned)
	journalPrefix := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "events.jsonl"))
	frozenInputs := map[string][]byte{}
	for _, name := range []string{"task-spec.json", "policy-snapshot.json", "capability-snapshot.json"} {
		frozenInputs[name] = readAbortFileBytes(t, filepath.Join(fixture.runDirectory, name))
	}
	const reason = "planned run abandoned by the maintainer"
	var stdout, stderr bytes.Buffer
	if exit := runAbortCommand(fixture.runID, "op:1", reason, true, &stdout, &stderr); exit != ExitOK {
		t.Fatalf("abort exit = %d, stderr = %s", exit, stderr.String())
	}
	var result abortJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode abort output: %v", err)
	}
	if result.State != domain.StateAborted || result.TerminalReason != lifecycle.PreAttemptAbortTerminalReason || result.Sequence != fixture.sequence+1 {
		t.Fatalf("abort result = %+v", result)
	}
	assertPreAttemptAbortEvidence(t, fixture, domain.StatePlanned, reason, journalPrefix)
	for name, want := range frozenInputs {
		if got := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, name)); !bytes.Equal(got, want) {
			t.Fatalf("pre-attempt abort rewrote frozen input %s", name)
		}
	}
}

// planReadyRunViaCLI drives one real `marshal task plan` through the CLI so
// the resulting READY run carries genuine planning events and frozen inputs.
func planReadyRunViaCLI(t *testing.T, setup autoFlowSetup, taskID, runID string) {
	t.Helper()
	taskPath := filepath.Join(t.TempDir(), "task.json")
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	writeCLIFixture(t, taskPath, cliPlanningTask(t, setup.repositoryRoot, taskID, setup.remoteURL))
	writeCLIFixture(t, policyPath, cliPlanningPolicy(t, taskID, runID))
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("task plan exit = %d, stderr = %s", exit, stderr.String())
	}
}

func TestTaskAbortAfterRealPlanTerminatesReadyRun(t *testing.T) {
	setup := newAutoFlowSetup(t)
	const taskID, runID = "abort-plan-task", "abort-plan-run"
	planReadyRunViaCLI(t, setup, taskID, runID)
	store := runstore.New(filepath.Join(setup.repositoryRoot, ".marshal"))
	state, err := store.Inspect(runID)
	if err != nil || state.State != domain.StateReady {
		t.Fatalf("planned run state = %+v, err = %v", state, err)
	}
	runDirectory := filepath.Join(setup.repositoryRoot, ".marshal", "runs", runID)
	journalPrefix := readAbortFileBytes(t, filepath.Join(runDirectory, "events.jsonl"))
	const reason = "abandoned immediately after planning"
	var stdout, stderr bytes.Buffer
	if exit := runAbortCommand(runID, "op:plan", reason, true, &stdout, &stderr); exit != ExitOK {
		t.Fatalf("abort exit = %d, stderr = %s", exit, stderr.String())
	}
	var result abortJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != domain.StateAborted || result.TerminalReason != lifecycle.PreAttemptAbortTerminalReason {
		t.Fatalf("abort result = %+v", result)
	}
	// The real journal carries planning events; the abort appends exactly one
	// run.aborted line after them and binds the planning-frozen digests.
	events, _, err := store.ReadEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Type != lifecycle.AbortEventType || last.StateFrom != domain.StateReady || last.StateTo != domain.StateAborted || last.AttemptID != "" {
		t.Fatalf("abort event = %+v", last)
	}
	journalData := readAbortFileBytes(t, filepath.Join(runDirectory, "events.jsonl"))
	if !bytes.Equal(journalData[:len(journalPrefix)], journalPrefix) {
		t.Fatalf("abort rewrote the planning journal prefix")
	}
	aborted, err := store.Inspect(runID)
	if err != nil || aborted.State != domain.StateAborted || aborted.SpecDigest != state.SpecDigest || aborted.PolicyDigest != state.PolicyDigest || aborted.CapabilityDigest != state.CapabilityDigest || aborted.BaseSHA != state.BaseSHA || aborted.WorktreePath != state.WorktreePath {
		t.Fatalf("abort altered the frozen identity: %+v", aborted)
	}
	if _, err := os.Lstat(filepath.Join(runDirectory, "attempts")); !os.IsNotExist(err) {
		t.Fatalf("abort created an attempt tree: %v", err)
	}
}

func TestTaskAbortSucceedsAfterPreflightFailures(t *testing.T) {
	t.Run("missing plan approval keeps run ready", func(t *testing.T) {
		setup := newAutoFlowSetup(t)
		const taskID, runID = "abort-unapproved-task", "abort-unapproved-run"
		planReadyRunViaCLI(t, setup, taskID, runID)
		var stdout, stderr bytes.Buffer
		if exit := Run([]string{"task", "run", "--run", runID}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "plan 审批") {
			t.Fatalf("task run exit = %d, stderr = %q", exit, stderr.String())
		}
		state, err := runstore.New(filepath.Join(setup.repositoryRoot, ".marshal")).Inspect(runID)
		if err != nil || state.State != domain.StateReady {
			t.Fatalf("preflight failure moved the run: %+v, err = %v", state, err)
		}
		stdout.Reset()
		stderr.Reset()
		if exit := runAbortCommand(runID, "op:preflight", "unapproved run abandoned", true, &stdout, &stderr); exit != ExitOK {
			t.Fatalf("abort exit = %d, stderr = %s", exit, stderr.String())
		}
		aborted, err := runstore.New(filepath.Join(setup.repositoryRoot, ".marshal")).Inspect(runID)
		if err != nil || aborted.State != domain.StateAborted || aborted.TerminalReason != lifecycle.PreAttemptAbortTerminalReason {
			t.Fatalf("aborted state = %+v, err = %v", aborted, err)
		}
	})
	t.Run("unregistered frozen adapter", func(t *testing.T) {
		setup := newAutoFlowSetup(t)
		const taskID, runID = "abort-unregistered-task", "abort-unregistered-run"
		planReadyRunViaCLI(t, setup, taskID, runID)
		t.Setenv("MARSHAL_QWEN_PATH", "")
		var stdout, stderr bytes.Buffer
		if exit := Run([]string{"task", "run", "--run", runID}, strings.NewReader(""), &stdout, &stderr); exit != ExitUnavailable {
			t.Fatalf("task run exit = %d, stderr = %q", exit, stderr.String())
		}
		stdout.Reset()
		stderr.Reset()
		if exit := runAbortCommand(runID, "op:preflight", "adapter unavailable run abandoned", true, &stdout, &stderr); exit != ExitOK {
			t.Fatalf("abort exit = %d, stderr = %s", exit, stderr.String())
		}
		aborted, err := runstore.New(filepath.Join(setup.repositoryRoot, ".marshal")).Inspect(runID)
		if err != nil || aborted.State != domain.StateAborted || aborted.TerminalReason != lifecycle.PreAttemptAbortTerminalReason {
			t.Fatalf("aborted state = %+v, err = %v", aborted, err)
		}
	})
	t.Run("corrupt capability snapshot preflight", func(t *testing.T) {
		setup := newAutoFlowSetup(t)
		const taskID, runID = "abort-corrupt-capability-task", "abort-corrupt-capability-run"
		planReadyRunViaCLI(t, setup, taskID, runID)
		capabilityPath := filepath.Join(setup.repositoryRoot, ".marshal", "runs", runID, "capability-snapshot.json")
		corrupted := []byte(`{"corrupt":`)
		if err := os.WriteFile(capabilityPath, corrupted, 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if exit := Run([]string{"task", "run", "--run", runID}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "CapabilitySnapshot") {
			t.Fatalf("task run exit = %d, stderr = %q", exit, stderr.String())
		}
		stdout.Reset()
		stderr.Reset()
		if exit := runAbortCommand(runID, "op:preflight", "corrupt preflight run abandoned", true, &stdout, &stderr); exit != ExitOK {
			t.Fatalf("abort exit = %d, stderr = %s", exit, stderr.String())
		}
		// The corrupt frozen input is diagnostic evidence and survives the
		// abort byte for byte.
		if got := readAbortFileBytes(t, capabilityPath); !bytes.Equal(got, corrupted) {
			t.Fatalf("abort rewrote the corrupt frozen input: %q", string(got))
		}
		aborted, err := runstore.New(filepath.Join(setup.repositoryRoot, ".marshal")).Inspect(runID)
		if err != nil || aborted.State != domain.StateAborted || aborted.TerminalReason != lifecycle.PreAttemptAbortTerminalReason {
			t.Fatalf("aborted state = %+v, err = %v", aborted, err)
		}
	})
}

// assertAbortDenied drives one rejection through the CLI and proves the
// denial is a final decision: fixed sentinel on stderr and --json stdout,
// zero writes anywhere in the run evidence.
func assertAbortDenied(t *testing.T, fixture abortFixture, sentinel string) {
	t.Helper()
	var journalBefore []byte
	journalPath := filepath.Join(fixture.runDirectory, "events.jsonl")
	if _, err := os.Lstat(journalPath); err == nil {
		journalBefore = readAbortFileBytes(t, journalPath)
	}
	stateBefore, err := fixture.store.Inspect(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if exit := runAbortCommand(fixture.runID, "op:1", "denied abort request", true, &stdout, &stderr); exit != ExitFailure {
		t.Fatalf("denied abort exit = %d, stderr = %q", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), sentinel) {
		t.Fatalf("stderr lacks sentinel %s: %q", sentinel, stderr.String())
	}
	var rejected struct {
		Status   string       `json:"status"`
		Sentinel string       `json:"sentinel"`
		State    domain.State `json:"state"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rejected); err != nil {
		t.Fatalf("decode rejected output: %v (%q)", err, stdout.String())
	}
	if rejected.Status != "rejected" || rejected.Sentinel != sentinel || rejected.State != stateBefore.State {
		t.Fatalf("rejected output = %+v", rejected)
	}
	if journalBefore == nil {
		if _, err := os.Lstat(journalPath); !os.IsNotExist(err) {
			t.Fatalf("denied abort created the journal: %v", err)
		}
	} else if got := readAbortFileBytes(t, journalPath); !bytes.Equal(got, journalBefore) {
		t.Fatalf("denied abort modified the journal")
	}
	stateAfter, err := fixture.store.Inspect(fixture.runID)
	if err != nil || stateAfter.State != stateBefore.State || stateAfter.Sequence != stateBefore.Sequence ||
		stateAfter.TerminalReason != stateBefore.TerminalReason || stateAfter.CurrentAttemptID != stateBefore.CurrentAttemptID ||
		stateAfter.AttemptsUsed != stateBefore.AttemptsUsed {
		t.Fatalf("denied abort mutated state: %+v vs %+v, err = %v", stateAfter, stateBefore, err)
	}
	for _, name := range []string{"outcome.json", "outcome.md", "result.md", "result.md.pending", "outcome.json.pending"} {
		if _, err := os.Lstat(filepath.Join(fixture.runDirectory, name)); !os.IsNotExist(err) {
			t.Fatalf("denied abort wrote %s: %v", name, err)
		}
	}
}

func TestTaskAbortPreAttemptDenialsFailClosed(t *testing.T) {
	t.Run("attempt tree present", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		if err := os.MkdirAll(filepath.Join(fixture.runDirectory, "attempts", "attempt-fixture-1"), 0o700); err != nil {
			t.Fatal(err)
		}
		assertAbortDenied(t, fixture, abortDeniedAttemptExists)
	})
	t.Run("snapshot carries attempt identity", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		lease, err := fixture.store.Acquire(fixture.runID)
		if err != nil {
			t.Fatal(err)
		}
		state, err := fixture.store.Inspect(fixture.runID)
		if err != nil {
			t.Fatal(err)
		}
		state.CurrentAttemptID, state.AttemptsUsed = "attempt:1", 1
		if err := fixture.store.WriteSnapshot(lease, state); err != nil {
			t.Fatal(err)
		}
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
		assertAbortDenied(t, fixture, abortDeniedAttemptExists)
	})
	t.Run("journal carries attempt fact", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		// A repair-audit event is the only structurally legal way for an
		// attempt identity to appear on a READY journal tail; the negative
		// proof must still fail closed on it.
		lease, err := fixture.store.Acquire(fixture.runID)
		if err != nil {
			t.Fatal(err)
		}
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:audit-attempt",
			RunID: fixture.runID, AttemptID: "attempt:1", Sequence: fixture.sequence + 1, Type: lifecycle.RepairAuditEventType,
			StateFrom: domain.StateReady, StateTo: domain.StateReady, Timestamp: time.Unix(200, 0).UTC(), Payload: map[string]any{},
		}
		if err := fixture.store.Append(lease, event, fixture.sequence); err != nil {
			t.Fatal(err)
		}
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
		assertAbortDenied(t, fixture, abortDeniedAttemptExists)
	})
	t.Run("publication intent present", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		if err := os.WriteFile(filepath.Join(fixture.runDirectory, "publication-intent.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		assertAbortDenied(t, fixture, abortDeniedPublicationIntent)
	})
	t.Run("side effect fact present", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		if err := os.WriteFile(filepath.Join(fixture.runDirectory, "verification-report.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		assertAbortDenied(t, fixture, abortDeniedSideEffect)
	})
	t.Run("publication record present", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		if err := os.WriteFile(filepath.Join(fixture.runDirectory, "publication-record.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		assertAbortDenied(t, fixture, abortDeniedPublicationPresent)
	})
	t.Run("publications archive present", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		if err := os.MkdirAll(filepath.Join(fixture.runDirectory, "publications"), 0o700); err != nil {
			t.Fatal(err)
		}
		assertAbortDenied(t, fixture, abortDeniedPublicationPresent)
	})
	t.Run("snapshot carries publication identity", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		lease, err := fixture.store.Acquire(fixture.runID)
		if err != nil {
			t.Fatal(err)
		}
		state, err := fixture.store.Inspect(fixture.runID)
		if err != nil {
			t.Fatal(err)
		}
		state.Publication = &domain.RunPublication{Provider: "github", Repository: "org/repo", HeadBranch: "marshal/abort", BaseBranch: "main"}
		if err := fixture.store.WriteSnapshot(lease, state); err != nil {
			t.Fatal(err)
		}
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
		assertAbortDenied(t, fixture, abortDeniedPublicationPresent)
	})
	t.Run("publishing state", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StatePublishing)
		assertAbortDenied(t, fixture, abortDeniedPublicationInProgress)
	})
	t.Run("published state", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StatePublished)
		assertAbortDenied(t, fixture, abortDeniedPublicationPresent)
	})
	t.Run("ci pending state", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateCIPending)
		assertAbortDenied(t, fixture, abortDeniedPublicationPresent)
	})
}

func TestTaskAbortRejectsOtherStates(t *testing.T) {
	for _, target := range []domain.State{domain.StateCreated, domain.StateRunning, domain.StateVerifying, domain.StateReworkRequested} {
		target := target
		t.Run(string(target), func(t *testing.T) {
			fixture := newAbortFixture(t, target)
			assertAbortDenied(t, fixture, abortDeniedStateNotEligible)
		})
	}
	t.Run(string(domain.StateAccepted), func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateAccepted)
		var stdout, stderr bytes.Buffer
		if exit := runAbortCommand(fixture.runID, "op:1", "re-abort a closed run", true, &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "已处于终态") {
			t.Fatalf("terminal re-abort exit = %d, stderr = %q", exit, stderr.String())
		}
		events, _, err := fixture.store.ReadEvents(fixture.runID)
		if err != nil || uint64(len(events)) != fixture.sequence {
			t.Fatalf("terminal re-abort modified the journal: %d events, err = %v", len(events), err)
		}
		for _, name := range []string{"outcome.json", "result.md"} {
			if _, err := os.Lstat(filepath.Join(fixture.runDirectory, name)); !os.IsNotExist(err) {
				t.Fatalf("terminal re-abort wrote %s: %v", name, err)
			}
		}
	})
}

func TestTaskAbortRepeatIdentitiesAfterReadyAbort(t *testing.T) {
	fixture := newAbortFixture(t, domain.StateReady)
	const reason = "abandoned before dispatch"
	var stdout, stderr bytes.Buffer
	if exit := runAbortCommand(fixture.runID, "op:1", reason, true, &stdout, &stderr); exit != ExitOK {
		t.Fatalf("abort exit = %d, stderr = %s", exit, stderr.String())
	}
	outcomeData := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "outcome.json"))
	resultData := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "result.md"))

	// Same run, same authority, same actor/reason/digest: idempotent.
	stdout.Reset()
	stderr.Reset()
	if exit := runAbortCommand(fixture.runID, "op:1", reason, true, &stdout, &stderr); exit != ExitOK {
		t.Fatalf("idempotent repeat exit = %d, stderr = %s", exit, stderr.String())
	}
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil || uint64(len(events)) != fixture.sequence+1 {
		t.Fatalf("idempotent repeat appended events: %d, err = %v", len(events), err)
	}
	if got := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "outcome.json")); !bytes.Equal(got, outcomeData) {
		t.Fatalf("idempotent repeat rewrote the outcome")
	}
	if got := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "result.md")); !bytes.Equal(got, resultData) {
		t.Fatalf("idempotent repeat rewrote result.md")
	}

	// Same slot, different actor: deterministic conflict, zero writes.
	stdout.Reset()
	stderr.Reset()
	if exit := runAbortCommand(fixture.runID, "op:2", reason, false, &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "身份不一致") {
		t.Fatalf("foreign-actor conflict exit = %d, stderr = %q", exit, stderr.String())
	}
	// Same slot, different reason: deterministic conflict, zero writes.
	stdout.Reset()
	stderr.Reset()
	if exit := runAbortCommand(fixture.runID, "op:1", "a different reason", false, &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "身份不一致") {
		t.Fatalf("foreign-reason conflict exit = %d, stderr = %q", exit, stderr.String())
	}
	events, _, err = fixture.store.ReadEvents(fixture.runID)
	if err != nil || uint64(len(events)) != fixture.sequence+1 {
		t.Fatalf("conflicting repeats appended events: %d, err = %v", len(events), err)
	}
	if got := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "outcome.json")); !bytes.Equal(got, outcomeData) {
		t.Fatalf("conflicting repeats rewrote the outcome")
	}
}

// appendCrashAuthorityEvent appends exactly the pre-attempt abort authority
// event through the real Core append path, simulating a crash immediately
// after the journal write and before outcome/result/snapshot completion.
func appendCrashAuthorityEvent(t *testing.T, fixture abortFixture, actor, reason string, timestamp time.Time) domain.RunEvent {
	t.Helper()
	lease, err := fixture.store.Acquire(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release() }()
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:abort-crash",
		RunID: fixture.runID, AttemptID: "", Sequence: fixture.sequence + 1, Type: lifecycle.AbortEventType,
		StateFrom: domain.StateReady, StateTo: domain.StateAborted, Timestamp: timestamp,
		Actor:   &domain.Actor{Type: domain.ControlSourceTypeHuman, ID: actor},
		Payload: map[string]any{"terminalReason": lifecycle.PreAttemptAbortTerminalReason, "reason": reason},
	}
	if err := fixture.store.Append(lease, event, fixture.sequence); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestTaskAbortReplaysAfterJournalAppendCrash(t *testing.T) {
	const (
		actor  = "op:crash"
		reason = "crash replay reason"
	)
	timestamp := time.Unix(500, 0).UTC()

	t.Run("crash before outcome result and snapshot", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		event := appendCrashAuthorityEvent(t, fixture, actor, reason, timestamp)
		var stdout, stderr bytes.Buffer
		if exit := runAbortCommand(fixture.runID, actor, reason, true, &stdout, &stderr); exit != ExitOK {
			t.Fatalf("crash replay exit = %d, stderr = %s", exit, stderr.String())
		}
		events, _, err := fixture.store.ReadEvents(fixture.runID)
		if err != nil || uint64(len(events)) != fixture.sequence+1 {
			t.Fatalf("crash replay appended a second event: %d events, err = %v", len(events), err)
		}
		abortCount := 0
		for _, journalEvent := range events {
			if journalEvent.Type == lifecycle.AbortEventType {
				abortCount++
			}
		}
		if abortCount != 1 {
			t.Fatalf("crash replay recorded %d abort events", abortCount)
		}
		state, err := fixture.store.Inspect(fixture.runID)
		if err != nil || state.State != domain.StateAborted || state.TerminalReason != lifecycle.PreAttemptAbortTerminalReason || state.Sequence != fixture.sequence+1 {
			t.Fatalf("crash replay state = %+v, err = %v", state, err)
		}
		outcomeData := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "outcome.json"))
		var outcome domain.OutcomeBundle
		if err := json.Unmarshal(outcomeData, &outcome); err != nil {
			t.Fatal(err)
		}
		payloadData, err := json.Marshal(event.Payload)
		if err != nil {
			t.Fatal(err)
		}
		wantDigest, err := canonical.DigestJSON(payloadData)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.TerminalState != domain.StateAborted || outcome.Verdict != "abort" || outcome.FinalReviewDigest != wantDigest || outcome.FinalEvidenceDigest != wantDigest || !outcome.GeneratedAt.Equal(timestamp) {
			t.Fatalf("crash replay outcome not bound to the authority event: %+v", outcome)
		}
		if _, err := os.Lstat(filepath.Join(fixture.runDirectory, "result.md")); err != nil {
			t.Fatalf("crash replay did not complete result.md: %v", err)
		}
		// A second identical replay stays idempotent.
		stdout.Reset()
		stderr.Reset()
		if exit := runAbortCommand(fixture.runID, actor, reason, true, &stdout, &stderr); exit != ExitOK {
			t.Fatalf("second crash replay exit = %d, stderr = %s", exit, stderr.String())
		}
		events, _, err = fixture.store.ReadEvents(fixture.runID)
		if err != nil || uint64(len(events)) != fixture.sequence+1 {
			t.Fatalf("second crash replay appended events: %d", len(events))
		}
	})

	t.Run("crash after outcome commit", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		event := appendCrashAuthorityEvent(t, fixture, actor, reason, timestamp)
		payloadData, err := json.Marshal(event.Payload)
		if err != nil {
			t.Fatal(err)
		}
		abortDigest, err := canonical.DigestJSON(payloadData)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := review.PrepareOutcome(fixture.runDirectory, review.OutcomeData{
			TaskID: fixture.taskID, RunID: fixture.runID, TerminalState: domain.StateAborted, Verdict: "abort",
			FinalReviewRound: 1, FinalReviewDigest: abortDigest, FinalEvidenceDigest: abortDigest,
			Summary: reason, FindingCount: 0, GeneratedAt: timestamp,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.Commit(); err != nil {
			t.Fatal(err)
		}
		outcomeBefore := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "outcome.json"))
		var stdout, stderr bytes.Buffer
		if exit := runAbortCommand(fixture.runID, actor, reason, true, &stdout, &stderr); exit != ExitOK {
			t.Fatalf("crash replay exit = %d, stderr = %s", exit, stderr.String())
		}
		if got := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "outcome.json")); !bytes.Equal(got, outcomeBefore) {
			t.Fatalf("crash replay rewrote the committed outcome")
		}
		if _, err := os.Lstat(filepath.Join(fixture.runDirectory, "result.md")); err != nil {
			t.Fatalf("crash replay did not complete result.md: %v", err)
		}
		state, err := fixture.store.Inspect(fixture.runID)
		if err != nil || state.State != domain.StateAborted || state.Sequence != fixture.sequence+1 {
			t.Fatalf("crash replay state = %+v, err = %v", state, err)
		}
	})

	t.Run("crash after result commit with stale snapshot", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		event := appendCrashAuthorityEvent(t, fixture, actor, reason, timestamp)
		payloadData, err := json.Marshal(event.Payload)
		if err != nil {
			t.Fatal(err)
		}
		abortDigest, err := canonical.DigestJSON(payloadData)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := review.PrepareOutcome(fixture.runDirectory, review.OutcomeData{
			TaskID: fixture.taskID, RunID: fixture.runID, TerminalState: domain.StateAborted, Verdict: "abort",
			FinalReviewRound: 1, FinalReviewDigest: abortDigest, FinalEvidenceDigest: abortDigest,
			Summary: reason, FindingCount: 0, GeneratedAt: timestamp,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.Commit(); err != nil {
			t.Fatal(err)
		}
		replayed, err := fixture.store.Inspect(fixture.runID)
		if err != nil {
			t.Fatal(err)
		}
		if err := stageAbortResult(fixture.runDirectory, replayed, actor, reason, timestamp, domain.StateAborted, lifecycle.PreAttemptAbortTerminalReason); err != nil {
			t.Fatal(err)
		}
		if err := commitAbortResult(fixture.runDirectory); err != nil {
			t.Fatal(err)
		}
		resultBefore := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "result.md"))
		// Roll the snapshot back to the pre-abort READY state: the journal
		// remains the sole authority.
		lease, err := fixture.store.Acquire(fixture.runID)
		if err != nil {
			t.Fatal(err)
		}
		stale := replayed
		stale.State, stale.Sequence, stale.TerminalReason = domain.StateReady, fixture.sequence, ""
		if err := fixture.store.WriteSnapshot(lease, stale); err != nil {
			t.Fatal(err)
		}
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if exit := runAbortCommand(fixture.runID, actor, reason, true, &stdout, &stderr); exit != ExitOK {
			t.Fatalf("crash replay exit = %d, stderr = %s", exit, stderr.String())
		}
		if got := readAbortFileBytes(t, filepath.Join(fixture.runDirectory, "result.md")); !bytes.Equal(got, resultBefore) {
			t.Fatalf("crash replay rewrote the committed result.md")
		}
		state, err := fixture.store.Inspect(fixture.runID)
		if err != nil || state.State != domain.StateAborted || state.Sequence != fixture.sequence+1 || state.TerminalReason != lifecycle.PreAttemptAbortTerminalReason {
			t.Fatalf("crash replay did not refresh the snapshot: %+v, err = %v", state, err)
		}
		events, _, err := fixture.store.ReadEvents(fixture.runID)
		if err != nil || uint64(len(events)) != fixture.sequence+1 {
			t.Fatalf("crash replay appended events: %d", len(events))
		}
	})

	t.Run("divergent request after crash writes nothing", func(t *testing.T) {
		fixture := newAbortFixture(t, domain.StateReady)
		appendCrashAuthorityEvent(t, fixture, actor, reason, timestamp)
		var stdout, stderr bytes.Buffer
		if exit := runAbortCommand(fixture.runID, actor, "a different reason", false, &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "身份不一致") {
			t.Fatalf("divergent replay exit = %d, stderr = %q", exit, stderr.String())
		}
		for _, name := range []string{"outcome.json", "result.md"} {
			if _, err := os.Lstat(filepath.Join(fixture.runDirectory, name)); !os.IsNotExist(err) {
				t.Fatalf("divergent replay wrote %s: %v", name, err)
			}
		}
		snapshot, err := fixture.store.ReadSnapshot(fixture.runID)
		if err != nil || snapshot.State != domain.StateReady || snapshot.Sequence != fixture.sequence {
			t.Fatalf("divergent replay moved the snapshot: %+v, err = %v", snapshot, err)
		}
		events, _, err := fixture.store.ReadEvents(fixture.runID)
		if err != nil || uint64(len(events)) != fixture.sequence+1 {
			t.Fatalf("divergent replay modified the journal: %d events, err = %v", len(events), err)
		}
	})
}

func TestTaskAbortAndRunRaceSerializeOnRunLease(t *testing.T) {
	setup := newAutoFlowSetup(t)
	const taskID, runID = "abort-race-task", "abort-race-run"
	setup.planAndApprove(t, taskID, runID, true)
	start := make(chan struct{})
	type commandOutcome struct {
		exit   int
		stdout string
		stderr string
	}
	abortOutcome := make(chan commandOutcome, 1)
	runOutcome := make(chan commandOutcome, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		var stdout, stderr bytes.Buffer
		exit := runAbortCommand(runID, "op:race", "race abort", true, &stdout, &stderr)
		abortOutcome <- commandOutcome{exit, stdout.String(), stderr.String()}
	}()
	go func() {
		defer wg.Done()
		<-start
		var stdout, stderr bytes.Buffer
		exit := Run([]string{"task", "run", "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr)
		runOutcome <- commandOutcome{exit, stdout.String(), stderr.String()}
	}()
	close(start)
	wg.Wait()
	abortResult := <-abortOutcome
	runResult := <-runOutcome

	store := runstore.New(filepath.Join(setup.repositoryRoot, ".marshal"))
	state, err := store.Inspect(runID)
	if err != nil {
		t.Fatalf("race left inconsistent run evidence: %v", err)
	}
	events, _, err := store.ReadEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	abortEvents, attemptFacts := 0, 0
	for _, event := range events {
		if event.Type == lifecycle.AbortEventType {
			abortEvents++
		}
		if event.AttemptID != "" || event.Type == "worker.started" {
			attemptFacts++
		}
	}
	if state.State == domain.StateAborted {
		// The abort won the lease race: no attempt authority fact may exist
		// and task run must have failed closed.
		if abortResult.exit != ExitOK {
			t.Fatalf("abort lost the race it must have won: exit = %d, stderr = %s", abortResult.exit, abortResult.stderr)
		}
		if runResult.exit == ExitOK {
			t.Fatalf("task run must not succeed after the abort won: %s", runResult.stdout)
		}
		if abortEvents != 1 || attemptFacts != 0 {
			t.Fatalf("abort-first race left %d abort events and %d attempt facts", abortEvents, attemptFacts)
		}
		if state.TerminalReason != lifecycle.PreAttemptAbortTerminalReason || state.CurrentAttemptID != "" || state.AttemptsUsed != 0 {
			t.Fatalf("abort-first race state = %+v", state)
		}
		attemptsRoot := filepath.Join(setup.repositoryRoot, ".marshal", "runs", runID, "attempts")
		if entries, err := os.ReadDir(attemptsRoot); err == nil && len(entries) != 0 {
			t.Fatalf("abort-first race left attempt directories: %v", entries)
		}
		return
	}
	// The run won the lease race: an attempt authority fact exists and the
	// abort must have failed closed without any terminal write.
	if runResult.exit != ExitOK {
		t.Fatalf("task run lost the race it must have won: exit = %d, stderr = %s", runResult.exit, runResult.stderr)
	}
	if abortResult.exit == ExitOK {
		t.Fatalf("abort must fail closed once an attempt authority fact exists: %s", abortResult.stdout)
	}
	if abortEvents != 0 {
		t.Fatalf("run-first race recorded an abort event: %d", abortEvents)
	}
	if attemptFacts == 0 {
		t.Fatalf("run-first race left no attempt authority fact")
	}
}

func TestTaskAbortConcurrentSameRequestAppendsSingleEvent(t *testing.T) {
	fixture := newAbortFixture(t, domain.StateReady)
	start := make(chan struct{})
	exits := make(chan int, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var stdout, stderr bytes.Buffer
			exits <- runAbortCommand(fixture.runID, "op:1", "concurrent abort", true, &stdout, &stderr)
		}()
	}
	close(start)
	wg.Wait()
	close(exits)
	successes := 0
	for exit := range exits {
		if exit == ExitOK {
			successes++
		} else if exit != ExitFailure {
			t.Fatalf("concurrent abort unexpected exit = %d", exit)
		}
	}
	if successes == 0 {
		t.Fatalf("no writer won the serialized abort race")
	}
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil || uint64(len(events)) != fixture.sequence+1 {
		t.Fatalf("concurrent aborts appended %d events, err = %v", len(events), err)
	}
	abortCount := 0
	for _, event := range events {
		if event.Type == lifecycle.AbortEventType {
			abortCount++
		}
	}
	if abortCount != 1 {
		t.Fatalf("concurrent aborts recorded %d abort events", abortCount)
	}
	state, err := fixture.store.Inspect(fixture.runID)
	if err != nil || state.State != domain.StateAborted || state.TerminalReason != lifecycle.PreAttemptAbortTerminalReason {
		t.Fatalf("concurrent abort state = %+v, err = %v", state, err)
	}
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
