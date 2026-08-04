package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

func TestDoctorReportsCompiledContracts(t *testing.T) {
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")

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
		Workers         []struct {
			AdapterID     string `json:"adapterId"`
			Outcome       string `json:"outcome"`
			Compatibility string `json:"compatibility"`
		} `json:"workers"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	if report.Status != "ok" || report.ContractSchemas != 15 || report.WorkerAdapters != 0 || report.Milestone != "6" || len(report.Workers) != 3 {
		t.Fatalf("doctor report = %+v", report)
	}
	for index, adapterID := range []string{"opencode", "qwen", "pi"} {
		if report.Workers[index].AdapterID != adapterID || report.Workers[index].Outcome != app.WorkerOutcomeNotConfigured || report.Workers[index].Compatibility != "not-probed" {
			t.Fatalf("doctor worker %d = %+v", index, report.Workers[index])
		}
	}
}

func TestDoctorReportsCompatibilityWithoutLocalDetails(t *testing.T) {
	for _, test := range []struct {
		name, version, compatibility string
		exit                         int
	}{
		{name: "supported", version: "1.18.12", compatibility: "supported", exit: 0},
		{name: "unsupported", version: "1.19.0", compatibility: "unsupported", exit: 0},
		{name: "probe failure", version: "top-secret-version", compatibility: "probe-failed", exit: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable := filepath.Join(t.TempDir(), "secret-opencode-location")
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s'\nexit %d\n", test.version, test.exit)
			if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MARSHAL_OPENCODE_PATH", executable)
			t.Setenv("MARSHAL_QWEN_PATH", "")
			t.Setenv("MARSHAL_PI_PATH", "")

			var stdout, stderr bytes.Buffer
			exitCode := Run([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr)
			if exitCode != ExitOK {
				t.Fatalf("doctor exit = %d, stderr = %s", exitCode, stderr.String())
			}
			var report struct {
				Workers []doctorWorker `json:"workers"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Workers) != 3 || report.Workers[0].AdapterID != "opencode" || report.Workers[0].Compatibility != test.compatibility {
				t.Fatalf("workers = %+v", report.Workers)
			}
			output := stdout.String() + stderr.String()
			if strings.Contains(output, executable) || strings.Contains(output, "secret-opencode-location") || strings.Contains(output, "top-secret-version") {
				t.Fatalf("doctor leaked local detail: %s", output)
			}
			if test.compatibility != "probe-failed" && report.Workers[0].BinaryVersion != test.version {
				t.Fatalf("binary version = %q, want %q", report.Workers[0].BinaryVersion, test.version)
			}
		})
	}
}

func TestDoctorCanceledContextDoesNotProbeWorkers(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "probed")
	executable := filepath.Join(t.TempDir(), "opencode")
	script := "#!/bin/sh\n: > \"" + marker + "\"\nprintf '1.18.12\\n'\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_OPENCODE_PATH", executable)
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if exit := RunContext(ctx, []string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("doctor exit = %d, stderr = %s", exit, stderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("doctor probed after cancellation: %v", err)
	}
	var report struct {
		Workers []doctorWorker `json:"workers"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Workers) != 3 || report.Workers[0].Compatibility != "not-probed" {
		t.Fatalf("workers = %+v", report.Workers)
	}
}

func TestDoctorRejectsInvalidRunBeforeWorkerProbe(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "probed")
	executable := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n: > \""+marker+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_OPENCODE_PATH", executable)
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"doctor", "--run", "../escape", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("doctor exit = %d, stderr = %s", exit, stderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("invalid request probed worker: %v", err)
	}
}

func TestDoctorRunReconcilesEvidenceAndBlocksCorruption(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	repositoryRoot, err = filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	const remoteURL = "https://example.invalid/marshal-doctor-run.git"
	runGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}
	executable := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '1.18.12\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_OPENCODE_PATH", executable)
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")
	const (
		taskID = "doctor-run-task"
		runID  = "doctor-run-01"
	)
	taskPath := filepath.Join(t.TempDir(), "task.json")
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	writeCLIFixture(t, taskPath, cliPlanningTask(t, repositoryRoot, taskID, remoteURL))
	writeCLIFixture(t, policyPath, cliPlanningPolicy(t, taskID, runID))
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("plan exit = %d, stderr = %s", exit, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"doctor", "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("doctor exit = %d, stdout = %s, stderr = %s", exit, stdout.String(), stderr.String())
	}
	var healthy doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &healthy); err != nil {
		t.Fatal(err)
	}
	if healthy.Status != "ok" || healthy.Run == nil || healthy.Run.Status != "ok" || len(healthy.Run.Findings) != 0 {
		t.Fatalf("healthy report = %+v", healthy)
	}

	capabilityPath := filepath.Join(repositoryRoot, ".marshal", "runs", runID, "capability-snapshot.json")
	capabilityData, err := os.ReadFile(capabilityPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capabilityPath, []byte(`{"secret":"must-not-leak"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"doctor", "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure {
		t.Fatalf("corrupt doctor exit = %d, stderr = %s", exit, stderr.String())
	}
	var corrupt doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &corrupt); err != nil {
		t.Fatal(err)
	}
	if corrupt.Status != "blocked" || corrupt.Run == nil {
		t.Fatalf("corrupt report = %+v", corrupt)
	}
	found := false
	for _, finding := range corrupt.Run.Findings {
		found = found || finding.Code == "capability-snapshot-invalid"
	}
	if !found || strings.Contains(stdout.String()+stderr.String(), "must-not-leak") || strings.Contains(stdout.String()+stderr.String(), capabilityPath) {
		t.Fatalf("unsafe corrupt report: %s%s", stdout.String(), stderr.String())
	}

	if err := os.WriteFile(capabilityPath, capabilityData, 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(repositoryRoot, ".marshal", "runs", runID, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"secret":"damaged-state"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"doctor", "--run", runID, "--repair", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("repair exit = %d, stdout = %s, stderr = %s", exit, stdout.String(), stderr.String())
	}
	var repaired doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &repaired); err != nil {
		t.Fatal(err)
	}
	if repaired.Status != "ok" || repaired.Run == nil || repaired.Run.Status != "ok" || repaired.Repair == nil || repaired.Repair.Outcome != "applied" || repaired.Repair.EventID == "" {
		t.Fatalf("repair report = %+v", repaired)
	}
	if strings.Contains(stdout.String()+stderr.String(), "damaged-state") || strings.Contains(stdout.String()+stderr.String(), statePath) {
		t.Fatalf("repair leaked damaged state: %s%s", stdout.String(), stderr.String())
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
		if command == "plan" || command == "run" || command == "status" || command == "verify" || command == "review" || command == "publish" || command == "accept" {
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

func TestTaskPlanRequiresAllNamedArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"none":           {"task", "plan"},
		"missing task":   {"task", "plan", "--policy", "policy.json", "--run", "run-1"},
		"missing policy": {"task", "plan", "--task", "task.json", "--run", "run-1"},
		"missing run":    {"task", "plan", "--task", "task.json", "--policy", "policy.json"},
		"positional":     {"task", "plan", "--task", "task.json", "--policy", "policy.json", "--run", "run-1", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exitCode := Run(args, strings.NewReader(""), &stdout, &stderr)
			if exitCode != ExitUsage {
				t.Fatalf("Run(%v) exit = %d, want %d; stderr=%s", args, exitCode, ExitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), "marshal task plan --task PATH --policy PATH --run RUN_ID") {
				t.Fatalf("usage missing from stderr: %q", stderr.String())
			}
		})
	}
}

func TestTaskPlanEndToEndFreezesSelectedAdapter(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	const remoteURL = "https://example.invalid/marshal-cli-plan.git"
	runGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)

	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}

	executable := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '1.18.12\\n'; exit 0; fi\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_OPENCODE_PATH", executable)
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")

	const (
		taskID = "cli-plan-task"
		runID  = "cli-plan-run"
	)
	taskPath := filepath.Join(t.TempDir(), "task.json")
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	writeCLIFixture(t, taskPath, cliPlanningTask(t, repositoryRoot, taskID, remoteURL))
	writeCLIFixture(t, policyPath, cliPlanningPolicy(t, taskID, runID))

	stdout.Reset()
	stderr.Reset()
	exit := Run([]string{"task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("task plan exit = %d, stderr = %s", exit, stderr.String())
	}
	var result struct {
		State domain.RunState `json:"state"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode planning result: %v", err)
	}
	if result.State.State != domain.StateReady || result.State.RunID != runID || result.State.TaskID != taskID {
		t.Fatalf("planning state = %+v", result.State)
	}
	capability, err := os.ReadFile(filepath.Join(repositoryRoot, ".marshal", "runs", runID, "capability-snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var identity struct {
		AdapterID string `json:"adapterId"`
	}
	if err := json.Unmarshal(capability, &identity); err != nil || identity.AdapterID != "opencode" {
		t.Fatalf("frozen capability adapter = %q, err = %v", identity.AdapterID, err)
	}
}

func TestTaskRunUsesFrozenFallbackAdapter(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	const remoteURL = "https://example.invalid/marshal-cli-run.git"
	runGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}
	marker := filepath.Join(t.TempDir(), "pi-started")
	executable := filepath.Join(t.TempDir(), "pi")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then printf '0.83.0\\n'; exit 0; fi\n" +
		": > \"" + marker + "\"\n" +
		"exit 1\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", executable)

	const (
		taskID = "cli-run-fallback-task"
		runID  = "cli-run-fallback-run"
	)
	taskPath := filepath.Join(t.TempDir(), "task.json")
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	writeCLIFixture(t, taskPath, cliPlanningTaskWithWorkers(t, repositoryRoot, taskID, remoteURL, "opencode", []any{"pi"}))
	writeCLIFixture(t, policyPath, cliPlanningPolicyWithWorkers(t, taskID, runID, true, []any{"opencode", "pi"}))
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("task plan exit = %d, stderr = %s", exit, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	exit := Run([]string{"task", "run", "--run", runID}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitFailure {
		t.Fatalf("task run exit = %d, want worker failure %d; stderr = %s", exit, ExitFailure, stderr.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("frozen Pi adapter was not started: %v; stderr = %s", err, stderr.String())
	}
	capability, err := os.ReadFile(filepath.Join(repositoryRoot, ".marshal", "runs", runID, "capability-snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var identity struct {
		AdapterID string `json:"adapterId"`
	}
	if err := json.Unmarshal(capability, &identity); err != nil || identity.AdapterID != "pi" {
		t.Fatalf("frozen capability adapter = %q, err = %v", identity.AdapterID, err)
	}
}

func TestTaskRunRejectsUnsafeOrUnavailableFrozenIdentityBeforeWorkerStart(t *testing.T) {
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
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")

	t.Run("invalid run id", func(t *testing.T) {
		stdout.Reset()
		stderr.Reset()
		exit := Run([]string{"task", "run", "--run", "../../escape"}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitUsage || !strings.Contains(stderr.String(), "Run ID 无效") {
			t.Fatalf("exit = %d, stderr = %q", exit, stderr.String())
		}
		if _, err := os.Stat(filepath.Join(repositoryRoot, "escape")); !os.IsNotExist(err) {
			t.Fatalf("invalid Run ID created an escaped path: %v", err)
		}
	})

	t.Run("missing snapshot", func(t *testing.T) {
		stdout.Reset()
		stderr.Reset()
		exit := Run([]string{"task", "run", "--run", "missing-run"}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitFailure || stderr.String() != "运行失败：读取冻结 CapabilitySnapshot 失败。\n" {
			t.Fatalf("exit = %d, stderr = %q", exit, stderr.String())
		}
	})

	t.Run("malformed snapshot", func(t *testing.T) {
		runDir := filepath.Join(repositoryRoot, ".marshal", "runs", "malformed-run")
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "capability-snapshot.json"), []byte(`{"secret":"must-not-leak"`), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout.Reset()
		stderr.Reset()
		exit := Run([]string{"task", "run", "--run", "malformed-run"}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitFailure || stderr.String() != "运行失败：冻结 CapabilitySnapshot 无效。\n" || strings.Contains(stderr.String(), "must-not-leak") {
			t.Fatalf("exit = %d, stderr = %q", exit, stderr.String())
		}
	})

	t.Run("unregistered frozen adapter", func(t *testing.T) {
		runDir := filepath.Join(repositoryRoot, ".marshal", "runs", "unregistered-run")
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			t.Fatal(err)
		}
		capability := readCLIFixture(t, "examples/happy-path/capability-snapshot.json")
		capability["adapterId"] = "pi"
		writeCLIFixture(t, filepath.Join(runDir, "capability-snapshot.json"), capability)
		stdout.Reset()
		stderr.Reset()
		exit := Run([]string{"task", "run", "--run", "unregistered-run"}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitUnavailable || stderr.String() != "运行失败：冻结 Worker Adapter 当前未配置或不可用。\n" {
			t.Fatalf("exit = %d, stderr = %q", exit, stderr.String())
		}
	})
}

func cliPlanningTask(t *testing.T, repositoryRoot, taskID, remoteURL string) map[string]any {
	return cliPlanningTaskWithWorkers(t, repositoryRoot, taskID, remoteURL, "opencode", []any{})
}

func cliPlanningTaskWithWorkers(t *testing.T, repositoryRoot, taskID, remoteURL, preferred string, fallbacks []any) map[string]any {
	t.Helper()
	fixture := readCLIFixture(t, "examples/happy-path/task-spec.json")
	fixture["metadata"].(map[string]any)["id"] = taskID
	repository := fixture["repository"].(map[string]any)
	repository["path"] = repositoryRoot
	repository["baseRef"] = "HEAD"
	repository["remote"] = "origin"
	repository["expectedRemoteUrl"] = remoteURL
	worker := fixture["worker"].(map[string]any)
	worker["preferredAdapter"] = preferred
	worker["fallbackAdapters"] = fallbacks
	worker["executionProfile"] = "workspace-write"
	worker["sessionPolicy"] = "ephemeral"
	fixture["deliverables"] = []any{map[string]any{"id": "implementation", "kind": "code", "required": true, "pathGlob": "README.md"}}
	publication := fixture["publication"].(map[string]any)
	publication["required"] = false
	publication["provider"] = "none"
	publication["mode"] = "none"
	publication["remote"] = "origin"
	publication["baseBranch"] = "main"
	publication["mergePolicy"] = "never"
	publication["requiredChecks"] = []any{}
	return fixture
}

func cliPlanningPolicy(t *testing.T, taskID, runID string) map[string]any {
	return cliPlanningPolicyWithWorkers(t, taskID, runID, false, []any{"opencode"})
}

func cliPlanningPolicyWithWorkers(t *testing.T, taskID, runID string, allowFallback bool, allowed []any) map[string]any {
	t.Helper()
	fixture := readCLIFixture(t, "examples/happy-path/policy-snapshot.json")
	fixture["taskId"] = taskID
	fixture["runId"] = runID
	effective := fixture["effective"].(map[string]any)
	effective["allowFallbackWorkers"] = allowFallback
	effective["allowPublication"] = false
	effective["allowMerge"] = false
	effective["allowedAdapters"] = allowed
	return fixture
}

func readCLIFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := marshalSchemas.FS.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func writeCLIFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
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
