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
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

func TestTaskReviewEndToEndRejectsStaleWorktreeAndPersistsTerminalOutcome(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	runGitCLI(t, repository, "init", "-q")
	runGitCLI(t, repository, "config", "user.name", "Marshal Test")
	runGitCLI(t, repository, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCLI(t, repository, "add", "README.md")
	runGitCLI(t, repository, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(runGitCLI(t, repository, "rev-parse", "HEAD"))
	location, err := marshalRepository.Discover(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := location.Init(); err != nil {
		t.Fatal(err)
	}
	manager, err := gitworktree.Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(location.StateRoot, "TASK-REVIEW", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repository, "worktree", "remove", "--force", worktree.Path).Run()
		_ = exec.Command("git", "-C", repository, "branch", "-D", worktree.Branch).Run()
	})
	if err := os.MkdirAll(filepath.Join(worktree.Path, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree.Path, "src", "code.go"), []byte("package src\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskData := []byte(fmt.Sprintf(`{
  "apiVersion":"marshal.dev/v1alpha1","kind":"Task",
  "metadata":{"id":"TASK-REVIEW","title":"CLI review fixture"},
  "repository":{"path":%q,"baseRef":"%s","remote":"origin"},
  "work":{"objective":"独立验证并审查隔离变更。","constraints":["保留证据"]},
  "scope":{"allowPaths":["src/**"],"denyPaths":[],"allowSubmodules":false,"maxChangedFiles":5,"maxDiffBytes":100000},
  "acceptance":{"commands":[{"id":"source-exists","argv":["sh","-c","test -f src/code.go"],"cwd":".","timeoutSeconds":5,"required":true,"baselinePolicy":"none","maxLogBytes":4096}],"allowNoChange":false},
  "deliverables":[{"id":"source","kind":"code","required":true,"pathGlob":"src/*.go","minimumCount":1}],
  "worker":{"preferredAdapter":"fake","fallbackAdapters":[],"executionProfile":"workspace-write","sessionPolicy":"ephemeral"},
  "budgets":{"runTimeoutSeconds":60,"attemptTimeoutSeconds":30,"maxAttempts":2,"maxOperationalRetries":0,"maxReworkRounds":1,"maxOutputBytes":100000},
  "publication":{"required":false,"provider":"none","mode":"none","remote":"origin","baseBranch":"main","mergePolicy":"never","requiredChecks":[]}
}`, repository, base))
	digest, err := canonical.DigestJSON(taskData)
	if err != nil {
		t.Fatal(err)
	}
	store := runstore.New(location.StateRoot)
	lease, err := store.Acquire("run:review")
	if err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState("TASK-REVIEW", "run:review", time.Now())
	transitions := [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateVerifying}}
	for index, transition := range transitions {
		event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: fmt.Sprintf("event:review:%d", index+1), RunID: "run:review", Sequence: uint64(index + 1), Type: "run.transition", StateFrom: transition[0], StateTo: transition[1], Timestamp: time.Now().UTC(), Payload: map[string]any{}}
		if transition[1] == domain.StateRunning {
			event.AttemptID = "attempt:1"
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
	}
	state.State, state.Sequence, state.SpecDigest, state.BaseSHA, state.WorktreePath = domain.StateVerifying, uint64(len(transitions)), digest, base, worktree.Path
	state.AttemptsUsed, state.CurrentAttemptID = 1, "attempt:1"
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	runDirectory := filepath.Join(location.StateRoot, "runs", "run:review")
	if err := os.WriteFile(filepath.Join(runDirectory, "task-spec.json"), taskData, 0o600); err != nil {
		t.Fatal(err)
	}
	workerData, err := marshalSchemas.FS.ReadFile("examples/happy-path/worker-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var worker map[string]any
	if err := json.Unmarshal(workerData, &worker); err != nil {
		t.Fatal(err)
	}
	worker["taskId"], worker["runId"], worker["attemptId"] = "TASK-REVIEW", "run:review", "attempt:1"
	worker["declaredChangedFiles"] = []string{"src/code.go"}
	workerData, err = json.Marshal(worker)
	if err != nil {
		t.Fatal(err)
	}
	workerPath := filepath.Join(runDirectory, "attempts", "attempt:1", "worker-result.json")
	if err := os.MkdirAll(filepath.Dir(workerPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workerPath, workerData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repository); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"task", "verify", "--run", "run:review", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("verify exit = %d, stderr = %s", exit, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "review", "--run", "run:review", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("review packet exit = %d, stderr = %s", exit, stderr.String())
	}
	packetData, err := os.ReadFile(filepath.Join(runDirectory, "review-packet.json"))
	if err != nil {
		t.Fatal(err)
	}
	var packet domain.ReviewPacket
	if err := json.Unmarshal(packetData, &packet); err != nil {
		t.Fatal(err)
	}
	packetDigest, err := canonical.DigestJSON(packetData)
	if err != nil {
		t.Fatal(err)
	}
	invalidDecisionPath := filepath.Join(repository, "invalid-decision.txt")
	if err := os.WriteFile(invalidDecisionPath, []byte("看起来没问题，可以合并。\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "review", "--run", "run:review", "--decision", invalidDecisionPath}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure {
		t.Fatalf("invalid prose exit = %d, stderr = %s", exit, stderr.String())
	}
	unchangedState, err := store.Inspect("run:review")
	if err != nil {
		t.Fatal(err)
	}
	if unchangedState.State != domain.StateReviewPending || unchangedState.Sequence != 5 {
		t.Fatalf("invalid decision changed state: %+v", unchangedState)
	}

	if err := os.WriteFile(filepath.Join(worktree.Path, "src", "late.go"), []byte("package src\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "review", "--run", "run:review"}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "worktree evidence changed") {
		t.Fatalf("stale review exit = %d, stderr = %s", exit, stderr.String())
	}
	unchangedState, err = store.Inspect("run:review")
	if err != nil {
		t.Fatal(err)
	}
	if unchangedState.State != domain.StateReviewPending || unchangedState.Sequence != 5 {
		t.Fatalf("stale evidence changed state: %+v", unchangedState)
	}
	if err := os.Remove(filepath.Join(worktree.Path, "src", "late.go")); err != nil {
		t.Fatal(err)
	}

	decision := domain.ReviewDecision{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision,
		TaskID: "TASK-REVIEW", RunID: "run:review", ReviewRound: packet.ReviewRound,
		Reviewer:   domain.Reviewer{Type: "lead-agent", ID: "codex"},
		SpecDigest: digest, ReviewPacketDigest: packetDigest, VerificationDigest: packet.VerificationDigest,
		ArtifactManifestDigest: packet.ArtifactManifestDigest, EvidenceDigest: packet.EvidenceDigest,
		Verdict: "reject", Summary: "E2E 明确拒绝，用于验证终态记录。",
		BlockingFindings: []domain.Finding{}, NonBlockingFindings: []domain.Finding{},
		PublicationRecommendation: "do-not-publish", MergeRecommendation: "do-not-merge", DecidedAt: time.Now().UTC(),
	}
	decisionData, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	decisionPath := filepath.Join(repository, "review-decision.json")
	if err := os.WriteFile(decisionPath, decisionData, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "review", "--run", "run:review", "--decision", decisionPath, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("decision exit = %d, stderr = %s", exit, stderr.String())
	}
	terminalState, err := store.Inspect("run:review")
	if err != nil {
		t.Fatal(err)
	}
	if terminalState.State != domain.StateRejected || terminalState.Sequence != 6 {
		t.Fatalf("terminal state = %+v", terminalState)
	}
	for _, path := range []string{"decisions/decision-001.json", "review-packets/packet-001.json", "outcome.json", "outcome.md", "result.md"} {
		if _, err := os.Stat(filepath.Join(runDirectory, path)); err != nil {
			t.Fatalf("missing durable review artifact %s: %v", path, err)
		}
	}
	resultMarkdown, err := os.ReadFile(filepath.Join(runDirectory, "result.md"))
	if err != nil {
		t.Fatal(err)
	}
	outcomeMarkdown, err := os.ReadFile(filepath.Join(runDirectory, "outcome.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(resultMarkdown, outcomeMarkdown) {
		t.Fatalf("result.md bytes diverge from outcome.md")
	}

	if err := os.RemoveAll(filepath.Join(worktree.Path, "src")); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "cleanup", "--run", "run:review", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("cleanup preview exit = %d, stderr = %s", exit, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("cleanup preview stderr = %q", stderr.String())
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
	if preview.RunID != "run:review" || preview.Applied || len(preview.Targets) != 1 || preview.Targets[0].Kind != "managed-worktree" || preview.Targets[0].Path != worktree.Path {
		t.Fatalf("cleanup preview = %+v", preview)
	}
}
