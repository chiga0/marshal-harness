package verification_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/verification"
)

// Issue #138 跨包回归：污染只存在于 command isolate 时，Review 的
// current-observation guard 与 PacketBuilder 仍绑定原 Candidate。
func TestIssue138MutationStillBuildsReviewPacketForOriginalCandidate(t *testing.T) {
	repository := t.TempDir()
	gitReviewTest(t, repository, "init", "-q")
	gitReviewTest(t, repository, "config", "user.name", "Marshal Test")
	gitReviewTest(t, repository, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitReviewTest(t, repository, "add", "README.md")
	gitReviewTest(t, repository, "commit", "-q", "-m", "base")
	baseSHA := strings.TrimSpace(gitReviewTest(t, repository, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repository, "source.txt"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	taskData, err := os.ReadFile(filepath.Join("..", "..", "schemas", "examples", "happy-path", "task-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatal(err)
	}
	specDigest, err := canonical.DigestJSON(taskData)
	if err != nil {
		t.Fatal(err)
	}
	runDirectory := t.TempDir()
	input := verification.Input{
		TaskID: "ENG-123", RunID: "run-01", AttemptID: "attempt-01",
		AuthorityNamespaceID: "authority-01", SpecDigest: specDigest,
		BaseSHA: baseSHA, Worktree: repository, RunDirectory: runDirectory,
		Scope:             verification.ScopePolicy{AllowPaths: []string{"source.txt"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20},
		Commands:          []verification.CommandSpec{{ID: "polluting", Argv: []string{"sh", "-c", "mkdir __pycache__; printf cache > __pycache__/module.pyc"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096}},
		PatchCaptureBytes: 1 << 20,
	}
	result, err := verification.New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" || result.Report.CandidateDigest == "" || result.Report.WorkerCandidateDigest == "" {
		t.Fatalf("unexpected verification result: %+v", result.Report)
	}
	current, err := verification.Observe(repository, baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := review.ValidateCurrentObservation(result.Report, current); err != nil {
		t.Fatalf("isolated command invalidated original candidate: %v", err)
	}

	workerData, err := os.ReadFile(filepath.Join("..", "..", "schemas", "examples", "happy-path", "worker-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	workerDirectory := filepath.Join(runDirectory, "attempts", "attempt-01")
	if err := os.MkdirAll(workerDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDirectory, "worker-result.json"), workerData, 0o600); err != nil {
		t.Fatal(err)
	}
	reportData, err := json.Marshal(result.Report)
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := json.Marshal(result.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	packet, _, err := (&review.PacketBuilder{RunDirectory: runDirectory, Validator: validator}).Build(review.PacketBuildInput{
		Task: task, TaskData: taskData, Report: result.Report, ReportData: reportData,
		Manifest: result.Manifest, ManifestData: manifestData, TaskID: "ENG-123", RunID: "run-01",
		SpecDigest: specDigest, BaseSHA: baseSHA, ReviewRound: 1, AttemptsUsed: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if packet.CandidateDigest != result.Report.CandidateDigest || packet.WorkerCandidateDigest != result.Report.WorkerCandidateDigest || packet.DiffDigest != current.DiffDigest {
		t.Fatalf("packet lost original candidate binding: %+v", packet)
	}
}

func gitReviewTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
