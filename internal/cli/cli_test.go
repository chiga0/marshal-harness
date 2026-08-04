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

func TestDoctorReportsCompiledContracts(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	exitCode := Run([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != ExitOK {
		t.Fatalf("Run() exit = %d, stderr = %s", exitCode, stderr.String())
	}
	var report struct {
		Status          string `json:"status"`
		ContractSchemas int    `json:"contractSchemas"`
		WorkerAdapters  int    `json:"workerAdapters"`
		Milestone       string `json:"milestone"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	if report.Status != "ok" || report.ContractSchemas != 15 || report.WorkerAdapters != 0 || report.Milestone != "5" {
		t.Fatalf("doctor report = %+v", report)
	}
}

func TestContractValidateFromStandardInput(t *testing.T) {
	t.Parallel()

	data, err := marshalSchemas.FS.ReadFile("examples/happy-path/task-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := Run([]string{"contract", "validate", "-"}, bytes.NewReader(data), &stdout, &stderr)
	if exitCode != ExitOK {
		t.Fatalf("Run() exit = %d, stderr = %s", exitCode, stderr.String())
	}
	if got := stdout.String(); got != "有效：Task\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestContractValidateWithExplicitSchema(t *testing.T) {
	t.Parallel()

	task, err := marshalSchemas.FS.ReadFile("examples/happy-path/task-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := Run([]string{"contract", "validate", "--schema", "task-spec", "-"}, bytes.NewReader(task), &stdout, &stderr)
	if exitCode != ExitOK {
		t.Fatalf("valid Task exit = %d, stderr = %s", exitCode, stderr.String())
	}

	runState, err := marshalSchemas.FS.ReadFile("examples/happy-path/run-state.json")
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	exitCode = Run([]string{"contract", "validate", "--schema", "task-spec", "-"}, bytes.NewReader(runState), &stdout, &stderr)
	if exitCode != ExitFailure {
		t.Fatalf("mismatched RunState exit = %d, want %d", exitCode, ExitFailure)
	}
}

func TestReadBoundedRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	if _, err := readBounded(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("readBounded accepted oversized input")
	}
}

func TestPatchCaptureLimitUsesSafeDefault(t *testing.T) {
	if got := patchCaptureLimit(0); got != 64<<20 {
		t.Fatalf("default patch capture limit = %d", got)
	}
	if got := patchCaptureLimit(99); got != 100 {
		t.Fatalf("bounded patch capture limit = %d", got)
	}
}

func TestTaskSkeletonHasNoFilesystemSideEffects(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	temporaryDirectory := t.TempDir()
	if err := os.Chdir(temporaryDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	for _, command := range taskCommands {
		if command == "run" || command == "status" || command == "verify" || command == "review" || command == "publish" || command == "accept" {
			continue
		}
		var stdout, stderr bytes.Buffer
		exitCode := Run([]string{"task", command}, strings.NewReader(""), &stdout, &stderr)
		if exitCode != ExitUnavailable {
			t.Fatalf("task %s exit = %d, want %d", command, exitCode, ExitUnavailable)
		}
	}
	if _, err := os.Stat(filepath.Join(temporaryDirectory, ".marshal")); !os.IsNotExist(err) {
		t.Fatalf("task skeleton created .marshal: %v", err)
	}
}

func TestInitAndTaskStatusEndToEnd(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	command := exec.Command("git", "-C", repository, "init", "-q")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.Chdir(repository); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}
	store := runstore.New(filepath.Join(repository, ".marshal"))
	lease, err := store.Acquire("run:fixture")
	if err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState("task:fixture", "run:fixture", time.Unix(1, 0))
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "status", "--run", "run:fixture", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("status exit = %d, stderr = %s", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"state": "CREATED"`) {
		t.Fatalf("status output = %s", stdout.String())
	}
}

func TestTaskVerifyEndToEnd(t *testing.T) {
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
	worktree, err := manager.Create(location.StateRoot, "TASK-1", base)
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
  "metadata":{"id":"TASK-1","title":"CLI verification fixture"},
  "repository":{"path":%q,"baseRef":"%s","remote":"origin"},
  "work":{"objective":"Verify an isolated fixture change."},
  "scope":{"allowPaths":["src/**"],"denyPaths":[],"allowSubmodules":false,"maxChangedFiles":5,"maxDiffBytes":100000},
  "acceptance":{"commands":[{"id":"source-exists","argv":["sh","-c","test -f src/code.go"],"cwd":".","timeoutSeconds":5,"required":true,"baselinePolicy":"none","maxLogBytes":4096}],"allowNoChange":false},
  "deliverables":[{"id":"source","kind":"code","required":true,"pathGlob":"src/*.go","minimumCount":1},{"id":"pull-request","kind":"publication","required":true}],
  "worker":{"preferredAdapter":"fake","fallbackAdapters":[],"executionProfile":"workspace-write","sessionPolicy":"ephemeral"},
  "budgets":{"runTimeoutSeconds":60,"attemptTimeoutSeconds":30,"maxAttempts":1,"maxOperationalRetries":0,"maxReworkRounds":0,"maxOutputBytes":100000},
  "publication":{"required":true,"provider":"github","mode":"draft","remote":"origin","baseBranch":"main","mergePolicy":"never","requiredChecks":[]}
}`, repository, base))
	digest, err := canonical.DigestJSON(taskData)
	if err != nil {
		t.Fatal(err)
	}
	store := runstore.New(location.StateRoot)
	lease, err := store.Acquire("run:verify")
	if err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState("TASK-1", "run:verify", time.Now())
	transitions := [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateVerifying}}
	for index, transition := range transitions {
		event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: fmt.Sprintf("event:%d", index+1), RunID: "run:verify", Sequence: uint64(index + 1), Type: "run.transition", StateFrom: transition[0], StateTo: transition[1], Timestamp: time.Now().UTC(), Payload: map[string]any{}}
		if transition[1] == domain.StateRunning {
			event.AttemptID = "attempt:1"
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
	}
	state.State, state.Sequence, state.SpecDigest, state.BaseSHA, state.WorktreePath = domain.StateVerifying, uint64(len(transitions)), digest, base, worktree.Path
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	runDirectory := filepath.Join(location.StateRoot, "runs", "run:verify")
	if err := os.WriteFile(filepath.Join(runDirectory, "task-spec.json"), taskData, 0o600); err != nil {
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
	if exit := Run([]string{"task", "verify", "--run", "run:verify", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("verify exit = %d, stderr = %s", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status": "pass"`) {
		t.Fatalf("verify output = %s", stdout.String())
	}
	verifiedState, err := store.Inspect("run:verify")
	if err != nil {
		t.Fatal(err)
	}
	if verifiedState.State != domain.StateReviewPending || verifiedState.Sequence != 5 {
		t.Fatalf("verified state = %+v", verifiedState)
	}
}

func runGitCLI(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
